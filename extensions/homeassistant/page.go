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

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page (ui contribution "home", a nav_page): the areas with what is on in each; an area's devices (or sensors,
// or everything) with their state; a search across every entity; and an entity opened with its controls,
// attributes and recent history. A tap here is the user's own: it calls the service at once (locks and alarms ask
// first), unlike the assistant's actions, which the hub's gate confirms.

type pageState struct {
	Area    string `json:"a,omitempty"` // area id
	Q       string `json:"q,omitempty"`
	Entity  string `json:"e,omitempty"`
	Kind    string `json:"k,omitempty"` // devices (default) | sensors | all
	Offline bool   `json:"o,omitempty"` // show unavailable entities too
}

const (
	refreshList   = 30
	refreshEntity = 15
	rowsMost      = 250 // a view takes 300 items
	searchMost    = 100
)

var kinds = []wire.Choice{{Value: "devices", Title: "设备"}, {Value: "sensors", Title: "传感器"}, {Value: "all", Title: "全部"}}

func (st *pageState) clean() {
	if !slices.ContainsFunc(kinds, func(c wire.Choice) bool { return c.Value == st.Kind }) {
		st.Kind = "devices"
	}
	st.Q = clip(strings.TrimSpace(st.Q), 40)
	if !entityRE.MatchString(st.Entity) {
		st.Entity = ""
	}
}

func (a *app) render(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	var st pageState
	_ = r.DecodeState(&st)
	st.clean()
	return a.page(ctx, r, st, statesFor), nil
}

func (a *app) action(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	var st pageState
	_ = r.DecodeState(&st)
	st.clean()
	fresh, toast := statesFor, ""
	kind, arg, _ := strings.Cut(r.Action, ":")
	switch kind {
	case "area":
		st.Area, st.Entity, st.Q, st.Offline = arg, "", "", false
	case "filter": // the area filter next to the search box
		st.Area, st.Entity = r.Values["area"], ""
	case "search":
		q, ok := r.Values["q"]
		if !ok {
			q = r.Values["query"] // an app before UI version 3: a field and a button
		}
		st.Q, st.Entity = clip(strings.TrimSpace(q), 40), ""
	case "clear":
		st.Q = ""
	case "kind":
		if arg == "" {
			arg = r.Values["kind"]
		}
		st.Kind = arg
		st.clean()
	case "offline":
		st.Offline = !st.Offline
	case "open":
		if entityRE.MatchString(arg) {
			st.Entity = arg
		}
	case "back":
		if st.Entity != "" {
			st.Entity = ""
		} else {
			st.Area = ""
		}
	case "home":
		st = pageState{Kind: st.Kind}
	case "refresh":
		fresh, toast = 0, "已刷新"
		if h, _, _ := a.client(); h != nil {
			h.tryAgain()
		}
	case "svc", "opt", "num", "hvac":
		msg, err := a.tap(ctx, st.Entity, kind, arg, r.Values)
		if err != nil {
			toast = err.Error()
		} else {
			toast = msg
		}
		fresh = 0
	}
	return &wire.ActionResult{View: a.page(ctx, r, st, fresh), Toast: toast}, nil
}

