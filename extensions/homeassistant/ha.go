package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Home Assistant's REST API (https://developers.home-assistant.io/docs/api/rest), with a long-lived access token.
// Only what the page and the tools need: every state at once (cached a few seconds), the areas through a template,
// the services, a service call, and an entity's history.

var (
	statesFor   = 5 * time.Second  // the states are read again after this
	areasFor    = 5 * time.Minute  // the areas and their entities
	servicesFor = 10 * time.Minute // the services
)

var (
	errNotConfigured = errors.New("还没填 Home Assistant 的地址和令牌：在扩展的设置里填")
	errUnauthorized  = errors.New("Home Assistant 不认这个令牌（401）：在 Home Assistant 的个人资料 › 安全里新建一个长期访问令牌，填进扩展的密钥")
	errNoEntity      = errors.New("Home Assistant 里没有这个实体")
)

// State is an entity's state as the API answers it.
type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
}

func (s State) Domain() string {
	d, _, _ := strings.Cut(s.EntityID, ".")
	return d
}

// Name is the friendly name, else the entity id.
func (s State) Name() string {
	if n, ok := s.Attributes["friendly_name"].(string); ok && strings.TrimSpace(n) != "" {
		return strings.Join(strings.Fields(n), " ") // integrations join device and entity names with doubled spaces
	}
	return s.EntityID
}

func (s State) attr(k string) string {
	switch v := s.Attributes[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (s State) num(k string) (float64, bool) {
	v, ok := s.Attributes[k].(float64)
	return v, ok
}

func (s State) list(k string) []string {
	raw, _ := s.Attributes[k].([]any)
	var out []string
	for _, x := range raw {
		if str, ok := x.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

// Unavailable says the entity has no state to show.
func (s State) Unavailable() bool {
	return s.State == "unavailable" || s.State == "unknown" || s.State == ""
}

// Area is an area with its entities (those whose device is in it and those placed there themselves).
type Area struct {
	ID       string
	Name     string
	Entities []string
}

// Service is one service of a domain, with its fields' names and what they say.
type Service struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Fields      map[string]map[string]any `json:"fields"`
}

// ha is the client with its caches. Configured once per initialize.
type ha struct {
	base  string // http(s)://host:port, no trailing slash
	token string
	http  *http.Client

	mu        sync.Mutex
	states    []State
	statesAt  time.Time
	areas     []Area
	areasAt   time.Time
	services  map[string]map[string]Service
	servAt    time.Time
	lastOK    time.Time
	lastErr   error
	lastErrAt time.Time
	retry     bool // the user asked to try again (Retry, refresh): the next request goes out even after a refusal
}

// tryAgain lets the next request go out though the token was refused a moment ago.
func (h *ha) tryAgain() {
	h.mu.Lock()
	h.retry = true
	h.mu.Unlock()
}

func newHA(base, token string, insecure bool) *ha {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the user's choice, for a self-signed LAN certificate
	}
	return &ha{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 15 * time.Second, Transport: tr}}
}

func (h *ha) configured() bool { return h != nil && h.base != "" && h.token != "" }

func (h *ha) note(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case err == nil:
		h.lastOK, h.lastErr = time.Now().UTC(), nil
	case errors.Is(err, errNoEntity), errors.Is(err, context.Canceled):
	default:
		h.lastErr, h.lastErrAt = err, time.Now()
	}
}

// authPause is how long a refused token is not tried again on its own: Home Assistant bans an address after a few
// failed logins (http.login_attempts_threshold), and a page that refreshes every 30 seconds would get there. A new
// token (a new client) or an explicit refresh (Retry) tries again at once.
var authPause = 5 * time.Minute

// send sends one request and returns the answer's body; errors are kept for the connector's status.
func (h *ha) send(ctx context.Context, method, path string, body any) ([]byte, error) {
	if !h.configured() {
		return nil, errNotConfigured
	}
	h.mu.Lock()
	refused := h.lastErr != nil && errors.Is(h.lastErr, errUnauthorized) && time.Since(h.lastErrAt) < authPause && !h.retry
	last := h.lastErr
	h.retry = false
	h.mu.Unlock()
	if refused {
		return nil, last
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("Home Assistant 的地址不对：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			err = fmt.Errorf("连不上 Home Assistant（%s）：%w", h.host(), unwrapURL(err))
		}
		h.note(err)
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	switch {
	case err != nil:
		err = fmt.Errorf("读 Home Assistant 的回答出错：%w", err)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		where, _, _ := strings.Cut(path, "?")
		err = fmt.Errorf("%w。被拒的是 %s %s（HTTP %d）", errUnauthorized, method, where, resp.StatusCode)
		if !tokenLooksRight(h.token) {
			err = fmt.Errorf("%w。填的令牌看起来不完整（%s）：长期访问令牌是一长串（一百多个字符）、eyJ 开头、中间有两个点；重新复制一次，整段粘贴", errUnauthorized, tokenShape(h.token))
		}
	case resp.StatusCode == http.StatusNotFound:
		err = errNoEntity
	case resp.StatusCode >= 300:
		msg := strings.TrimSpace(string(raw))
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		err = fmt.Errorf("Home Assistant 回了 HTTP %d：%s", resp.StatusCode, clip(oneLine(msg), 200))
	}
	h.note(err)
	return raw, err
}

// do sends one request and decodes the JSON answer into out (nil: discarded).
func (h *ha) do(ctx context.Context, method, path string, body, out any) error {
	raw, err := h.send(ctx, method, path, body)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Home Assistant 的回答读不懂：%w", err)
	}
	return nil
}

