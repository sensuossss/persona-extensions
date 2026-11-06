package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// The page (ui contribution "watchlist", a nav_page): the watchlist with last prices and changes, a search across
// every source, and an instrument opened in the same page: its figures, its price over the chosen bar and its
// volume, and buttons to keep it, drop it or move it. Only Source's methods are used here, whichever market the
// instrument is in. The state it carries is the open instrument (a ref), its bar and the search.

type pageState struct {
	Inst string `json:"i,omitempty"` // a ref ("okx:BTC-USDT"); a bare symbol is the default source's
	Bar  string `json:"b,omitempty"`
	Q    string `json:"q,omitempty"` // the search: the list page shows what matches it instead of the watchlist
	Sort string `json:"s,omitempty"` // the watchlist's order: "" its own, "up" the biggest rise first, "down" the biggest fall
	Mkt  string `json:"m,omitempty"` // the watchlist shows only this source's group; "" every group
}

const (
	candlesShow  = 60 // bars in the charts (a chart takes 120 points a series)
	refreshEvery = 30 // seconds: the apps draw the page again while it is on screen
	// refreshIdle: the same when every quote on it is from a market not trading (a closed market's figures stay)
	refreshIdle = 300
	statsMost   = 6 // stats in a stat_grid
)

func (p *plugin) readState(raw string) (pageState, *Ref) {
	var st pageState
	_ = json.Unmarshal([]byte(raw), &st)
	var open *Ref
	if r, ok := p.srcs.parse(st.Inst); ok && st.Inst != "" {
		open, st.Inst = &r, r.String()
	} else {
		st.Inst = ""
	}
	if !barKnown(st.Bar) {
		st.Bar = ""
	}
	st.Q = cleanQuery(st.Q)
	if st.Sort != "up" && st.Sort != "down" {
		st.Sort = ""
	}
	if p.srcs.get(st.Mkt) == nil {
		st.Mkt = ""
	}
	return st, open
}

func (st pageState) encode() string {
	b, _ := json.Marshal(st)
	return string(b)
}

// draw is how the asking app draws: the time zone, whether it has UI version 2 (charts, stat grids, segmented) and 3
// (a search box).
type draw struct {
	loc    *time.Location
	v2, v3 bool
}

func (p *plugin) drawFor(r pluginkit.UIRequest) draw {
	d := draw{loc: p.loc, v2: r.Draws(2), v3: r.Draws(3)}
	if d.loc == nil { // the extension's own setting wins; else the user's, as the hub says it
		d.loc = r.Location()
	}
	return d
}

func (p *plugin) uiRender(ctx context.Context, _ *pluginkit.Hub, raw json.RawMessage) (any, error) {
	var r pluginkit.UIRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Contribution == "card" {
		return p.card(ctx)
	}
	st, open := p.readState(r.State)
	return p.page(ctx, p.drawFor(r), st, open, false), nil
}