// tap runs a control of the open entity: svc (a button: service|data), opt (a select's option), num (a number's
// value), hvac (a climate's mode).
func (a *app) tap(ctx context.Context, eid, kind, arg string, values map[string]string) (string, error) {
	if eid == "" {
		return "", fmt.Errorf("先打开一个设备")
	}
	d, _, _ := strings.Cut(eid, ".")
	if err := a.canWrite(d, slices.Contains(secureDomains, d)); err != nil {
		return "", err
	}
	var service string
	data := map[string]any{}
	switch kind {
	case "svc":
		svc, raw, _ := strings.Cut(arg, "|")
		if !serviceRE.MatchString(svc) {
			return "", fmt.Errorf("不认识的操作")
		}
		service = svc
		if raw != "" && json.Unmarshal([]byte(raw), &data) != nil {
			return "", fmt.Errorf("不认识的操作")
		}
	case "opt":
		service, data["option"] = "select_option", values["option"]
		if values["option"] == "" {
			return "", fmt.Errorf("先选一个")
		}
	case "num":
		v, err := strconv.ParseFloat(strings.TrimSpace(values["value"]), 64)
		if err != nil {
			return "", fmt.Errorf("填一个数")
		}
		service, data["value"] = "set_value", v
	case "hvac":
		mode := arg
		if mode == "" {
			mode = values["hvac"]
		}
		if !serviceRE.MatchString(mode) {
			return "", fmt.Errorf("先选一个模式")
		}
		service, data["hvac_mode"] = "set_hvac_mode", mode
	}
	for _, k := range []string{"entity_id", "device_id", "area_id"} {
		delete(data, k)
	}
	text, err := a.run(ctx, call{Domain: d, Service: service, Entities: []string{eid}, Data: data})
	if err != nil {
		return "", err
	}
	_, now, _ := strings.Cut(text, "现在：")
	return "好了：" + clip(now, 80), nil
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState, fresh time.Duration) *wire.View {
	v := &wire.View{Title: "家", RefreshAfter: refreshList}
	hm, err := a.home(ctx, fresh)
	if err != nil {
		v.Sections = []wire.Section{{Items: []wire.Component{
			{Type: "text", Text: err.Error(), Style: "secondary"},
			{Type: "button", Title: "重试", Action: &wire.Action{ID: "refresh"}}}}}
		v.State = ext.EncodeState(st)
		return v
	}
	if st.Area != "" && hm.areaByID(st.Area) == nil {
		st.Area = ""
	}
	if r.UIVersion >= 3 && st.Entity == "" {
		v.Search = &wire.Search{Placeholder: "搜设备、传感器：客厅灯、空调、温度", Value: st.Q, Action: wire.Action{ID: "search"}}
		if len(hm.Areas) >= 1 && len(hm.Areas) <= 19 {
			f := &wire.Filter{Name: "area", Title: "区域", Value: st.Area, Action: wire.Action{ID: "filter"},
				Choices: []wire.Choice{{Value: "", Title: "全部区域"}}}
			if hm.ByType {
				f.Title, f.Choices[0].Title = "类型", "全部类型"
			}
			for _, ar := range hm.Areas {
				f.Choices = append(f.Choices, wire.Choice{Value: ar.ID, Title: ar.Name})
			}
			v.Filter = f
		}
	}
	switch {
	case st.Entity != "":
		a.entityPage(ctx, r, hm, &st, v)
	case st.Q != "":
		a.searchPage(r, hm, st, v)
	case st.Area == "":
		overviewPage(hm, v)
	default:
		areaPage(r, hm, st, v)
	}
	v.State = ext.EncodeState(st)
	return v
}

func (hm *home) areaByID(id string) *Area {
	for i := range hm.Areas {
		if hm.Areas[i].ID == id {
			return &hm.Areas[i]
		}
	}
	return nil
}

func overviewPage(hm *home, v *wire.View) {
	var alarms []wire.Component
	for _, s := range hm.States {
		if stateTone(s) == "danger" && !hm.Hidden[s.EntityID] {
			alarms = append(alarms, entityRow(hm, s, true))
		}
	}
	if len(alarms) > 0 {
		v.Sections = append(v.Sections, wire.Section{Title: "报警", Items: alarms})
	}
	if hm.Note != "" {
		v.Sections = append(v.Sections, wire.Section{Items: []wire.Component{{Type: "text", Text: hm.Note, Style: "secondary"}}})
	}
	sec := wire.Section{Title: fmt.Sprintf("区域 · %d", len(hm.Areas))}
	if hm.ByType {
		sec.Title = fmt.Sprintf("类型 · %d", len(hm.Areas))
	}
	for _, ar := range hm.Areas {
		sum := summarizeArea(hm, ar)
		row := wire.Component{Type: "row", Key: "area:" + ar.ID, Title: ar.Name, Subtitle: sum.line(), Action: &wire.Action{ID: "area:" + ar.ID}}
		if sum.on > 0 {
			row.Badge = &wire.Badge{Text: fmt.Sprintf("%d 开着", sum.on), Tone: "positive"}
		}
		sec.Items = append(sec.Items, row)
	}
	if len(hm.Areas) == 0 {
		sec.Items = append(sec.Items, wire.Component{Type: "text", Text: "Home Assistant 里还没有区域：用上面的搜索找设备", Style: "secondary"})
	}
	sec.Footer = "点一个区域看里面的设备"
	if hm.ByType {
		sec.Footer = "点一个类型看这类设备"
	}
	v.Sections = append(v.Sections, sec, footer())
}

