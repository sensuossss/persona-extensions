package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeAPI is GitHub's REST API on made-up data: example-org/example-repo, alice and bob. Paths not listed are 404s.
func fakeAPI(t *testing.T) (*Connector, *[]string) {
	long := strings.Repeat("示例描述。", 1000)
	routes := map[string]string{
		"/search/issues": `{"total_count":2,"items":[
			{"number":12,"title":"Add the example","state":"open","user":{"login":"alice"},"html_url":"https://github.com/example-org/example-repo/pull/12",
			 "repository_url":"https://api.github.com/repos/example-org/example-repo","updated_at":"2026-01-02T03:04:05Z","comments":3,"pull_request":{"merged_at":null}},
			{"number":7,"title":"Example bug","state":"closed","user":{"login":"bob"},"html_url":"https://github.com/example-org/example-repo/issues/7",
			 "repository_url":"https://api.github.com/repos/example-org/example-repo","updated_at":"2026-01-01T00:00:00Z","comments":0}]}`,
		"/search/code": `{"total_count":1,"items":[{"path":"src/example.go","html_url":"https://github.com/example-org/example-repo/blob/main/src/example.go",
			"repository":{"full_name":"example-org/example-repo"}}]}`,
		"/repos/example-org/example-repo/issues/12": `{"number":12,"title":"Add the example","state":"open","user":{"login":"alice"},
			"body":"` + long + `","html_url":"https://github.com/example-org/example-repo/pull/12","created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T03:04:05Z","comments":2,"labels":[{"name":"enhancement"}],"pull_request":{"merged_at":null}}`,
		"/repos/example-org/example-repo/pulls/12": `{"head":{"ref":"feat/example","sha":"0123456789abcdef0123456789abcdef01234567"},"base":{"ref":"main"},
			"mergeable_state":"clean","additions":10,"deletions":2,"changed_files":2,"requested_reviewers":[{"login":"bob"}],"requested_teams":[{"slug":"reviewers"}]}`,
		"/repos/example-org/example-repo/pulls/12/reviews": `[
			{"user":{"login":"bob"},"state":"CHANGES_REQUESTED","body":"请改一下示例。","submitted_at":"2026-01-02T01:00:00Z"},
			{"user":{"login":"bob"},"state":"COMMENTED","body":"","submitted_at":"2026-01-02T01:00:01Z"}]`,
		"/repos/example-org/example-repo/issues/12/comments": `[
			{"user":{"login":"ci-bot[bot]","type":"Bot"},"body":"Preview ready.","created_at":"2026-01-01T12:00:00Z"},
			{"user":{"login":"alice"},"body":"改好了。","created_at":"2026-01-02T03:00:00Z"}]`,
		"/repos/example-org/example-repo/pulls/12/files": `[
			{"filename":"src/example.go","status":"modified","additions":8,"deletions":2,"patch":"@@ -1 +1 @@\n-old\n+new"},
			{"filename":"docs/new.md","previous_filename":"docs/old.md","status":"renamed","additions":2,"deletions":0},
			{"filename":"assets/big.bin","status":"added","additions":0,"deletions":0}]`,
		"/repos/example-org/example-repo/commits/0123456789abcdef0123456789abcdef01234567/check-runs": `{"total_count":2,"check_runs":[
			{"id":101,"name":"test","status":"completed","conclusion":"failure","html_url":"https://github.com/example-org/example-repo/actions/runs/1/job/101",
			 "app":{"slug":"github-actions"},"output":{"title":"Process completed with exit code 1."}},
			{"id":102,"name":"lint","status":"in_progress","app":{"slug":"github-actions"}}]}`,
		"/repos/example-org/example-repo/actions/jobs/101/logs": strings.Repeat("setup line\n", 2000) + "--- FAIL: TestExample\nexit status 1\n",
	}
	// PR 14 is busy: 101 comments and 101 reviews, listed oldest first a page at a time. Only the last review
	// (bob's approval, the newest entry) has a verdict; the 100 before it are empty inline-thread replies.
	routes["/repos/example-org/example-repo/issues/14"] = `{"number":14,"title":"Busy example","state":"open","user":{"login":"alice"},
		"body":"desc","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z","comments":101,"pull_request":{"merged_at":null}}`
	routes["/repos/example-org/example-repo/pulls/14"] = `{"head":{"ref":"feat/busy","sha":"0123456789abcdef0123456789abcdef01234567"},"base":{"ref":"main"}}`
	lists := map[string][]string{}
	for i := 1; i <= 101; i++ {
		at := fmt.Sprintf("2026-01-02T%02d:%02d:00Z", i/60, i%60)
		lists["/repos/example-org/example-repo/issues/14/comments"] = append(lists["/repos/example-org/example-repo/issues/14/comments"],
			fmt.Sprintf(`{"user":{"login":"bob"},"body":"comment %d","created_at":%q}`, i, at))
		review := fmt.Sprintf(`{"user":{"login":"bob"},"state":"COMMENTED","body":"","submitted_at":"2026-01-01T%02d:%02d:00Z"}`, i/60, i%60)
		if i == 101 {
			review = `{"user":{"login":"bob"},"state":"APPROVED","body":"","submitted_at":"2026-01-02T23:00:00Z"}`
		}
		lists["/repos/example-org/example-repo/pulls/14/reviews"] = append(lists["/repos/example-org/example-repo/pulls/14/reviews"], review)
	}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		if items, ok := lists[r.URL.Path]; ok {
			per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			per, page = max(per, 1), max(page, 1) // GitHub's defaults are 30 and 1; every caller here sets both
			from, to := min((page-1)*per, len(items)), min(page*per, len(items))
			_, _ = w.Write([]byte("[" + strings.Join(items[from:to], ",") + "]"))
			return
		}
		// PR 13 has 1001 changed files: ten full pages of 100 plus file #1001 alone on page 11.
		if r.URL.Path == "/repos/example-org/example-repo/pulls/13/files" {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || page > 11 {
				w.WriteHeader(http.StatusNotFound) // a tripwire: the loop must never page past the short last page
				return
			}
			n := 100
			if page == 11 {
				n = 1
			}
			var b strings.Builder
			b.WriteByte('[')
			for i := 0; i < n; i++ {
				num := (page-1)*100 + i + 1
				if i > 0 {
					b.WriteByte(',')
				}
				if num == 1001 {
					b.WriteString(`{"filename":"src/file1001.go","status":"added","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+file1001"}`)
				} else {
					fmt.Fprintf(&b, `{"filename":"src/file%04d.go","status":"modified","additions":1,"deletions":0,"patch":"@@ -1 +1 @@\n-old\n+new"}`, num)
				}
			}
			b.WriteByte(']')
			_, _ = w.Write([]byte(b.String()))
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	return c, &seen
}

func call(t *testing.T, c *Connector, name string, args string) string {
	t.Helper()
	out, err := c.Call(context.Background(), name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestToolsAreTheFourReadOnes(t *testing.T) {
	c, _ := fakeAPI(t)
	var names []string
	for _, tl := range c.Tools() {
		names = append(names, tl.Name)
		if tl.Description == "" || !strings.HasPrefix(string(tl.InputSchema), `{"type":"object"`) {
			t.Errorf("%s: %+v", tl.Name, tl)
		}
	}
	if fmt.Sprint(names) != "[github_search github_read github_diff github_checks]" {
		t.Fatalf("%v", names)
	}
}

func TestSearch(t *testing.T) {
	c, seen := fakeAPI(t)
	out := call(t, c, "github_search", `{"query":"involves:@me is:open","limit":5}`)
	mustContain(t, out, "2 results",
		"example-org/example-repo#12 [PR open] Add the example — alice, updated 2026-01-02 03:04Z, 3 comments https://github.com/example-org/example-repo/pull/12",
		"example-org/example-repo#7 [issue closed] Example bug — bob")
	if q := (*seen)[0]; !strings.Contains(q, "q=involves%3A%40me+is%3Aopen") || !strings.Contains(q, "per_page=5") {
		t.Errorf("query %s", q)
	}
	out = call(t, c, "github_search", `{"query":"repo:example-org/example-repo Example","kind":"code"}`)
	mustContain(t, out, "1 results", "example-org/example-repo src/example.go")
	if _, err := c.Call(context.Background(), "github_search", json.RawMessage(`{"query":" "}`)); err == nil {
		t.Error("an empty query was searched")
	}
}

func TestReadAPullRequest(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_read", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "Pull request example-org/example-repo#12: Add the example", "State: open · by alice",
		"Labels: enhancement", "Branch: feat/example → main (head 0123456789ab) · 2 files, +10 −2 · mergeable: clean",
		"Review requested from: bob, team reviewers", "more characters cut]",
		"--- 3 comments and reviews, newest first",
		"2026-01-02 03:00Z alice:\n改好了。", "bob requested changes:\n请改一下示例。", "ci-bot[bot] (bot):")
	// newest first; an empty COMMENTED review (inline comments only) is not an entry
	if strings.Index(out, "改好了") > strings.Index(out, "请改一下") || strings.Index(out, "请改一下") > strings.Index(out, "Preview ready") {
		t.Errorf("not newest first:\n%s", out)
	}
	if strings.Count(out, "bob reviewed") != 0 {
		t.Errorf("an empty review is listed:\n%s", out)
	}
	if len(out) > bodyCap+3000 {
		t.Errorf("%d bytes: the description was not cut", len(out))
	}
	out = call(t, c, "github_read", `{"repo":"example-org/example-repo","number":12,"comments":1}`)
	mustContain(t, out, "(the newest 1)", "改好了")
	if strings.Contains(out, "Preview ready") {
		t.Errorf("more than one comment:\n%s", out)
	}
}

// With 101 comments the last page holds one: the newest 15 reach back into the page before, and the reviews are
// read past the first 100 so the latest verdict shows.
func TestReadTheNewestAcrossAPageBoundary(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_read", `{"repo":"example-org/example-repo","number":14}`)
	mustContain(t, out, "--- 102 comments and reviews, newest first (the newest 15)", "bob approved",
		"bob:\ncomment 101", "bob:\ncomment 100\n", "bob:\ncomment 88")
	if strings.Contains(out, "comment 87") {
		t.Errorf("more than the newest 15:\n%s", out)
	}
}

func TestDiff(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "3 files changed, +10 −2", "modified src/example.go +8 −2", "renamed docs/old.md → docs/new.md +2 −0")
	out = call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12,"path":"src/example.go"}`)
	mustContain(t, out, "src/example.go (modified, +8 −2)", "+new")
	out = call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12,"path":"assets/big.bin"}`)
	mustContain(t, out, "no patch")
	if _, err := c.Call(context.Background(), "github_diff", json.RawMessage(`{"repo":"example-org/example-repo","number":12,"path":"nope"}`)); err == nil {
		t.Error("a file the PR does not change")
	}

	// A PR with 1001 files pages past the old ten-page cap: the summary totals include file #1001.
	big, bigSeen := fakeAPI(t)
	out = call(t, big, "github_diff", `{"repo":"example-org/example-repo","number":13}`)
	mustContain(t, out, "1001 files changed, +1001 −0", "added src/file1001.go +1 −0")
	if pages := filePages(*bigSeen, 13); pages != 11 {
		t.Errorf("the 1001-file summary fetched %d pages, want 11", pages)
	}

	// A path lookup for a file past #1000 reaches page 11 and returns its patch, not "not among the files".
	later, laterSeen := fakeAPI(t)
	out = call(t, later, "github_diff", `{"repo":"example-org/example-repo","number":13,"path":"src/file1001.go"}`)
	mustContain(t, out, "src/file1001.go (added, +1 −0)", "+file1001")
	if pages := filePages(*laterSeen, 13); pages != 11 {
		t.Errorf("the file #1001 lookup fetched %d pages, want 11", pages)
	}

	// A short first page still stops after one request.
	small, smallSeen := fakeAPI(t)
	call(t, small, "github_diff", `{"repo":"example-org/example-repo","number":12}`)
	if pages := filePages(*smallSeen, 12); pages != 1 {
		t.Errorf("a one-page PR fetched %d pages, want 1", pages)
	}
}