func (p *plugin) uiAction(ctx context.Context, _ *pluginkit.Hub, raw json.RawMessage) (any, error) {
	var r pluginkit.UIRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Contribution == "card" { // it has no buttons: drawn again
		v, err := p.card(ctx)
		if err != nil {
			return nil, err
		}
		return pluginkit.ActionResult{View: &v}, nil
	}
	st, open := p.readState(r.State)
	refresh, toast := false, ""
	kind, arg, _ := strings.Cut(r.Action, ":") // the rest is a ref, itself "source:symbol"
	ref, refOK := p.srcs.parse(arg)
	switch kind {
	case "open":
		if refOK {
			open = &ref
		}
	case "back":
		open = nil
	case "search":
		q, ok := r.Values["q"] // the search box
		if !ok {
			q = r.Values["inst"] // an app before UI version 3: the field and a button
		}
		st.Q, open = cleanQuery(q), nil
	case "clear":
		st.Q = ""
	case "sort", "mkt":
		if arg == "" { // the segmented control: the choice is in the values
			arg = r.Values[kind]
		}
		if kind == "sort" {
			st.Sort = map[string]string{"up": "up", "down": "down"}[arg]
		} else if p.srcs.get(arg) != nil {
			st.Mkt = arg
		} else {
			st.Mkt = ""
		}
	case "bar":
		if arg == "" { // the segmented control: the choice is in the values
			arg = r.Values["bar"]
		}
		if barKnown(arg) {
			st.Bar = arg
		}
	case "look", "add":
		if arg == "" { // typed in the add field
			typed := strings.TrimSpace(r.Values["inst"])
			if ref, refOK = p.srcs.parse(typed); !refOK {
				toast = "「" + clip(typed, 30) + "」不像能查的代码：用上面的搜索，或者写成 BTC-USDT 这样"
				break
			}
		} else if !refOK {
			break
		}
		if kind == "look" {
			open = &ref
			break
		}
		if _, err := p.quote(ctx, ref); err != nil {
			toast = ref.Symbol + "：" + err.Error()
			break
		}
		if msg, err := p.addWatch(ref); err != nil {
			toast = err.Error()
		} else {
			toast = msg
			if st.Q != "" && open != nil && *open == ref { // added from a search's result: back to the search
				open = nil
			}
		}
	case "remove":
		if refOK {
			if msg, err := p.removeWatch(ref); err != nil {
				toast = err.Error()
			} else {
				toast = msg
			}
		}
	case "alert": // a new price alert, from the instrument page's fields
		if !refOK {
			break
		}
		a, err := p.newAlert(ctx, ref, r.Values["alert_kind"], r.Values["alert_value"])
		if err == nil {
			err = p.alerts.add(a)
		}
		if err != nil {
			toast = err.Error()
			break
		}
		q, _ := p.quote(ctx, ref)
		toast = "已设提醒：" + ref.Symbol + " " + a.what(q)
	case "unalert": // the rest is the alert's id
		if a, ok, err := p.alerts.remove(arg); err != nil {
			toast = err.Error()
		} else if ok {
			_, sym, _ := strings.Cut(a.Ref, ":")
			toast = "已删掉 " + sym + " 的提醒"
		}
	case "top":
		if refOK {
			if err := p.moveWatchTop(ref); err != nil {
				toast = err.Error()
			}
		}
	case "up", "down":
		if refOK {
			by := -1
			if kind == "down" {
				by = 1
			}
			if err := p.moveWatch(ref, by); err != nil {
				toast = err.Error()
			}
		}
	case "refresh":
		refresh, toast = true, "已刷新"
	}
	st.Inst = ""
	if open != nil {
		st.Inst = open.String()
	}
	v := p.page(ctx, p.drawFor(r), st, open, refresh)
	return pluginkit.ActionResult{View: &v, Toast: toast}, nil
}

// page is the watchlist (or a search) or the open instrument; what went wrong is drawn, with a way to try again.
func (p *plugin) page(ctx context.Context, d draw, st pageState, open *Ref, refresh bool) pluginkit.View {
	if open != nil {
		return p.instPage(ctx, d, st, *open)
	}
	return p.listPage(ctx, d, st, refresh)
}

