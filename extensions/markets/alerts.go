package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// Price alerts: the user sets one on an instrument's page (above or below a price, or a move over the period of at
// least some percent); the extension checks them every minute on its own and, once one is met, puts it in the inbox
// (connector capability inbox, kind price_alert) and wakes the hub. An alert goes off once: it then stays, met, until
// the user deletes it, and its inbox thing for a week. Kept in the data directory (alerts.json).

const (
	alertsMost  = 50
	alertEvery  = time.Minute        // how often the alerts are checked
	alertKeep   = 7 * 24 * time.Hour // a met alert's thing stays in the inbox this long
	alertKind   = "price_alert"
	alertsFresh = 30 * time.Second // quotes younger than this do for a check
)

// Alert is one price alert.
type Alert struct {
	ID   string `json:"id"`
	Ref  string `json:"ref"`  // "cn:600519.SH"
	Kind string `json:"kind"` // above, below, move
	// Value is the price (above, below) or the move in percent (move, either way).
	Value   float64   `json:"value"`
	Created time.Time `json:"created"`
	// Met is when it went off ("" while it waits), with the quote then.
	Met    *time.Time `json:"met,omitempty"`
	Price  float64    `json:"price,omitempty"`
	Change float64    `json:"change,omitempty"` // a fraction
	Name   string     `json:"name,omitempty"`   // the instrument's name then
	// Decimals are its prices' (Quote.Decimals), to write them the same way.
	Decimals int `json:"decimals,omitempty"`
}

var alertKinds = []pluginkit.Choice{{Value: "above", Title: "涨到"}, {Value: "below", Title: "跌到"}, {Value: "move", Title: "涨跌幅超"}}

// what says what the alert waits for: "涨到 1500.00", "涨跌幅超 3%".
func (a Alert) what(q Quote) string {
	switch a.Kind {
	case "above":
		return "涨到 " + q.Price(a.Value)
	case "below":
		return "跌到 " + q.Price(a.Value)
	}
	return "涨跌幅超 " + strconv.FormatFloat(a.Value, 'f', -1, 64) + "%"
}

// meets says whether q meets the alert.
func (a Alert) meets(q Quote) bool {
	if q.Last == 0 {
		return false
	}
	switch a.Kind {
	case "above":
		return q.Last >= a.Value
	case "below":
		return q.Last <= a.Value
	case "move":
		c, ok := q.Change()
		return ok && math.Abs(c)*100 >= a.Value
	}
	return false
}

// alerts is the extension's alerts with where they are kept.
type alerts struct {
	mu   sync.Mutex
	path string // "" before initialize or without a data directory: in memory only
	list []Alert
	// wake tells the hub the inbox has something new (the hub's connector.changed); nil before initialize.
	wake func()
}

func (as *alerts) load(dir string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.list, as.path = nil, ""
	if dir == "" {
		return
	}
	as.path = filepath.Join(dir, "alerts.json")
	raw, err := os.ReadFile(as.path)
	if err == nil {
		_ = json.Unmarshal(raw, &as.list)
	}
}

// saveLocked writes the list; as.mu held.
func (as *alerts) saveLocked() error {
	if as.path == "" {
		return nil
	}
	raw, _ := json.MarshalIndent(as.list, "", "  ")
	tmp := as.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("存不下提醒：%w", err)
	}
	return os.Rename(tmp, as.path)
}

func (as *alerts) all() []Alert {
	as.mu.Lock()
	defer as.mu.Unlock()
	return slices.Clone(as.list)
}

func (as *alerts) of(r Ref) []Alert {
	var out []Alert
	for _, a := range as.all() {
		if a.Ref == r.String() {
			out = append(out, a)
		}
	}
	return out
}

func (as *alerts) add(a Alert) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.list) >= alertsMost {
		return fmt.Errorf("提醒最多 %d 条：先删掉几条", alertsMost)
	}
	for _, x := range as.list {
		if x.Met == nil && x.Ref == a.Ref && x.Kind == a.Kind && x.Value == a.Value {
			return errors.New("已经有这条提醒了")
		}
	}
	as.list = append(as.list, a)
	return as.saveLocked()
}

func (as *alerts) remove(id string) (Alert, bool, error) {
	as.mu.Lock()
	defer as.mu.Unlock()
	i := slices.IndexFunc(as.list, func(a Alert) bool { return a.ID == id })
	if i < 0 {
		return Alert{}, false, nil
	}
	a := as.list[i]
	as.list = slices.Delete(as.list, i, i+1)
	return a, true, as.saveLocked()
}