func footer() wire.Section {
	return wire.Section{Items: []wire.Component{
		{Type: "text", Text: "Home Assistant · 读于 " + time.Now().Format("15:04:05"), Style: "caption"},
		{Type: "button", Title: "刷新", Action: &wire.Action{ID: "refresh"}}}}
}

func inKind(kind string, s State) bool {
	switch kind {
	case "devices":
		return slices.Contains(deviceDomains, s.Domain())
	case "sensors":
		return slices.Contains(sensorDomains, s.Domain())
	}
	return true
}

func areaPage(r *ext.UIRequest, hm *home, st pageState, v *wire.View) {
	ar := hm.areaByID(st.Area)
	v.Title = ar.Name
	if r.UIVersion >= 2 {
		v.Sections = append(v.Sections, wire.Section{Items: []wire.Component{{Type: "segmented", Name: "kind", Value: st.Kind,
			Choices: kinds, Action: &wire.Action{ID: "kind"}}}})
	} else {
		var rows []wire.Component
		for _, k := range kinds {
			c := wire.Component{Type: "row", Title: k.Title, Action: &wire.Action{ID: "kind:" + k.Value}}
			if k.Value == st.Kind {
				c.Badge = &wire.Badge{Text: "正在看", Tone: "positive"}
			}
			rows = append(rows, c)
		}
		v.Sections = append(v.Sections, wire.Section{Items: rows})
	}
	var shown []State
	offline := 0
	for _, e := range ar.Entities {
		s, ok := hm.ByID[e]
		if !ok || !inKind(st.Kind, s) {
			continue
		}
		if s.Unavailable() && !st.Offline {
			offline++
			continue
		}
		shown = append(shown, s)
	}
	sort.SliceStable(shown, func(i, j int) bool { // by domain in deviceDomains' order, then name
		di, dj := domainRank(shown[i].Domain()), domainRank(shown[j].Domain())
		if di != dj {
			return di < dj
		}
		return shown[i].Name() < shown[j].Name()
	})
	sec := wire.Section{Title: fmt.Sprintf("%s · %d", kindTitle(st.Kind), len(shown))}
	for i, s := range shown {
		if i == rowsMost {
			sec.Footer = fmt.Sprintf("只列前 %d 个，用搜索找别的", rowsMost)
			break
		}
		sec.Items = append(sec.Items, entityRow(hm, s, false))
	}
	if len(shown) == 0 {
		sec.Items = append(sec.Items, wire.Component{Type: "text", Text: "这里没有" + kindTitle(st.Kind), Style: "secondary"})
	}
	switch {
	case offline > 0:
		sec.Items = append(sec.Items, wire.Component{Type: "row", Title: fmt.Sprintf("还有 %d 个离线的", offline), Subtitle: "点一下也列出来", Action: &wire.Action{ID: "offline"}})
	case st.Offline:
		sec.Items = append(sec.Items, wire.Component{Type: "row", Title: "不列离线的", Action: &wire.Action{ID: "offline"}})
	}
	v.Sections = append(v.Sections, sec, wire.Section{Items: []wire.Component{{Type: "button", Title: "返回区域", Action: &wire.Action{ID: "home"}}}}, footer())
}

