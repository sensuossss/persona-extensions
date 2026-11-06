package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/github/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/github/internal/kb"
)

// The knowledge base (docs/plan-connectors.md「四 · 知识库」): the PRs and issues the user is involved in
// (involves:@me — author, assignee, mentioned, commented, review requested), never whole repositories. One chat per
// PR / issue, kind pr or issue: its description, then comments, reviews and inline review comments in time order.

var (
	archiveFirstDays = 30                  // how far back the first run looks
	archiveForget    = 90 * 24 * time.Hour // threads quiet this long leave the cursor
	archiveText      = 4000                // characters of one message kept
	archivePages     = 10                  // pages of one list read per thread (100 each)
)

// archiveCursor is the connector's cursor: the updated_at reached, and per thread the newest message archived.
type archiveCursor struct {
	Since   time.Time            `json:"since"`
	Threads map[string]time.Time `json:"threads,omitempty"`
}

// Changes reads the threads updated since the cursor, oldest first, with the messages not archived yet.
func (c *Connector) Changes(ctx context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	if !c.signedIn() || c.ratesLow() {
		return nil, cursor, false, nil
	}
	var cur archiveCursor
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &cur); err != nil {
			return nil, cursor, false, fmt.Errorf("cursor: %v", err)
		}
	}
	if cur.Threads == nil {
		cur.Threads = map[string]time.Time{}
	}
	if cur.Since.IsZero() {
		cur.Since = time.Now().UTC().AddDate(0, 0, -archiveFirstDays)
	}
	me := c.Status(ctx).Account
	q := url.Values{"q": {"involves:@me updated:>=" + cur.Since.UTC().Format(time.RFC3339)}, "sort": {"updated"}, "order": {"asc"},
		"per_page": {fmt.Sprint(max(1, min(limit, 100)))}}
	var res struct {
		Total int     `json:"total_count"`
		Items []issue `json:"items"`
	}
	if _, err := c.Get(ctx, "/search/issues?"+q.Encode(), &res); err != nil {
		return nil, cursor, false, err
	}
	var batches []connector.ChatBatch
	for _, it := range res.Items {
		thread := it.repo() + "#" + fmt.Sprint(it.Number)
		msgs, err := c.threadMessages(ctx, it, cur.Threads[thread], me)
		if err != nil {
			return batches, encode(cur), false, err // what was read so far is archived; this thread comes again
		}
		if len(msgs) > 0 {
			cur.Threads[thread] = msgs[len(msgs)-1].At
			kind := "issue"
			if it.PullRequest != nil {
				kind = "pr"
			}
			owner, name, _ := strings.Cut(it.repo(), "/")
			batches = append(batches, connector.ChatBatch{Chat: kb.SourceChat{
				ID:   fmt.Sprintf("%s-%s-%d", owner, name, it.Number),
				Name: fmt.Sprintf("%s#%d %s", name, it.Number, it.Title), Kind: kind, UpdatedAt: it.UpdatedAt}, Messages: msgs})
		}
		if it.UpdatedAt.After(cur.Since) {
			cur.Since = it.UpdatedAt
		}
	}
	for t, at := range cur.Threads {
		if time.Since(at) > archiveForget {
			delete(cur.Threads, t)
		}
	}
	return batches, encode(cur), res.Total > len(res.Items), nil
}

func encode(cur archiveCursor) string {
	b, _ := json.Marshal(cur)
	return string(b)
}

// threadMessages are a thread's messages after after (all of them, the description first, when it is zero).
func (c *Connector) threadMessages(ctx context.Context, it issue, after time.Time, me string) ([]kb.SourceMessage, error) {
	repo, n := it.repo(), it.Number
	var out []kb.SourceMessage
	add := func(id string, u user, at time.Time, text string) {
		text = strings.TrimSpace(text)
		if !at.After(after) || text == "" {
			return
		}
		if r := []rune(text); len(r) > archiveText {
			text = string(r[:archiveText]) + "…"
		}
		out = append(out, kb.SourceMessage{ID: id, SenderID: u.Login, SenderName: who(u), Mine: me != "" && strings.EqualFold(u.Login, me),
			Type: "TEXT", Text: text, At: at, SenderHandles: []kb.Handle{{Kind: kb.HandleGitHub, Value: u.Login}}})
	}
	add("body", it.User, it.CreatedAt, "【"+it.Title+"】\n"+it.Body)

	since := ""
	if !after.IsZero() {
		since = "&since=" + url.QueryEscape(after.UTC().Format(time.RFC3339))
	}
	var cs []struct {
		ID int64 `json:"id"`
		comment
	}
	if err := getPages(ctx, c, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100%s", repo, n, since), &cs); err != nil {
		return nil, err
	}
	for _, cm := range cs {
		add(fmt.Sprint("c", cm.ID), cm.User, cm.CreatedAt, cm.Body)
	}
	if it.PullRequest != nil {
		var rs []struct {
			ID int64 `json:"id"`
			comment
		}
		if err := getPages(ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			verb := map[string]string{"APPROVED": "批准了", "CHANGES_REQUESTED": "要求修改", "COMMENTED": "评审了"}[r.State]
			if verb == "" || r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
				continue
			}
			text := "（review）" + verb
			if b := strings.TrimSpace(r.Body); b != "" {
				text += "：" + b
			}
			add(fmt.Sprint("r", r.ID), r.User, r.SubmittedAt, text)
		}
		var ics []struct {
			ID        int64     `json:"id"`
			User      user      `json:"user"`
			Body      string    `json:"body"`
			Path      string    `json:"path"`
			Line      *int      `json:"line"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := getPages(ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100%s", repo, n, since), &ics); err != nil {
			return nil, err
		}
		for _, ic := range ics {
			where := ic.Path
			if ic.Line != nil {
				where += fmt.Sprintf(":%d", *ic.Line)
			}
			add(fmt.Sprint("i", ic.ID), ic.User, ic.CreatedAt, "（"+where+"）"+ic.Body)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// getPages reads a list of 100 a page until a short page, so the cursor only moves past a thread read whole; reviews
// have no since and come oldest first, and the newest messages are the ones past the first page.
func getPages[T any](ctx context.Context, c *Connector, path string, out *[]T) error {
	for page := 1; page <= archivePages; page++ {
		var p []T
		if _, err := c.Get(ctx, fmt.Sprintf("%s&page=%d", path, page), &p); err != nil {
			return err
		}
		if *out = append(*out, p...); len(p) < 100 {
			break
		}
	}
	return nil
}