func (h *ha) host() string {
	if u, err := url.Parse(h.base); err == nil {
		return u.Host
	}
	return h.base
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ping checks the address and the token.
func (h *ha) ping(ctx context.Context) error {
	var m struct {
		Message string `json:"message"`
	}
	return h.do(ctx, http.MethodGet, "/api/", nil, &m)
}

// States reads every state, from the cache when younger than fresh.
func (h *ha) States(ctx context.Context, fresh time.Duration) ([]State, error) {
	h.mu.Lock()
	if h.states != nil && fresh > 0 && time.Since(h.statesAt) < fresh {
		s := h.states
		h.mu.Unlock()
		return s, nil
	}
	h.mu.Unlock()
	var s []State
	if err := h.do(ctx, http.MethodGet, "/api/states", nil, &s); err != nil {
		return nil, err
	}
	sort.Slice(s, func(i, j int) bool { return s[i].EntityID < s[j].EntityID })
	h.mu.Lock()
	h.states, h.statesAt = s, time.Now()
	h.mu.Unlock()
	return s, nil
}

// State reads one entity's state.
func (h *ha) State(ctx context.Context, id string) (State, error) {
	var s State
	err := h.do(ctx, http.MethodGet, "/api/states/"+url.PathEscape(id), nil, &s)
	return s, err
}

// cachedState is an entity's state from the last read of all of them (no request): for actions.describe.
func (h *ha) cachedState(id string) (State, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := sort.Search(len(h.states), func(i int) bool { return h.states[i].EntityID >= id })
	if i < len(h.states) && h.states[i].EntityID == id {
		return h.states[i], true
	}
	return State{}, false
}

// areasTemplate lists every area with its entities: the REST API has no area registry, its template engine has.
const areasTemplate = `{% set ns = namespace(o=[]) %}{% for a in areas() %}{% set ns.o = ns.o + [[a, area_name(a), area_entities(a)]] %}{% endfor %}{{ ns.o | tojson }}`

// Areas reads the areas, in Home Assistant's order.
func (h *ha) Areas(ctx context.Context) ([]Area, error) {
	h.mu.Lock()
	if h.areas != nil && time.Since(h.areasAt) < areasFor {
		a := h.areas
		h.mu.Unlock()
		return a, nil
	}
	h.mu.Unlock()
	text, err := h.send(ctx, http.MethodPost, "/api/template", map[string]string{"template": areasTemplate}) // answers text
	if err != nil {
		return nil, err
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(text, &rows); err != nil {
		return nil, fmt.Errorf("Home Assistant 的区域读不懂：%w", err)
	}
	var out []Area
	for _, r := range rows {
		if len(r) != 3 {
			continue
		}
		var a Area
		_ = json.Unmarshal(r[0], &a.ID)
		_ = json.Unmarshal(r[1], &a.Name)
		_ = json.Unmarshal(r[2], &a.Entities)
		if a.ID != "" {
			if a.Name == "" {
				a.Name = a.ID
			}
			out = append(out, a)
		}
	}
	if out == nil {
		out = []Area{}
	}
	h.mu.Lock()
	h.areas, h.areasAt = out, time.Now()
	h.mu.Unlock()
	return out, nil
}

// Services reads every domain's services.
func (h *ha) Services(ctx context.Context) (map[string]map[string]Service, error) {
	h.mu.Lock()
	if h.services != nil && time.Since(h.servAt) < servicesFor {
		s := h.services
		h.mu.Unlock()
		return s, nil
	}
	h.mu.Unlock()
	var list []struct {
		Domain   string             `json:"domain"`
		Services map[string]Service `json:"services"`
	}
	if err := h.do(ctx, http.MethodGet, "/api/services", nil, &list); err != nil {
		return nil, err
	}
	out := map[string]map[string]Service{}
	for _, d := range list {
		out[d.Domain] = d.Services
	}
	h.mu.Lock()
	h.services, h.servAt = out, time.Now()
	h.mu.Unlock()
	return out, nil
}

// Call calls domain.service with data (entity_id among it), and forgets the cached states so the next read shows the
// change.
func (h *ha) Call(ctx context.Context, domain, service string, data map[string]any) error {
	err := h.do(ctx, http.MethodPost, "/api/services/"+url.PathEscape(domain)+"/"+url.PathEscape(service), data, nil)
	h.mu.Lock()
	h.statesAt = time.Time{}
	h.mu.Unlock()
	return err
}

// Change is a state an entity had from At.
type Change struct {
	State string    `json:"state"`
	At    time.Time `json:"last_changed"`
}

// History reads id's changes since since, oldest first.
func (h *ha) History(ctx context.Context, id string, since time.Time) ([]Change, error) {
	q := url.Values{"filter_entity_id": {id}, "minimal_response": {""}, "no_attributes": {""},
		"end_time": {time.Now().UTC().Format(time.RFC3339)}}
	var rows [][]Change
	path := "/api/history/period/" + url.PathEscape(since.UTC().Format(time.RFC3339)) + "?" + q.Encode()
	if err := h.do(ctx, http.MethodGet, path, nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