func (p *plugin) listPage(ctx context.Context, d draw, st pageState, refresh bool) pluginkit.View {
	v := pluginkit.View{Title: "行情", State: st.encode(), RefreshAfter: refreshEvery}
	if d.v3 {
		var ex []string
		for _, src := range p.srcs {
			_, q := src.Examples()
			if !slices.Contains(ex, q) {
				ex = append(ex, q)
			}
		}
		v.Search = &pluginkit.Search{Placeholder: "搜代码或名称：" + strings.Join(ex, "、"), Value: st.Q, Action: pluginkit.UIAction{ID: "search"}}
	}
	if st.Q != "" {
		v.Sections = append(v.Sections, p.searchSections(ctx, st.Q, d.v3, refresh)...)
		if !d.v3 {
			v.Sections = append(v.Sections, p.addSection(false))
		}
		v.Sections = append(v.Sections, p.readAt(d))
		return v
	}
	watch := p.watchlist()
	marks := p.marks()
	// each source asked once, for its watchlist and its benchmark; one that fails says so in its group
	qs, errs := map[Ref]Quote{}, map[string]error{}
	for _, src := range p.srcs {
		refs := group(watch, src.ID())
		if len(refs) == 0 && !d.v2 {
			continue
		}
		for _, m := range group(marks, src.ID()) {
			if !slices.Contains(refs, m) {
				refs = append(refs, m)
			}
		}
		if len(refs) == 0 {
			continue
		}
		got, err := src.Quotes(ctx, symbols(refs), freshness(quotesFor, refresh))
		p.note(err)
		if err != nil {
			errs[src.ID()] = err
			continue
		}
		for sym, q := range got {
			qs[Ref{src.ID(), sym}] = q
		}
	}
	active := len(watch) == 0 || len(errs) > 0 // a quote of a market trading, nothing to show, or a retry: the usual refresh

	if d.v2 { // 大盘: each source's benchmark
		if stats := p.markStats(marks, qs); len(stats) > 1 { // one alone (OKX only) would repeat the watchlist
			v.Sections = append(v.Sections, pluginkit.Section{Title: "大盘", Items: []pluginkit.Item{{Type: "stat_grid", Stats: stats}}})
		}
	}
	if len(watch) == 0 {
		v.Sections = append(v.Sections, pluginkit.Section{Title: "自选 · 0",
			Items: []pluginkit.Item{{Type: "text", Text: "自选是空的：搜一个加进来", Style: "secondary"}}})
	}

	// the sources the watchlist has, for the filter
	var present []Source
	for _, src := range p.srcs {
		if len(group(watch, src.ID())) > 0 {
			present = append(present, src)
		}
	}
	if st.Mkt != "" && !slices.ContainsFunc(present, func(s Source) bool { return s.ID() == st.Mkt }) {
		st.Mkt = "" // nothing of it left
		v.State = st.encode()
	}
	if d.v2 && len(watch) > 1 {
		ctl := pluginkit.Section{Items: []pluginkit.Item{{Type: "segmented", Name: "sort", Value: or(st.Sort, "order"),
			Action: &pluginkit.UIAction{ID: "sort"}, Choices: []pluginkit.Choice{{Value: "order", Title: "自选顺序"},
				{Value: "up", Title: "涨幅"}, {Value: "down", Title: "跌幅"}}}}}
		if len(present) > 1 && len(present) < 5 { // a segmented control takes at most 5
			mkt := pluginkit.Item{Type: "segmented", Name: "mkt", Value: or(st.Mkt, "all"), Action: &pluginkit.UIAction{ID: "mkt"},
				Choices: []pluginkit.Choice{{Value: "all", Title: "全部"}}}
			for _, src := range present {
				mkt.Choices = append(mkt.Choices, pluginkit.Choice{Value: src.ID(), Title: src.Market()})
			}
			ctl.Items = append(ctl.Items, mkt)
		}
		v.Sections = append(v.Sections, ctl)
	}

	// one group a source, in the sources' order
	var groups []pluginkit.Section
	for _, src := range present {
		if st.Mkt != "" && src.ID() != st.Mkt {
			continue
		}
		refs := group(watch, src.ID())
		sec := pluginkit.Section{Title: fmt.Sprintf("%s · %d", src.Market(), len(refs))}
		if len(p.srcs) == 1 {
			sec.Title = fmt.Sprintf("自选 · %d", len(refs))
		}
		if err := errs[src.ID()]; err != nil {
			sec.Items = []pluginkit.Item{{Type: "text", Text: err.Error(), Style: "secondary"},
				{Type: "button", Title: "重试", Action: &pluginkit.UIAction{ID: "refresh"}}}
			groups = append(groups, sec)
			continue
		}
		if st.Sort != "" {
			sortByChange(refs, qs, st.Sort == "down")
		}
		periods := map[string]bool{}
		for _, r := range refs {
			q, ok := qs[r]
			if ok {
				periods[q.Period] = true
				active = active || !q.Idle
			}
			sec.Items = append(sec.Items, p.quoteRow(r, q, ok, false))
		}
		switch {
		case periods["24h"]:
			sec.Footer = "24h 涨跌按 24 小时前的价格算"
		case periods["当日"]:
			sec.Footer = "涨跌按上个交易日的收盘价算，时间是交易所当地的"
		}
		groups = append(groups, sec)
	}
	if n := len(groups); n > 0 {
		groups[n-1].Footer = strings.TrimPrefix(groups[n-1].Footer+"。点一行看 K 线", "。")
	}
	v.Sections = append(v.Sections, groups...)
	if sec, ok := p.alertsSection(qs); ok {
		v.Sections = append(v.Sections, sec)
	}
	if !d.v3 { // with a search box, a code is added through the search
		v.Sections = append(v.Sections, p.addSection(false))
	}
	v.Sections = append(v.Sections, p.readAt(d))
	if !active {
		v.RefreshAfter = refreshIdle
	}
	return v
}