func kindTitle(k string) string {
	for _, c := range kinds {
		if c.Value == k {
			return c.Title
		}
	}
	return k
}

func domainRank(d string) int {
	if i := slices.Index(deviceDomains, d); i >= 0 {
		return i
	}
	if i := slices.Index(sensorDomains, d); i >= 0 {
		return len(deviceDomains) + i
	}
	return 1000
}

// entityRow is an entity in a list: its name, what it is and the detail, its state (a badge for devices, the value
// for the rest).
func entityRow(hm *home, s State, withArea bool) wire.Component {
	sub := []string{domainName(s.Domain())}
	if d := detail(s); d != "" {
		sub = append(sub, d)
	}
	if withArea {
		if n := hm.areaName(s.EntityID); n != "" {
			sub = append(sub, n)
		}
	}
	row := wire.Component{Type: "row", Key: s.EntityID, Title: clip(s.Name(), 80), Subtitle: strings.Join(sub, " · "),
		Action: &wire.Action{ID: "open:" + s.EntityID}}
	text := stateText(s)
	switch {
	case text == "":
	case slices.Contains(deviceDomains, s.Domain()) || s.Domain() == "binary_sensor" || s.Unavailable():
		row.Badge = &wire.Badge{Text: clip(text, 12), Tone: stateTone(s)}
	default:
		row.Value = clip(text, 24)
	}
	return row
}

func (a *app) searchPage(r *ext.UIRequest, hm *home, st pageState, v *wire.View) {
	found := hm.match(st.Q, hm.areaByID(st.Area), "", st.Offline)
	sec := wire.Section{Title: fmt.Sprintf("搜索「%s」· %d", st.Q, len(found))}
	for i, s := range found {
		if i == searchMost {
			sec.Footer = fmt.Sprintf("只列前 %d 个，写得具体些", searchMost)
			break
		}
		sec.Items = append(sec.Items, entityRow(hm, s, true))
	}
	if len(found) == 0 {
		sec.Items = append(sec.Items, wire.Component{Type: "text", Text: "没找到对得上的", Style: "secondary"})
	}
	if !st.Offline {
		sec.Items = append(sec.Items, wire.Component{Type: "row", Title: "也搜离线的", Action: &wire.Action{ID: "offline"}})
	}
	sec.Items = append(sec.Items, wire.Component{Type: "button", Title: "返回", Action: &wire.Action{ID: "clear"}})
	if r.UIVersion < 3 {
		v.Sections = append(v.Sections, searchField(st.Q))
	}
	v.Sections = append(v.Sections, sec, footer())
}

func searchField(q string) wire.Section {
	return wire.Section{Items: []wire.Component{
		{Type: "field", Name: "query", Label: "搜索", Kind: "text", Value: q, Placeholder: "客厅灯、空调、温度"},
		{Type: "button", Title: "搜索", Action: &wire.Action{ID: "search", Submit: true}}}}
}