func newAlertID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newAlert reads an alert from the page's fields; it refuses one already met, which would go off at once.
func (p *plugin) newAlert(ctx context.Context, r Ref, kind, value string) (Alert, error) {
	if !slices.ContainsFunc(alertKinds, func(c pluginkit.Choice) bool { return c.Value == kind }) {
		return Alert{}, errors.New("选一种提醒：涨到、跌到或涨跌幅超")
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "%"), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return Alert{}, errors.New("写一个大于 0 的数")
	}
	if kind == "move" && v > 100 {
		return Alert{}, errors.New("涨跌幅写百分数，如 3 是 3%")
	}
	q, err := p.quote(ctx, r)
	if err != nil {
		return Alert{}, err
	}
	a := Alert{ID: newAlertID(), Ref: r.String(), Kind: kind, Value: v, Created: now().UTC(), Name: q.Name, Decimals: q.Decimals}
	if a.meets(q) {
		return Alert{}, fmt.Errorf("现在已经是 %s（%s），不用等：换一个数", q.Price(q.Last), a.what(q))
	}
	return a, nil
}

// checkAlerts reads the quotes of the alerts waiting (each source once) and marks those met; it wakes the hub when
// one went off. Run every alertEvery by the extension's own loop.
func (p *plugin) checkAlerts(ctx context.Context, fresh time.Duration) {
	var refs []Ref
	for _, a := range p.alerts.all() {
		if a.Met != nil {
			continue
		}
		if r, ok := p.srcs.parse(a.Ref); ok && !slices.Contains(refs, r) {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		return
	}
	qs := map[Ref]Quote{}
	for _, src := range p.srcs {
		g := group(refs, src.ID())
		if len(g) == 0 {
			continue
		}
		got, err := src.Quotes(ctx, symbols(g), fresh)
		p.note(err)
		if err != nil {
			continue // the next round tries again
		}
		for sym, q := range got {
			qs[Ref{src.ID(), sym}] = q
		}
	}
	met := false
	as := &p.alerts
	as.mu.Lock()
	for i := range as.list {
		a := &as.list[i]
		r, ok := p.srcs.parse(a.Ref)
		q, has := qs[r]
		if a.Met != nil || !ok || !has || !a.meets(q) {
			continue
		}
		t := now().UTC()
		a.Met, a.Price, a.Name = &t, q.Last, q.Name
		a.Change, _ = q.Change()
		met = true
	}
	if met {
		if err := as.saveLocked(); err != nil {
			p.logf("markets: %v", err)
		}
	}
	wake := as.wake
	as.mu.Unlock()
	if met && wake != nil {
		wake()
	}
}

// runAlerts checks the alerts every alertEvery until ctx ends.
func (p *plugin) runAlerts(ctx context.Context) {
	t := time.NewTicker(alertEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.checkAlerts(ctx, alertsFresh)
		}
	}
}

// alertThing is a met alert as an inbox thing: one thread an alert, needing the user (they asked for it).
func (p *plugin) alertThing(a Alert) pluginkit.InboxThing[pluginkit.ThingExtra] {
	r, _ := p.srcs.parse(a.Ref)
	q := Quote{Last: a.Price, Decimals: a.Decimals}
	name := r.Symbol
	if a.Name != "" {
		name = a.Name + " " + r.Symbol
	}
	t := pluginkit.InboxThing[pluginkit.ThingExtra]{
		Thread: "alert:" + a.ID, Kind: alertKind, Label: "行情提醒",
		Title:   name + " " + a.what(q),
		Preview: fmt.Sprintf("%s 现价 %s，%s；提醒设于 %s", name, q.Price(a.Price), fmtChange(a.Change), a.Created.In(p.zone()).Format("1/2 15:04")),
		Sender:  "行情",
		At:      *a.Met, Last: a.Met.UTC().Format(time.RFC3339),
		Tier: pluginkit.TierAction, Why: "用户自己设的价格提醒",
		Tags:  []pluginkit.InboxTag{{Key: "symbol", Value: a.Ref, Name: name}},
		Extra: &pluginkit.ThingExtra{RuleTier: pluginkit.TierAction},
	}
	if src := p.srcs.get(r.Source); src != nil {
		if u := src.URL(r.Symbol); strings.HasPrefix(u, "https://") {
			t.URL = u
		}
	}
	return t
}

func (p *plugin) zone() *time.Location {
	if p.loc != nil {
		return p.loc
	}
	return time.Local
}

// cInboxCollect answers from memory: the alerts met in the last week.
func (p *plugin) cInboxCollect(context.Context, *pluginkit.Hub, json.RawMessage) (any, error) {
	things := []pluginkit.InboxThing[pluginkit.ThingExtra]{}
	for _, a := range p.alerts.all() {
		if a.Met != nil && now().Sub(*a.Met) < alertKeep {
			things = append(things, p.alertThing(a))
		}
	}
	pluginkit.StampRules(things, p.rules)
	return map[string]any{"things": things}, nil
}
