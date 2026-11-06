package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeArchiveAPI is search and one PR's conversation on made-up data; comments after since are what since asks.
func fakeArchiveAPI(t *testing.T) (*Connector, *[]string) {
	var mu sync.Mutex
	var seen []string
	routes := map[string]string{
		"/user": `{"login":"me"}`,
		"/search/issues": `{"total_count":1,"items":[{"number":12,"title":"Add the example","state":"open","user":{"login":"alice"},
			"body":"示例描述","repository_url":"https://api.github.com/repos/example-org/example-repo","created_at":"2026-11-01T08:00:00Z",
			"updated_at":"2026-11-02T10:00:00Z","pull_request":{}}]}`,
		"/repos/example-org/example-repo/issues/12/comments": `[
			{"id":1,"user":{"login":"bob"},"body":"请看一下","created_at":"2026-11-01T09:00:00Z"},
			{"id":2,"user":{"login":"ci-bot[bot]","type":"Bot"},"body":"Preview ready.","created_at":"2026-11-01T09:30:00Z"}]`,
		"/repos/example-org/example-repo/pulls/12/reviews": `[
			{"id":3,"user":{"login":"me"},"state":"APPROVED","body":"","submitted_at":"2026-11-02T10:00:00Z"},
			{"id":4,"user":{"login":"bob"},"state":"COMMENTED","body":"","submitted_at":"2026-11-01T09:10:00Z"}]`,
		"/repos/example-org/example-repo/pulls/12/comments": `[
			{"id":5,"user":{"login":"bob"},"body":"这里要判空","path":"src/example.go","line":42,"created_at":"2026-11-01T09:05:00Z"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		if b, ok := routes[r.URL.Path]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c, &seen
}

func TestChangesArchiveAThreadOnce(t *testing.T) {
	c, seen := fakeArchiveAPI(t)
	batches, next, more, err := c.Changes(context.Background(), "", 20)
	if err != nil || more || len(batches) != 1 {
		t.Fatalf("%d batches, more %v, %v", len(batches), more, err)
	}
	b := batches[0]
	if b.Chat.ID != "example-org-example-repo-12" || b.Chat.Kind != "pr" || b.Chat.Name != "example-repo#12 Add the example" {
		t.Fatalf("chat %+v", b.Chat)
	}
	var lines []string
	for _, m := range b.Messages {
		lines = append(lines, m.SenderName+"|"+m.Text)
	}
	want := []string{"alice|【Add the example】\n示例描述", "bob|请看一下", "bob|（src/example.go:42）这里要判空", "ci-bot[bot] (bot)|Preview ready.", "me|（review）批准了"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if !b.Messages[len(b.Messages)-1].Mine {
		t.Error("my review is not mine")
	}
	if q := (*seen)[1]; !strings.Contains(q, "involves%3A%40me+updated%3A%3E%3D") || !strings.Contains(q, "order=asc") {
		t.Errorf("search %s", q)
	}
	var cur archiveCursor
	_ = json.Unmarshal([]byte(next), &cur)
	if !cur.Since.Equal(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)) || cur.Threads["example-org/example-repo#12"].IsZero() {
		t.Fatalf("cursor %s", next)
	}

	// Again from the cursor: nothing new is archived twice, and the comments are asked since the last one.
	batches, _, _, err = c.Changes(context.Background(), next, 20)
	if err != nil || len(batches) != 0 {
		t.Fatalf("again: %d batches, %v", len(batches), err)
	}
	if q := (*seen)[len(*seen)-1]; !strings.Contains(q, "since=2026-11-02T10%3A00%3A00Z") {
		t.Errorf("not asked since the last message: %s", q)
	}
}

// fakeBusyArchiveAPI pages lists like GitHub (per_page / page) and honours since and the search's updated:>=: issue #7
// has 150 comments, PR #8, updated later, 130 reviews.
func fakeBusyArchiveAPI(t *testing.T) *Connector {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	type item struct {
		at   time.Time
		json string
	}
	var comments, reviews []item
	for i := range 150 {
		at := t0.Add(time.Duration(i) * time.Minute)
		comments = append(comments, item{at, fmt.Sprintf(`{"id":%d,"user":{"login":"bob"},"body":"comment %d","created_at":%q}`, i+1, i+1, at.Format(time.RFC3339))})
	}
	for i := range 130 {
		at := t0.Add(time.Duration(i) * time.Minute)
		reviews = append(reviews, item{at, fmt.Sprintf(`{"id":%d,"user":{"login":"carol"},"state":"APPROVED","body":"review %d","submitted_at":%q}`, 1000+i, i+1, at.Format(time.RFC3339))})
	}
	threads := []item{
		{time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), `{"number":7,"title":"Busy issue","state":"open","user":{"login":"alice"},"body":"desc",
			"repository_url":"https://api.github.com/repos/example-org/example-repo","created_at":"2026-10-31T00:00:00Z","updated_at":"2026-11-02T10:00:00Z"}`},
		{time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), `{"number":8,"title":"Busy PR","state":"open","user":{"login":"alice"},"body":"desc",
			"repository_url":"https://api.github.com/repos/example-org/example-repo","created_at":"2026-10-31T00:00:00Z","updated_at":"2026-11-03T10:00:00Z","pull_request":{}}`},
	}
	lists := map[string][]item{
		"/repos/example-org/example-repo/issues/7/comments": comments, "/repos/example-org/example-repo/issues/8/comments": nil,
		"/repos/example-org/example-repo/pulls/8/reviews": reviews, "/repos/example-org/example-repo/pulls/8/comments": nil,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var keep []item
		switch all, ok := lists[r.URL.Path]; {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
			return
		case r.URL.Path == "/search/issues":
			_, from, _ := strings.Cut(q.Get("q"), "updated:>=")
			since, _ := time.Parse(time.RFC3339, from)
			for _, it := range threads {
				if !it.at.Before(since) {
					keep = append(keep, it)
				}
			}
			var js []string
			for _, it := range keep {
				js = append(js, it.json)
			}
			_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, len(keep), strings.Join(js, ","))
			return
		case ok:
			since, _ := time.Parse(time.RFC3339, q.Get("since"))
			for _, it := range all {
				if !it.at.Before(since) {
					keep = append(keep, it)
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		per, _ := strconv.Atoi(q.Get("per_page"))
		if per <= 0 {
			per = 30
		}
		page, _ := strconv.Atoi(q.Get("page"))
		page = max(page, 1)
		var js []string
		for _, it := range keep[min((page-1)*per, len(keep)):min(page*per, len(keep))] {
			js = append(js, it.json)
		}
		_, _ = w.Write([]byte("[" + strings.Join(js, ",") + "]"))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c
}

// A thread with more than 100 comments or reviews is archived whole, not just its first page.
func TestChangesArchiveEveryPageOfABusyThread(t *testing.T) {
	c := fakeBusyArchiveAPI(t)
	got := map[string][]string{}
	cursor := ""
	for range 2 { // the second run, from the cursor, brings nothing the first missed
		batches, next, _, err := c.Changes(context.Background(), cursor, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range batches {
			for _, m := range b.Messages {
				got[b.Chat.ID] = append(got[b.Chat.ID], m.Text)
			}
		}
		cursor = next
	}
	for id, want := range map[string]int{"example-org-example-repo-7": 151, "example-org-example-repo-8": 131} {
		if ms := got[id]; len(ms) != want {
			t.Errorf("%s: archived %d of %d messages; the last is %q", id, len(ms), want, ms[len(ms)-1])
		}
	}
}