func or(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// sortByChange orders refs by their change, the biggest rise first (or the biggest fall, down); those without one
// go last, in their order.
func sortByChange(refs []Ref, qs map[Ref]Quote, down bool) {
	sort.SliceStable(refs, func(i, j int) bool {
		ci, oki := qs[refs[i]].Change()
		cj, okj := qs[refs[j]].Change()
		switch {
		case oki != okj:
			return oki
		case down:
			return ci < cj
		}
		return ci > cj
	})
}

func symbols(refs []Ref) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

// quoteRow is an instrument as a row: its name when it has one, else its symbol; under it the symbol (when the name
// is the title), the change in price and the turnover, in a search its kind and whether it is in the watchlist, and
// whether the market is trading; the last price and the change.
func (p *plugin) quoteRow(r Ref, q Quote, ok, search bool) pluginkit.Item {
	it := pluginkit.Item{Type: "row", Title: r.Symbol, Action: &pluginkit.UIAction{ID: "open:" + r.String()}}
	if !ok {
		it.Subtitle = "现在查不到这个代码的行情"
		it.Badge = &pluginkit.Badge{Text: "无行情", Tone: "caution"}
		return it
	}
	var parts []string
	if q.Name != "" {
		it.Title = q.Name
		parts = append(parts, r.Symbol)
	}
	if search && q.Kind != "" {
		parts = append(parts, q.Kind)
	}
	if !search && q.Base != 0 && q.Last != 0 {
		d := q.Last - q.Base
		sign := "+"
		if d < 0 {
			sign, d = "-", -d
		}
		if d == 0 {
			sign = ""
		}
		parts = append(parts, sign+q.PriceLike(d, q.Last))
	}
	if q.Turnover > 0 {
		parts = append(parts, "成交 "+fmtCN(q.Turnover)+" "+q.Currency)
	}
	if search && p.watching(r) {
		parts = append(parts, "已在自选")
	}
	if q.Idle && !q.At.IsZero() {
		parts = append(parts, "休市 · 截至 "+q.At.Format("1/2 15:04"))
	}
	it.Subtitle = strings.Join(parts, " · ")
	it.Value = q.Price(q.Last)
	if c, ok := q.Change(); ok {
		it.Badge = &pluginkit.Badge{Text: fmtChange(c), Tone: p.tone(c)}
	}
	return it
}

// searchSections list what matches q in every source, a group a source, the group with the best match first (then
// in the sources' order; each its best first): a row each, opening the instrument (whose page adds it to the
// watchlist). With a search box (v3) a query that is itself a code (cn:600519, BTC-USDT-SWAP) comes first as its
// own row, since the search box is then the only way to add one.
func (p *plugin) searchSections(ctx context.Context, q string, v3, refresh bool) []pluginkit.Section {
	title := "搜索「" + q + "」"
	back := pluginkit.Item{Type: "button", Title: "返回自选", Action: &pluginkit.UIAction{ID: "clear"}}
	hits, err := p.search(ctx, q, searchMost, refresh)
	var exact *hit
	if v3 {
		if r, ok := p.srcs.parse(q); ok && !slices.ContainsFunc(hits, func(h hit) bool { return h.Ref == r }) {
			if x, qerr := p.quote(ctx, r); qerr == nil {
				exact = &hit{r, x}
			}
		}
	}
	if err != nil && exact == nil {
		return []pluginkit.Section{{Title: title, Items: []pluginkit.Item{{Type: "text", Text: err.Error(), Style: "secondary"},
			{Type: "button", Title: "重试", Action: &pluginkit.UIAction{ID: "refresh"}}, back}}}
	}
	if len(hits) == 0 && exact == nil {
		return []pluginkit.Section{{Title: title, Items: []pluginkit.Item{
			{Type: "text", Text: "没有和「" + q + "」对得上的：" + p.srcs.markets(), Style: "secondary"}, back}}}
	}
	var out []pluginkit.Section
	if exact != nil {
		out = append(out, pluginkit.Section{Title: "代码 " + exact.Ref.String(), Items: []pluginkit.Item{p.quoteRow(exact.Ref, exact.Quote, true, true)}})
	}
	type grp struct {
		src   Source
		items []pluginkit.Item
		best  int
	}
	var gs []grp
	for _, src := range p.srcs {
		g := grp{src: src, best: 9}
		for _, h := range hits {
			if h.Ref.Source == src.ID() {
				g.items = append(g.items, p.quoteRow(h.Ref, h.Quote, true, true))
				g.best = min(g.best, matchRank(q, src, h))
			}
		}
		if len(g.items) > 0 {
			gs = append(gs, g)
		}
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].best < gs[j].best })
	for _, g := range gs {
		sec := pluginkit.Section{Title: fmt.Sprintf("%s · %d", g.src.Market(), len(g.items)), Items: g.items}
		if len(out) == 0 {
			sec.Title = title + " · " + sec.Title
		}
		out = append(out, sec)
	}
	last := &out[len(out)-1]
	last.Footer = "点一行看 K 线、加入自选"
	if len(hits) == searchMost {
		last.Footer = fmt.Sprintf("只列最前面的 %d 个，写得再具体些能找到别的。", searchMost) + last.Footer
	}
	last.Items = append(last.Items, back)
	return out
}

