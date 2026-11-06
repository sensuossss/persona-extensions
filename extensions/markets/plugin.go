package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// newSources is every source the extension has, the default first (a bare symbol is the default's). Adding a
// market is adding its Source here, its hosts to the manifest's connector hosts and network permission
// (TestManifest checks), and, when it needs a key, a secret in the manifest (docs/extensions/markets.md).
func newSources() sources {
	return append(sources{newOKX(okxBase)}, tencentSources(tencentBase)...)
}

// Config is the extension's configuration (the manifest's form).
type Config struct {
	Watchlist []string `json:"watchlist,omitempty"`
	Bar       string   `json:"bar,omitempty"`
	TZ        string   `json:"tz,omitempty"`
	// Colors is how a change is coloured: "red_up" (红涨绿跌, the default) or "green_up".
	Colors string `json:"colors,omitempty"`
	// OverviewCard: the card on 总览; nil (not set) is on.
	OverviewCard *bool `json:"overview_card,omitempty"`
	// TriageRules: the user's own rules for sorting the alerts in the inbox (read with pluginkit.TriageRules).
	TriageRules string `json:"triage_rules,omitempty"`
}

// defaultWatchlist is the watchlist when the configuration seeds none: the default source's symbols.
var defaultWatchlist = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}

const (
	watchMost  = 50 // the watchlist: one row each in a page of at most 300 items
	searchMost = 30 // what a search shows

	quotesFor  = 10 * time.Second // quotes are read again after this
	candlesFor = 30 * time.Second // candles are read again after this
)

// plugin is the running extension: the sources, the settings, the watchlist and how the last read went.
type plugin struct {
	logf func(string, ...any)
	cfg  Config
	loc  *time.Location // the configured time zone; nil: the app's
	srcs sources

	wmu       sync.Mutex
	watchPath string // "" before initialize, or when the hub gave no data directory: kept in memory only
	watch     []Ref

	alerts alerts
	rules  string // the user's triage rules for the inbox (triage_rules)

	mu        sync.Mutex
	lastOK    time.Time
	lastErr   error
	lastErrAt time.Time
}

func newPlugin(logf func(string, ...any)) *plugin {
	return &plugin{logf: logf, srcs: newSources()}
}

// tone is a change's tone: with 红涨绿跌 (the default) a rise is "danger" (red) and a fall "positive" (green); with
// green_up the other way round.
func (p *plugin) tone(c float64) string {
	up, down := "danger", "positive"
	if p.cfg.Colors == "green_up" {
		up, down = down, up
	}
	switch {
	case c > 0:
		return up
	case c < 0:
		return down
	}
	return "neutral"
}

// bar is the configured default bar when src has it, else src's nearest to 1H, else its first.
func (p *plugin) bar(src Source) string {
	bs := src.Bars()
	for _, b := range []string{p.cfg.Bar, "1H", "1D"} {
		if slices.Contains(bs, b) {
			return b
		}
	}
	if len(bs) > 0 {
		return bs[0]
	}
	return ""
}

