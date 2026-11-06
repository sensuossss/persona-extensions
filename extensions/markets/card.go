package main

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// The card on 总览 (ui contribution "card", an overview_card opening the page): the markets at a glance, a benchmark
// of each source, then the watchlist's biggest moves. Off in the settings (overview_card false), it answers
// unsupported, which the apps take as nothing to draw.

const (
	cardMovers  = 5  // rows of the watchlist's moves
	cardRefresh = 60 // seconds
)

// benchmarks are the markets' yardsticks, a source's only when it is there.
var benchmarks = []string{"okx:BTC-USDT", "cn:000001.SH", "hk:HSI", "us:.IXIC"}

// cardOn says whether the card is drawn: the setting, on unless set false.
func (p *plugin) cardOn() bool { return p.cfg.OverviewCard == nil || *p.cfg.OverviewCard }

// quotesEach reads refs' quotes, asking each source once; a source that fails is left out (the first error is
// returned with what the others gave).
func (p *plugin) quotesEach(ctx context.Context, refs []Ref) (map[Ref]Quote, error) {
	out := map[Ref]Quote{}
	var first error
	for _, src := range p.srcs {
		g := group(refs, src.ID())
		if len(g) == 0 {
			continue
		}
		qs, err := src.Quotes(ctx, symbols(g), quotesFor)
		p.note(err)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for sym, q := range qs {
			out[Ref{src.ID(), sym}] = q
		}
	}
	return out, first
}

// marks are the benchmarks of the sources there are.
func (p *plugin) marks() []Ref {
	var out []Ref
	for _, s := range benchmarks {
		if r, ok := p.srcs.parse(s); ok && strings.HasPrefix(s, r.Source+":") {
			out = append(out, r)
		}
	}
	return out
}

// markStats are the benchmarks as stats: a name, the last price and the change; those without a quote left out.
func (p *plugin) markStats(marks []Ref, qs map[Ref]Quote) []pluginkit.Stat {
	var stats []pluginkit.Stat
	for _, r := range marks {
		q, ok := qs[r]
		if !ok {
			continue
		}
		label := q.Name
		if label == "" {
			label, _, _ = strings.Cut(r.Symbol, "-") // BTC-USDT: BTC
		}
		st := pluginkit.Stat{Label: label, Value: q.Price(q.Last)}
		if c, ok := q.Change(); ok {
			st.Detail, st.Tone = fmtChange(c), p.tone(c)
		}
		stats = append(stats, st)
	}
	return stats
}

func (p *plugin) card(ctx context.Context) (pluginkit.View, error) {
	if !p.cardOn() {
		return pluginkit.View{}, pluginkit.Errorf(pluginkit.CodeUnsupported, "the card is off in the settings")
	}
	v := pluginkit.View{RefreshAfter: cardRefresh}
	marks := p.marks()
	watch := p.watchlist()
	qs, err := p.quotesEach(ctx, append(append([]Ref{}, marks...), watch...))
	active := false
	for _, r := range marks {
		if q, ok := qs[r]; ok {
			active = active || !q.Idle
		}
	}
	if stats := p.markStats(marks, qs); len(stats) > 0 {
		v.Sections = append(v.Sections, pluginkit.Section{Items: []pluginkit.Item{{Type: "stat_grid", Stats: stats}}})
	}

	// the watchlist's biggest moves either way
	type move struct {
		r Ref
		q Quote
		c float64
	}
	var moves []move
	for _, r := range watch {
		if q, ok := qs[r]; ok {
			active = active || !q.Idle
			if c, ok := q.Change(); ok {
				moves = append(moves, move{r, q, c})
			}
		}
	}
	sort.SliceStable(moves, func(i, j int) bool { return math.Abs(moves[i].c) > math.Abs(moves[j].c) })
	sec := pluginkit.Section{Title: "自选里动得最多的"}
	for _, m := range moves[:min(cardMovers, len(moves))] {
		// no action: a tap on the card opens the page
		it := pluginkit.Item{Type: "row", Title: m.r.Symbol, Value: m.q.Price(m.q.Last),
			Badge: &pluginkit.Badge{Text: fmtChange(m.c), Tone: p.tone(m.c)}}
		if m.q.Name != "" {
			it.Title, it.Subtitle = m.q.Name, m.r.Symbol
		}
		sec.Items = append(sec.Items, it)
	}
	switch {
	case len(sec.Items) > 0:
		v.Sections = append(v.Sections, sec)
	case len(watch) == 0:
		v.Sections = append(v.Sections, pluginkit.Section{Items: []pluginkit.Item{{Type: "text", Text: "自选是空的：点开搜一个加进来", Style: "secondary"}}})
	case err != nil:
		v.Sections = append(v.Sections, pluginkit.Section{Items: []pluginkit.Item{{Type: "text", Text: err.Error(), Style: "secondary"}}})
	}
	if !active && len(qs) > 0 {
		v.RefreshAfter = refreshIdle
	}
	return v, nil
}