// filePages counts the paged files requests the fake API saw for a pull request.
func filePages(seen []string, number int) int {
	prefix := fmt.Sprintf("/repos/example-org/example-repo/pulls/%d/files?", number)
	n := 0
	for _, u := range seen {
		if strings.HasPrefix(u, prefix) {
			n++
		}
	}
	return n
}

func TestChecksWithTheEndOfAFailedLog(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_checks", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "2 checks on 0123456789ab", "test: failure", "Process completed with exit code 1.", "lint: in_progress",
		"--- FAIL: TestExample\nexit status 1")
	if len(out) > logTail+1000 {
		t.Errorf("%d bytes: the log was not cut to its end", len(out))
	}
}

func TestTailKeepsValidUTF8OnAnUnbrokenLine(t *testing.T) {
	// Over logTail bytes with no newline; 6301 is not a multiple of the 3-byte rune,
	// so the byte cut at len-logTail lands in the middle of a rune.
	line := strings.Repeat("中", 2100) + "z"
	if len(line) <= logTail || len(line)%3 == 0 {
		t.Fatalf("test setup: len=%d must be > %d and not a multiple of 3", len(line), logTail)
	}
	got := tail(line, logTail)
	if !utf8.ValidString(got) {
		t.Fatalf("tail is not valid UTF-8: %q", got[:40])
	}
	body, ok := strings.CutPrefix(got, "[…]\n")
	if !ok {
		t.Fatalf("tail is missing the marker prefix: %q", got[:20])
	}
	if !strings.HasSuffix(body, "中z") {
		t.Errorf("tail does not keep the final runes: %q", body[len(body)-20:])
	}
	if !strings.HasSuffix(line, body) {
		t.Errorf("tail is not a suffix of the line")
	}
}

func TestBadArguments(t *testing.T) {
	c, seen := fakeAPI(t)
	for _, args := range []string{`{"repo":"../../user","number":1}`, `{"repo":"example-org","number":1}`, `{"repo":"a/b?x=1","number":1}`, `{"repo":"../user","number":1}`, `{"repo":"x/..","number":1}`} {
		if _, err := c.Call(context.Background(), "github_read", json.RawMessage(args)); err == nil {
			t.Errorf("%s was read", args)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("requests sent: %v", *seen)
	}
	if _, err := c.Call(context.Background(), "github_checks", json.RawMessage(`{"repo":"example-org/example-repo"}`)); err == nil {
		t.Error("checks without number or ref")
	}
	if _, err := c.Call(context.Background(), "github_read", json.RawMessage(`{"repo":"example-org/example-repo","number":99}`)); err == nil ||
		!strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("a missing issue: %v", err)
	}
}