// configure reads initialize's config and the stored watchlist (seeded from the config the first time).
func (p *plugin) configure(ip pluginkit.InitParams) error {
	var cfg Config
	if len(ip.Config) > 0 && string(ip.Config) != "null" {
		if err := json.Unmarshal(ip.Config, &cfg); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	p.loc = nil
	if cfg.TZ != "" {
		l, err := time.LoadLocation(cfg.TZ)
		if err != nil {
			return fmt.Errorf("时区 %q 不认识", cfg.TZ)
		}
		p.loc = l
	}
	p.cfg = cfg
	p.rules = pluginkit.TriageRules(ip.Config)
	p.alerts.load(ip.Plugin.DataDir)

	seed := cfg.Watchlist
	if len(seed) == 0 {
		seed = defaultWatchlist
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.watch = p.readRefs(seed)
	p.watchPath = ""
	if ip.Plugin.DataDir != "" {
		p.watchPath = filepath.Join(ip.Plugin.DataDir, "watchlist.json")
		raw, err := os.ReadFile(p.watchPath)
		switch {
		case err == nil:
			var stored []string
			if json.Unmarshal(raw, &stored) == nil {
				p.watch = p.readRefs(stored) // a bare symbol in it is the default source's
			} else {
				p.logf("markets: %s unreadable, starting from the configured watchlist", p.watchPath)
			}
		case !errors.Is(err, os.ErrNotExist):
			p.logf("markets: read watchlist: %v", err)
		}
	}
	return nil
}

// readRefs reads stored or configured refs, dropping what no source takes and repeats.
func (p *plugin) readRefs(list []string) []Ref {
	var out []Ref
	for _, s := range list {
		if r, ok := p.srcs.parse(s); ok && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func (p *plugin) watchlist() []Ref {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return slices.Clone(p.watch)
}

func (p *plugin) watching(r Ref) bool { return slices.Contains(p.watchlist(), r) }

// setWatch changes the watchlist with fn and stores it; fn says what to tell the user, or an error.
func (p *plugin) setWatch(fn func([]Ref) ([]Ref, string, error)) (string, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	next, msg, err := fn(slices.Clone(p.watch))
	if err != nil {
		return "", err
	}
	if p.watchPath != "" {
		refs := make([]string, len(next))
		for i, r := range next {
			refs[i] = r.String()
		}
		raw, _ := json.Marshal(refs)
		tmp := p.watchPath + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return "", fmt.Errorf("自选存不下：%w", err)
		}
		if err := os.Rename(tmp, p.watchPath); err != nil {
			return "", fmt.Errorf("自选存不下：%w", err)
		}
	}
	p.watch = next
	return msg, nil
}

func (p *plugin) addWatch(r Ref) (string, error) {
	return p.setWatch(func(w []Ref) ([]Ref, string, error) {
		switch {
		case slices.Contains(w, r):
			return w, r.Symbol + " 已经在自选里", nil
		case len(w) >= watchMost:
			return nil, "", fmt.Errorf("自选最多 %d 个", watchMost)
		}
		return append(w, r), "已加入 " + r.Symbol, nil
	})
}

func (p *plugin) removeWatch(r Ref) (string, error) {
	return p.setWatch(func(w []Ref) ([]Ref, string, error) {
		i := slices.Index(w, r)
		if i < 0 {
			return w, "", nil
		}
		return slices.Delete(w, i, i+1), "已移出 " + r.Symbol, nil
	})
}

// moveWatch moves r one place up (by -1) or down (by 1).
// moveWatch moves r by one place among the watchlist's instruments of its source (the page lists each source as a
// group): it swaps with the nearest one of the same source before (by -1) or after (by 1).
func (p *plugin) moveWatch(r Ref, by int) error {
	_, err := p.setWatch(func(w []Ref) ([]Ref, string, error) {
		i := slices.Index(w, r)
		if i < 0 {
			return w, "", nil
		}
		for j := i + by; j >= 0 && j < len(w); j += by {
			if w[j].Source == r.Source {
				w[i], w[j] = w[j], w[i]
				break
			}
		}
		return w, "", nil
	})
	return err
}

// moveWatchTop moves r to the top of its source's group.
func (p *plugin) moveWatchTop(r Ref) error {
	_, err := p.setWatch(func(w []Ref) ([]Ref, string, error) {
		if !slices.Contains(w, r) {
			return w, "", nil
		}
		// the group's places stay its own: r takes the first, the group's others before it move down one of them
		var at []int
		for i, x := range w {
			if x.Source == r.Source {
				at = append(at, i)
			}
		}
		g := []Ref{r}
		for _, i := range at {
			if w[i] != r {
				g = append(g, w[i])
			}
		}
		for k, i := range at {
			w[i] = g[k]
		}
		return w, "", nil
	})
	return err
}

// group is the watchlist's instruments of one source, in order.
func group(w []Ref, source string) []Ref {
	var out []Ref
	for _, r := range w {
		if r.Source == source {
			out = append(out, r)
		}
	}
	return out
}

// note keeps how a read went, for the connector's status.
func (p *plugin) note(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case err == nil:
		p.lastOK, p.lastErr = time.Now().UTC(), nil
	case errors.Is(err, ErrNotFound), errors.Is(err, context.Canceled):
		// about one instrument, or the caller gave up: not the connector's state
	default:
		p.lastErr, p.lastErrAt = err, time.Now()
		p.logf("markets: %s", clip(oneLine(err.Error()), 200))
	}
}

func freshness(base time.Duration, refresh bool) time.Duration {
	if refresh {
		return 0
	}
	return base
}

// quotes reads the quotes of refs, asking each source once for its own; a source that fails fails the whole read
// (the page says why and offers a retry).
func (p *plugin) quotes(ctx context.Context, refs []Ref, refresh bool) (map[Ref]Quote, error) {
	bySource := map[string][]string{}
	for _, r := range refs {
		bySource[r.Source] = append(bySource[r.Source], r.Symbol)
	}
	out := map[Ref]Quote{}
	for _, src := range p.srcs {
		syms := bySource[src.ID()]
		if len(syms) == 0 {
			continue
		}
		qs, err := src.Quotes(ctx, syms, freshness(quotesFor, refresh))
		p.note(err)
		if err != nil {
			return nil, err
		}
		for sym, q := range qs {
			out[Ref{src.ID(), sym}] = q
		}
	}
	return out, nil
}

func (p *plugin) quote(ctx context.Context, r Ref) (Quote, error) {
	src := p.srcs.get(r.Source)
	if src == nil {
		return Quote{}, ErrNotFound
	}
	q, err := src.Quote(ctx, r.Symbol, quotesFor)
	p.note(err)
	return q, err
}

func (p *plugin) candles(ctx context.Context, r Ref, bar string, n int) ([]Candle, error) {
	src := p.srcs.get(r.Source)
	if src == nil {
		return nil, ErrNotFound
	}
	c, err := src.Candles(ctx, r.Symbol, bar, n, candlesFor)
	p.note(err)
	return c, err
}

// hit is a search result with the ref it opens.
type hit struct {
	Ref   Ref
	Quote Quote
}

// search asks every source and interleaves their answers, each source's best first (sources rank in their own
// ways and currencies, so their lists are not merged by value). A source that fails is skipped while another
// answers; when all fail the first error is returned.
func (p *plugin) search(ctx context.Context, q string, n int, refresh bool) ([]hit, error) {
	var lists [][]hit
	var firstErr error
	for _, src := range p.srcs {
		qs, err := src.Search(ctx, q, n, freshness(quotesFor, refresh))
		p.note(err)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var l []hit
		for _, x := range qs {
			l = append(l, hit{Ref{src.ID(), x.Symbol}, x})
		}
		lists = append(lists, l)
	}
	if len(lists) == 0 && firstErr != nil {
		return nil, firstErr
	}
	var out []hit
	for i := 0; len(out) < n; i++ {
		more := false
		for _, l := range lists {
			if i < len(l) && len(out) < n {
				out, more = append(out, l[i]), true
			}
		}
		if !more {
			break
		}
	}
	return out, nil
}

// connectorStatus is connector.status's answer.
type connectorStatus struct {
	State    string     `json:"state"`
	Account  string     `json:"account,omitempty"`
	Error    string     `json:"error,omitempty"`
	LastSync *time.Time `json:"last_sync,omitempty"`
}

// cStatus answers from memory: ok until a read fails; the last failure stands until a read succeeds again.
func (p *plugin) cStatus(context.Context, *pluginkit.Hub, json.RawMessage) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := connectorStatus{State: "ok", Account: p.srcs.markets() + "公开行情，不用账号"}
	if !p.lastOK.IsZero() {
		at := p.lastOK
		st.LastSync = &at
	}
	if p.lastErr != nil && p.lastErrAt.After(p.lastOK) {
		st.Error = clip(oneLine(p.lastErr.Error()), 200)
		st.State = "error"
		if errors.Is(p.lastErr, ErrRateLimited) {
			st.State = "rate_limited"
		}
	}
	return st, nil
}

func (p *plugin) handlers() map[string]pluginkit.Handler {
	h := map[string]pluginkit.Handler{
		"connector.status":        p.cStatus,
		"connector.tools.list":    p.cToolsList,
		"connector.tools.call":    p.cToolsCall,
		"connector.inbox.collect": p.cInboxCollect,
		"ui.render":               p.uiRender,
		"ui.action":               p.uiAction,
	}
	for name, fn := range h {
		want := []string{"markets"}
		if strings.HasPrefix(name, "ui.") {
			want = []string{"watchlist", "card"} // the manifest's ui contributions
		}
		h[name] = func(ctx context.Context, hub *pluginkit.Hub, params json.RawMessage) (any, error) {
			if c := pluginkit.Contribution(params); c != "" && !slices.Contains(want, c) {
				return nil, pluginkit.Errorf(pluginkit.CodeNoContribution, "no contribution %s here", c)
			}
			return fn(ctx, hub, params)
		}
	}
	return h
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