func (a *app) entityPage(ctx context.Context, r *ext.UIRequest, hm *home, st *pageState, v *wire.View) {
	v.RefreshAfter = refreshEntity
	back := wire.Component{Type: "button", Title: "返回", Action: &wire.Action{ID: "back"}}
	s, ok := hm.ByID[st.Entity]
	if !ok || (hm.Hidden[st.Entity] && hm.AreaOf[st.Entity] == nil) {
		v.Sections = []wire.Section{{Items: []wire.Component{{Type: "text", Text: errNoEntity.Error(), Style: "secondary"}, back}}}
		return
	}
	v.Title = clip(s.Name(), 60)

	// the state
	head := wire.Section{}
	facts := []string{domainName(s.Domain())}
	if n := hm.areaName(s.EntityID); n != "" {
		facts = append(facts, n)
	}
	facts = append(facts, "变于 "+s.LastChanged.Local().Format("01-02 15:04"))
	head.Items = append(head.Items,
		wire.Component{Type: "metric", Label: "状态", Value: orDash(stateText(s)), Detail: detail(s), Tone: stateTone(s)},
		wire.Component{Type: "text", Text: strings.Join(facts, " · ") + "\n" + s.EntityID, Style: "caption"})
	v.Sections = append(v.Sections, head)

	// the controls
	if ctl := a.controlSection(r, s); len(ctl.Items) > 0 {
		v.Sections = append(v.Sections, ctl)
	}

	// the history
	if hist := a.historySection(ctx, s); len(hist.Items) > 0 {
		v.Sections = append(v.Sections, hist)
	}

	// the attributes
	var keys []string
	for k := range s.Attributes {
		if !slices.Contains(noisyAttrs, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		attrs := wire.Section{Title: "属性"}
		if r.UIVersion >= 3 {
			t := wire.Component{Type: "table", Columns: []wire.TableColumn{{Title: "名称"}, {Title: "值"}}}
			for _, k := range keys {
				if len(t.Rows) == 100 {
					break
				}
				t.Rows = append(t.Rows, []string{k, clip(attrText(s.Attributes[k]), 300)})
			}
			attrs.Items = append(attrs.Items, t)
		} else {
			var lines []string
			for _, k := range keys {
				lines = append(lines, k+"："+clip(attrText(s.Attributes[k]), 120))
			}
			attrs.Items = append(attrs.Items, wire.Component{Type: "text", Text: clip(strings.Join(lines, "\n"), 1900), Style: "secondary"})
		}
		v.Sections = append(v.Sections, attrs)
	}
	v.Sections = append(v.Sections, wire.Section{Items: []wire.Component{back}})
}

func attrText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return tidy(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// controlSection is the entity's controls, or why there are none.
func (a *app) controlSection(r *ext.UIRequest, s State) wire.Section {
	sec := wire.Section{Title: "操作"}
	d := s.Domain()
	if err := a.canWrite(d, slices.Contains(secureDomains, d)); err != nil {
		if len(controls(s)) > 0 || options(s) != nil {
			sec.Items = append(sec.Items, wire.Component{Type: "text", Text: err.Error() + "：在扩展设置里加上才能操作", Style: "secondary"})
		}
		return sec
	}
	if s.Unavailable() {
		sec.Items = append(sec.Items, wire.Component{Type: "text", Text: "设备离线，现在操作不了", Style: "secondary"})
		return sec
	}
	secure := slices.Contains(secureDomains, d)
	for _, c := range controls(s) {
		id := "svc:" + c.Service
		if len(c.Data) > 0 {
			b, _ := json.Marshal(c.Data)
			id += "|" + string(b)
		}
		act := &wire.Action{ID: id}
		if secure || c.Role == "destructive" {
			act.Confirm = c.Title + "「" + s.Name() + "」？"
		}
		sec.Items = append(sec.Items, wire.Component{Type: "button", Title: c.Title, Role: c.Role, Action: act})
	}
	if modes := hvacModes(s); len(modes) > 0 {
		var choices []wire.Choice
		for _, m := range modes {
			choices = append(choices, wire.Choice{Value: m, Title: orDash(stateNames[m])})
		}
		if r.UIVersion >= 2 && len(choices) >= 2 && len(choices) <= 5 {
			sec.Items = append(sec.Items, wire.Component{Type: "segmented", Name: "hvac", Value: s.State, Choices: choices, Action: &wire.Action{ID: "hvac"}})
		} else if len(choices) >= 2 {
			sec.Items = append(sec.Items, wire.Component{Type: "field", Name: "hvac", Label: "模式", Kind: "choice", Value: s.State, Choices: choices},
				wire.Component{Type: "button", Title: "设成这个模式", Action: &wire.Action{ID: "hvac", Submit: true}})
		}
	}
	if opts := options(s); len(opts) > 0 {
		var choices []wire.Choice
		for _, o := range opts {
			choices = append(choices, wire.Choice{Value: o})
		}
		sec.Items = append(sec.Items, wire.Component{Type: "field", Name: "option", Label: "选项", Kind: "choice", Value: s.State, Choices: choices},
			wire.Component{Type: "button", Title: "设成这个", Action: &wire.Action{ID: "opt", Submit: true}})
	}
	if lo, hi, step, ok := numberRange(s); ok {
		sec.Items = append(sec.Items, wire.Component{Type: "field", Name: "value", Label: fmt.Sprintf("数值（%s–%s，步长 %s）", tidy(lo), tidy(hi), tidy(step)),
			Kind: "number", Value: s.State}, wire.Component{Type: "button", Title: "设成这个数", Action: &wire.Action{ID: "num", Submit: true}})
	}
	return sec
}

// historySection is the last 24 hours: a line for a number, the last changes otherwise.
func (a *app) historySection(ctx context.Context, s State) wire.Section {
	sec := wire.Section{Title: "最近 24 小时"}
	h, _, _ := a.client()
	changes, err := h.History(ctx, s.EntityID, time.Now().Add(-24*time.Hour))
	if err != nil {
		sec.Items = append(sec.Items, wire.Component{Type: "text", Text: "读不到历史：" + err.Error(), Style: "secondary"})
		return sec
	}
	if len(changes) == 0 {
		return sec
	}
	if pts := series(changes, 96); len(pts) >= 3 {
		unit := s.attr("unit_of_measurement")
		for i := range pts {
			pts[i].Display = tidy(pts[i].Value) + unit
		}
		sec.Items = append(sec.Items, wire.Component{Type: "chart", Kind: "line", Unit: unit, Series: []wire.ChartSeries{{Name: s.Name(), Points: pts}}})
		return sec
	}
	t := wire.Component{Type: "table", Columns: []wire.TableColumn{{Title: "时间"}, {Title: "状态"}}}
	shown := changes
	if len(shown) > 12 {
		shown = shown[len(shown)-12:]
	}
	for i := len(shown) - 1; i >= 0; i-- {
		c := shown[i]
		t.Rows = append(t.Rows, []string{c.At.Local().Format("01-02 15:04"), orDash(stateText(State{EntityID: s.EntityID, State: c.State, Attributes: s.Attributes}))})
	}
	sec.Items = append(sec.Items, t)
	return sec
}

// series turns numeric changes into at most n points: the value each change set, averaged into n even buckets over
// the span when there are more.
func series(cs []Change, n int) []wire.ChartPoint {
	type pt struct {
		at time.Time
		v  float64
	}
	var ps []pt
	for _, c := range cs {
		if f, err := strconv.ParseFloat(c.State, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			ps = append(ps, pt{c.At, f})
		}
	}
	if len(ps) < len(cs)/2 || len(ps) == 0 {
		return nil
	}
	label := func(t time.Time) string { return t.Local().Format("15:04") }
	if len(ps) <= n {
		out := make([]wire.ChartPoint, len(ps))
		for i, p := range ps {
			out[i] = wire.ChartPoint{Label: label(p.at), Value: p.v}
		}
		return out
	}
	start, end := ps[0].at, ps[len(ps)-1].at
	span := end.Sub(start) / time.Duration(n)
	if span <= 0 {
		return nil
	}
	var out []wire.ChartPoint
	j := 0
	last := ps[0].v
	for b := range n {
		lo, hi := start.Add(span*time.Duration(b)), start.Add(span*time.Duration(b+1))
		sum, cnt := 0.0, 0
		for j < len(ps) && (ps[j].at.Before(hi) || b == n-1) {
			if !ps[j].at.Before(lo) {
				sum, cnt = sum+ps[j].v, cnt+1
			}
			j++
		}
		if cnt > 0 {
			last = sum / float64(cnt)
		}
		out = append(out, wire.ChartPoint{Label: label(lo), Value: math.Round(last*100) / 100})
	}
	return out
}