// matchRank is how well a hit matches what was searched, for ordering the groups: 0 its symbol as the source reads
// the query, or its name exactly; 1 a name starting with it; 2 the rest; 3 a source without names (OKX) for a query
// in Chinese, which only matches by accident.
func matchRank(q string, src Source, h hit) int {
	if sym, ok := src.Normalize(q); ok && sym == h.Ref.Symbol {
		return 0
	}
	name := strings.ToLower(h.Quote.Name)
	lq := strings.ToLower(strings.TrimSpace(q))
	switch {
	case name != "" && name == lq:
		return 0
	case name != "" && strings.HasPrefix(name, lq):
		return 1
	case h.Quote.Name == "" && hasHan(q):
		return 3
	}
	return 2
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// addSection is the way to add by typing: with a search box (UI version 3) a code typed in full; before it, the
// same field also searches.
func (p *plugin) addSection(v3 bool) pluginkit.Section {
	var ex []string
	for i, src := range p.srcs {
		typed, _ := src.Examples()
		if i > 0 { // the default source's symbols need no prefix
			typed = src.ID() + ":" + typed
		}
		ex = append(ex, typed)
	}
	hint := "直接写代码，如 " + strings.Join(ex, "、")
	if len(p.srcs) > 1 {
		hint += "；不是 " + p.srcs[0].Name() + " 的，代码前写来源"
	}
	sec := pluginkit.Section{Title: "添加",
		Footer: hint,
		Items: []pluginkit.Item{
			{Type: "field", Name: "inst", Label: "代码", Kind: "text", Placeholder: "BTC-USDT"},
			{Type: "button", Title: "加入自选", Action: &pluginkit.UIAction{ID: "add", Submit: true}}}}
	if v3 {
		sec.Footer = "不确定代码就用上面的搜索。" + sec.Footer
	} else {
		sec.Items = append(sec.Items, pluginkit.Item{Type: "button", Title: "搜索", Action: &pluginkit.UIAction{ID: "search", Submit: true}})
	}
	return sec
}

func (p *plugin) readAt(d draw) pluginkit.Section {
	return pluginkit.Section{Items: []pluginkit.Item{
		{Type: "text", Text: p.sourceNames() + " 公开行情 · 读于 " + time.Now().In(d.loc).Format("15:04:05"), Style: "caption"},
		{Type: "button", Title: "刷新", Action: &pluginkit.UIAction{ID: "refresh"}}}}
}

func (p *plugin) sourceNames() string {
	var names []string
	for _, s := range p.srcs {
		names = append(names, s.Name())
	}
	return strings.Join(names, "、")
}

// cleanQuery is a search as kept: trimmed, at most 30 characters.
func cleanQuery(q string) string {
	return clip(strings.TrimSpace(q), 30)
}

func (p *plugin) instPage(ctx context.Context, d draw, st pageState, r Ref) pluginkit.View {
	back := pluginkit.Item{Type: "button", Title: "返回自选", Action: &pluginkit.UIAction{ID: "back"}}
	if st.Q != "" {
		back.Title = "返回搜索"
	}
	src := p.srcs.get(r.Source)
	bars := src.Bars() // the K line, over a bar this source has
	if !slices.Contains(bars, st.Bar) {
		st.Bar = p.bar(src)
	}
	bar := st.Bar
	v := pluginkit.View{Title: r.Symbol, State: st.encode(), RefreshAfter: refreshEvery}
	q, err := p.quote(ctx, r)
	if err != nil {
		items := []pluginkit.Item{{Type: "text", Text: err.Error(), Style: "secondary"}}
		if p.watching(r) {
			items = append(items, pluginkit.Item{Type: "button", Title: "移出自选", Role: "destructive",
				Action: &pluginkit.UIAction{ID: "remove:" + r.String()}})
		}
		v.Sections = []pluginkit.Section{{Items: append(items, back)}}
		return v
	}
	if q.Name != "" {
		v.Title = r.Symbol + " " + q.Name
	}

	// the period
	last := pluginkit.Stat{Label: "最新价", Value: q.Price(q.Last)}
	if c, ok := q.Change(); ok {
		last.Detail, last.Tone = q.Period+" "+fmtChange(c), p.tone(c)
	}
	if q.Idle && !q.At.IsZero() {
		last.Label = "休市 · 截至 " + q.At.Format("1/2 15:04")
		v.RefreshAfter = refreshIdle
	}
	figures := []pluginkit.Stat{last, {Label: q.Period + " 最高", Value: q.Price(q.High)}, {Label: q.Period + " 最低", Value: q.Price(q.Low)},
		{Label: q.Period + " 成交额", Value: fmtCompactFloat(q.Turnover) + " " + q.Currency}}
	var facts []pluginkit.Stat
	for _, f := range q.Facts {
		facts = append(facts, pluginkit.Stat{Label: f.Label, Value: f.Value})
	}
	if d.v2 {
		sec := pluginkit.Section{Items: []pluginkit.Item{{Type: "stat_grid", Stats: figures}}}
		for i := 0; i < len(facts); i += statsMost { // a grid takes at most 6
			sec.Items = append(sec.Items, pluginkit.Item{Type: "stat_grid", Stats: facts[i:min(i+statsMost, len(facts))]})
		}
		v.Sections = append(v.Sections, sec)
	} else {
		figures = append(figures, facts...)
		var items []pluginkit.Item
		for _, s := range figures {
			items = append(items, pluginkit.Item{Type: "metric", Label: s.Label, Value: s.Value, Detail: s.Detail, Tone: s.Tone})
		}
		v.Sections = append(v.Sections, pluginkit.Section{Items: items})
	}

	kline := pluginkit.Section{Title: "K 线 · " + barTitle(bar)}
	if d.v2 && len(bars) >= 2 && len(bars) <= 5 {
		seg := pluginkit.Item{Type: "segmented", Name: "bar", Value: bar, Action: &pluginkit.UIAction{ID: "bar"}}
		for _, b := range allBars {
			if slices.Contains(bars, b.id) {
				seg.Choices = append(seg.Choices, pluginkit.Choice{Value: b.id, Title: b.short})
			}
		}
		kline.Items = append(kline.Items, seg)
	} else if len(bars) >= 2 {
		for _, b := range allBars {
			if !slices.Contains(bars, b.id) {
				continue
			}
			it := pluginkit.Item{Type: "row", Title: b.title, Action: &pluginkit.UIAction{ID: "bar:" + b.id}}
			if b.id == bar {
				it.Badge = &pluginkit.Badge{Text: "正在看", Tone: "positive"}
			}
			kline.Items = append(kline.Items, it)
		}
	}
	cs, err := p.candles(ctx, r, bar, candlesShow)
	switch {
	case err != nil:
		kline.Items = append(kline.Items, pluginkit.Item{Type: "text", Text: err.Error(), Style: "secondary"})
	case len(cs) == 0:
		text := src.Name() + " 没给这个周期的 K 线"
		if bar == "T" {
			text = "还没有分时：今天还没开盘"
		}
		kline.Items = append(kline.Items, pluginkit.Item{Type: "text", Text: text, Style: "secondary"})
	default:
		kline.Items = append(kline.Items, klineItems(cs, bar, d.loc, d.v2, q, p.tone)...)
	}
	v.Sections = append(v.Sections, kline, p.alertSection(r, q))

	// what to do with it
	acts := pluginkit.Section{}
	watch := group(p.watchlist(), r.Source) // moved within its source's group
	if i := slices.Index(watch, r); i >= 0 {
		if i > 1 {
			acts.Items = append(acts.Items, pluginkit.Item{Type: "button", Title: "置顶", Action: &pluginkit.UIAction{ID: "top:" + r.String()}})
		}
		if i > 0 {
			acts.Items = append(acts.Items, pluginkit.Item{Type: "button", Title: "上移", Action: &pluginkit.UIAction{ID: "up:" + r.String()}})
		}
		if i < len(watch)-1 {
			acts.Items = append(acts.Items, pluginkit.Item{Type: "button", Title: "下移", Action: &pluginkit.UIAction{ID: "down:" + r.String()}})
		}
		acts.Items = append(acts.Items, pluginkit.Item{Type: "button", Title: "移出自选", Role: "destructive",
			Action: &pluginkit.UIAction{ID: "remove:" + r.String(), Confirm: "把 " + r.Symbol + " 移出自选？"}})
	} else {
		acts.Items = append(acts.Items, pluginkit.Item{Type: "button", Title: "加入自选", Action: &pluginkit.UIAction{ID: "add:" + r.String()}})
	}
	if u := src.URL(r.Symbol); strings.HasPrefix(u, "https://") {
		site := src.Name()
		if s, ok := src.(interface{ Site() string }); ok { // the website is not named like the source
			site = s.Site()
		}
		acts.Items = append(acts.Items, pluginkit.Item{Type: "link", Title: "在 " + site + " 打开", URL: u})
	}
	acts.Items = append(acts.Items, back,
		pluginkit.Item{Type: "text", Text: src.Name() + " 公开行情 · 读于 " + time.Now().In(d.loc).Format("15:04:05"), Style: "caption"})
	v.Sections = append(v.Sections, acts)
	return v
}

// klineItems are the period's figures, its closing prices as a line and its volume as bars; an app before UI
// version 2 gets the figures only (the hub drops charts for it anyway).
// Prices are written with q's decimals and changes toned by tone.
func klineItems(cs []Candle, bar string, loc *time.Location, v2 bool, q Quote, tone func(float64) string) []pluginkit.Item {
	first, last := cs[0], cs[len(cs)-1]
	hi, lo := first.High, first.Low
	for _, c := range cs {
		hi, lo = math.Max(hi, c.High), math.Min(lo, c.Low)
	}
	span := pluginkit.Stat{Label: fmt.Sprintf("%d 根涨跌", len(cs)), Value: "—"}
	if bar == "T" {
		span.Label = "开盘以来"
	}
	if first.Open != 0 {
		ch := (last.Close - first.Open) / first.Open
		span.Value, span.Tone = fmtChange(ch), tone(ch)
	}
	stats := []pluginkit.Stat{span, {Label: "区间最高", Value: q.Price(hi)}, {Label: "区间最低", Value: q.Price(lo)}}
	if !v2 {
		var items []pluginkit.Item
		for _, s := range stats {
			items = append(items, pluginkit.Item{Type: "metric", Label: s.Label, Value: s.Value, Tone: s.Tone})
		}
		return items
	}
	price := pluginkit.ChartSeries{Name: "收盘价"}
	vol := pluginkit.ChartSeries{Name: "成交量"}
	for _, c := range cs {
		label := barLabel(c.At, bar, loc)
		price.Points = append(price.Points, pluginkit.ChartPoint{Label: label, Value: c.Close,
			// the apps take 40 characters here: the open is the previous close, so it is left out
			Display: clip(fmt.Sprintf("收 %s 高 %s 低 %s", q.Price(c.Close), q.Price(c.High), q.Price(c.Low)), 40)})
		vol.Points = append(vol.Points, pluginkit.ChartPoint{Label: label, Value: c.Vol, Display: fmtCompactFloat(c.Vol)})
	}
	return []pluginkit.Item{
		{Type: "stat_grid", Stats: stats},
		{Type: "chart", Kind: "line", Title: "收盘价", Series: []pluginkit.ChartSeries{price}},
		{Type: "chart", Kind: "bar", Title: "成交量", Series: []pluginkit.ChartSeries{vol}},
	}
}

func barLabel(at time.Time, bar string, loc *time.Location) string {
	at = at.In(loc)
	switch bar {
	case "1D", "1W":
		return at.Format("1/2")
	case "T":
		return at.Format("15:04")
	}
	return at.Format("1/2 15:04")
}

func fmtChange(c float64) string {
	return fmt.Sprintf("%+.2f%%", c*100)
}

// fmtPrice writes a price with as many decimals as its size needs: 76330.10, 3.5021, 0.00001234; "—" when unknown.
func fmtPrice(f float64) string {
	if f == 0 {
		return "—"
	}
	return fmtFloat(f)
}

func fmtFloat(f float64) string {
	a := math.Abs(f)
	var s string
	switch {
	case a >= 1000:
		s = strconv.FormatFloat(f, 'f', 2, 64)
	case a >= 1:
		s = strconv.FormatFloat(f, 'f', 4, 64)
	case a == 0:
		return "0"
	default:
		s = strconv.FormatFloat(f, 'g', 4, 64)
		if strings.Contains(s, "e") { // 1.234e-05 reads worse than 0.00001234
			s = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	if strings.Contains(s, ".") && a < 1000 {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// fmtCompactFloat writes a volume short: 1.18B, 15.2K.
func fmtCompactFloat(f float64) string {
	a := math.Abs(f)
	for _, u := range []struct {
		n float64
		s string
	}{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if a >= u.n {
			return strconv.FormatFloat(f/u.n, 'f', 2, 64) + u.s
		}
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// alertSection is an instrument's price alerts and the form for a new one.
func (p *plugin) alertSection(r Ref, q Quote) pluginkit.Section {
	sec := pluginkit.Section{Title: "价格提醒", Footer: "到了进收件箱，每条只提醒一次；每分钟查一次。点一条删掉"}
	for _, a := range p.alerts.of(r) {
		sec.Items = append(sec.Items, p.alertRow(a, q, "unalert:"+a.ID))
	}
	sec.Items = append(sec.Items,
		pluginkit.Item{Type: "field", Name: "alert_kind", Label: "提醒", Kind: "choice", Value: "above", Choices: alertKinds},
		pluginkit.Item{Type: "field", Name: "alert_value", Label: "价格，或涨跌幅的百分数", Kind: "number", Placeholder: q.Price(q.Last)},
		pluginkit.Item{Type: "button", Title: "设提醒", Action: &pluginkit.UIAction{ID: "alert:" + r.String(), Submit: true}})
	return sec
}

// alertRow is an alert as a row: what it waits for, and whether it went off. action is what a tap does.
func (p *plugin) alertRow(a Alert, q Quote, action string) pluginkit.Item {
	q.Decimals = max(q.Decimals, a.Decimals)
	it := pluginkit.Item{Type: "row", Title: a.what(q), Subtitle: "等着", Action: &pluginkit.UIAction{ID: action}}
	if strings.HasPrefix(action, "unalert:") {
		it.Action.Confirm = "删掉这条提醒？"
	}
	if a.Met != nil {
		it.Subtitle = "已提醒 · " + a.Met.In(p.zone()).Format("1/2 15:04") + " · " + q.Price(a.Price)
		it.Badge = &pluginkit.Badge{Text: "到了", Tone: "caution"}
	}
	return it
}

// alertsSection lists every alert under the watchlist, each opening its instrument (whose page deletes it); false
// when there is none.
func (p *plugin) alertsSection(qs map[Ref]Quote) (pluginkit.Section, bool) {
	list := p.alerts.all()
	if len(list) == 0 {
		return pluginkit.Section{}, false
	}
	sec := pluginkit.Section{Title: fmt.Sprintf("价格提醒 · %d", len(list)), Footer: "点一条看它的代码，在那里删掉"}
	for _, a := range list {
		r, ok := p.srcs.parse(a.Ref)
		if !ok {
			continue
		}
		it := p.alertRow(a, qs[r], "open:"+r.String())
		name := r.Symbol
		if a.Name != "" {
			name = a.Name
		}
		it.Title = name + " " + it.Title
		sec.Items = append(sec.Items, it)
	}
	return sec, true
}
