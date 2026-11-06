package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestMain(m *testing.M) {
	settle = 0
	os.Exit(m.Run())
}

// ---- a fake Home Assistant, with a synthetic home ----

type fakeHA struct {
	srv   *httptest.Server
	mu    sync.Mutex
	state map[string]State
	calls []string // "domain.service entity_ids data"
	token string
	hits  int  // requests that reached it
	user  bool // the token's user is not an administrator: the template API answers 401, as Home Assistant's does
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{token: "good-token", state: map[string]State{}}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, s := range []State{
		{EntityID: "light.living", State: "on", Attributes: map[string]any{"friendly_name": "客厅灯", "brightness": 128.0, "supported_color_modes": []any{"brightness"}}},
		{EntityID: "light.bed", State: "off", Attributes: map[string]any{"friendly_name": "卧室灯", "supported_color_modes": []any{"onoff"}}},
		{EntityID: "switch.fan_plug", State: "unavailable", Attributes: map[string]any{"friendly_name": "风扇插座"}},
		{EntityID: "climate.living_ac", State: "cool", Attributes: map[string]any{"friendly_name": "客厅空调", "temperature": 26.0, "current_temperature": 27.5,
			"target_temp_step": 1.0, "min_temp": 16.0, "max_temp": 30.0, "hvac_modes": []any{"off", "cool", "heat", "auto"}}},
		{EntityID: "sensor.living_temp", State: "27.5", Attributes: map[string]any{"friendly_name": "客厅温度", "unit_of_measurement": "°C", "device_class": "temperature"}},
		{EntityID: "sensor.fridge_freezer", State: "-18", Attributes: map[string]any{"friendly_name": "冷冻室温度", "unit_of_measurement": "°C", "device_class": "temperature"}},
		{EntityID: "lock.front", State: "locked", Attributes: map[string]any{"friendly_name": "入户门锁"}},
		{EntityID: "select.ac_mode", State: "标准", Attributes: map[string]any{"friendly_name": "空调风模式", "options": []any{"标准", "静音"}}},
		{EntityID: "binary_sensor.leak", State: "on", Attributes: map[string]any{"friendly_name": "厨房漏水", "device_class": "moisture"}},
		{EntityID: "light.other_home", State: "on", Attributes: map[string]any{"friendly_name": "别处的灯"}},
		{EntityID: "shell_command.reboot", State: "unknown", Attributes: map[string]any{}},
	} {
		s.LastChanged, s.LastUpdated = at, at
		f.state[s.EntityID] = s
	}
	areas := `[["living","客厅",["light.living","climate.living_ac","sensor.living_temp","switch.fan_plug","select.ac_mode","sensor.fridge_freezer"]],` +
		`["bedroom","卧室",["light.bed","lock.front"]],["kitchen","厨房",["binary_sensor.leak"]],["away","别处",["light.other_home"]]]`
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch p := r.URL.Path; {
		case p == "/api/":
			fmt.Fprint(w, `{"message":"API running."}`)
		case p == "/api/states":
			var list []State
			for _, s := range f.state {
				list = append(list, s)
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(p, "/api/states/"):
			s, ok := f.state[strings.TrimPrefix(p, "/api/states/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(s)
		case p == "/api/template":
			if f.user {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"Unauthorized"}`)
				return
			}
			fmt.Fprint(w, areas)
		case p == "/api/services" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"domain":"light","services":{"turn_on":{},"turn_off":{},"toggle":{}}},{"domain":"lock","services":{"lock":{},"unlock":{}}},`+
				`{"domain":"climate","services":{"set_temperature":{},"set_hvac_mode":{},"turn_off":{}}},{"domain":"select","services":{"select_option":{}}}]`)
		case strings.HasPrefix(p, "/api/services/") && r.Method == http.MethodPost:
			parts := strings.Split(strings.TrimPrefix(p, "/api/services/"), "/")
			var data map[string]any
			_ = json.NewDecoder(r.Body).Decode(&data)
			ids, _ := data["entity_id"].([]any)
			delete(data, "entity_id")
			b, _ := json.Marshal(data)
			f.calls = append(f.calls, fmt.Sprintf("%s.%s %v %s", parts[0], parts[1], ids, b))
			for _, x := range ids {
				id := x.(string)
				s := f.state[id]
				switch parts[1] {
				case "turn_on":
					s.State = "on"
				case "turn_off":
					s.State = "off"
				case "unlock":
					s.State = "unlocked"
				case "select_option":
					s.State = data["option"].(string)
				case "set_temperature":
					s.Attributes["temperature"] = data["temperature"]
				}
				f.state[id] = s
			}
			fmt.Fprint(w, `[]`)
		case strings.HasPrefix(p, "/api/history/period/"):
			id := r.URL.Query().Get("filter_entity_id")
			if id == "sensor.living_temp" {
				var rows []string
				for i := range 30 {
					rows = append(rows, fmt.Sprintf(`{"state":"%g","last_changed":"%s"}`, 25+float64(i%5), at.Add(time.Duration(i)*10*time.Minute).Format(time.RFC3339)))
				}
				fmt.Fprint(w, "[["+strings.Join(rows, ",")+"]]")
				return
			}
			fmt.Fprintf(w, `[[{"state":"off","last_changed":"%s"},{"state":"on","last_changed":"%s"}]]`, at.Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHA) lastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func newTestApp(t *testing.T, f *fakeHA, cfg Config) *app {
	t.Helper()
	a := newApp(t.Logf)
	if cfg.URL == "" {
		cfg.URL = f.srv.URL
	}
	if cfg.HiddenAreas == nil {
		cfg.HiddenAreas = []string{"别处"}
	}
	a.configure(cfg, f.token)
	return a
}

// ---- the page ----

func texts(v *wire.View) string {
	var b strings.Builder
	if v.Search != nil {
		fmt.Fprintf(&b, "[search %q]\n", v.Search.Value)
	}
	if v.Filter != nil {
		fmt.Fprintf(&b, "[filter %s=%q %d choices]\n", v.Filter.Name, v.Filter.Value, len(v.Filter.Choices))
	}
	for _, s := range v.Sections {
		fmt.Fprintf(&b, "## %s | %s\n", s.Title, s.Footer)
		for _, it := range s.Items {
			fmt.Fprintf(&b, "%s %s|%s|%s|%s|%s|%s", it.Type, it.Title, it.Subtitle, it.Value, it.Text, it.Label, it.Detail)
			if it.Badge != nil {
				fmt.Fprintf(&b, " [%s %s]", it.Badge.Text, it.Badge.Tone)
			}
			if it.Action != nil {
				fmt.Fprintf(&b, " →%s", it.Action.ID)
				if it.Action.Confirm != "" {
					fmt.Fprintf(&b, " (confirm %s)", it.Action.Confirm)
				}
			}
			for _, c := range it.Choices {
				fmt.Fprintf(&b, " <%s>", c.Value)
			}
			for _, r := range it.Rows {
				fmt.Fprintf(&b, " %v", r)
			}
			for _, s := range it.Series {
				fmt.Fprintf(&b, " <series %d>", len(s.Points))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func req(state any, action string, values map[string]string) *ext.UIRequest {
	r := &ext.UIRequest{}
	r.Contribution, r.UIVersion, r.TZ, r.Action, r.Values = pageUI, 5, "Asia/Shanghai", action, values
	if state != nil {
		r.State = ext.EncodeState(state)
	}
	return r
}

func check(t *testing.T, v *wire.View) string {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := exttest.CheckView(raw, false); err != nil {
		t.Fatalf("the hub would refuse the view: %v\n%s", err, texts(v))
	}
	return texts(v)
}

func act(t *testing.T, a *app, st pageState, action string, values map[string]string) (*wire.ActionResult, pageState) {
	t.Helper()
	res, err := a.action(context.Background(), req(st, action, values))
	if err != nil || res.View == nil {
		t.Fatalf("%s: %v", action, err)
	}
	check(t, res.View)
	var next pageState
	_ = json.Unmarshal([]byte(res.View.State), &next)
	return res, next
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("no %q in:\n%s", w, got)
		}
	}
}

func TestPage(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})

	// the overview: areas with what is on, alarms first; a hidden area nowhere
	v, _ := a.render(context.Background(), req(nil, "", nil))
	got := check(t, v)
	contains(t, got, "[search", "[filter area=\"\" 4 choices]", "## 报警", "厨房漏水|传感器 · 厨房|", "[有水 danger]",
		"row 客厅|3 个设备，2 个开着，1 个离线，温度 27.5°|", "[2 开着 positive] →area:living", "→area:bedroom")
	if strings.Contains(got, "别处") {
		t.Errorf("a hidden area shows:\n%s", got)
	}

	// an area: its devices, offline ones behind a row; sensors on the other tab
	res, st := act(t, a, pageState{}, "area:living", nil)
	got = texts(res.View)
	contains(t, got, "segmented", "## 设备 · 2", "客厅灯|灯 · 亮度 50%||", "[开 positive] →open:light.living", "[制冷 positive]", "还有 1 个离线的")
	if strings.Contains(got, "客厅温度") {
		t.Errorf("a sensor among the devices:\n%s", got)
	}
	res, st = act(t, a, st, "kind", map[string]string{"kind": "sensors"})
	contains(t, texts(res.View), "客厅温度|传感器|27.5 °C|")
	res, _ = act(t, a, pageState{Area: "living"}, "offline", nil)
	contains(t, texts(res.View), "风扇插座", "[离线 caution]", "不列离线的")

	// a light: its controls, a tap turns it off
	res, st = act(t, a, pageState{Area: "living"}, "open:light.living", nil)
	got = texts(res.View)
	contains(t, got, "metric ||开||状态|亮度 50%", "button 关", "→svc:turn_off", `→svc:turn_on|{"brightness_pct":20}`, "## 最近 24 小时", "## 属性", "[brightness 128]")
	if res.View.Search != nil {
		t.Errorf("a search box on an entity's page")
	}
	res, st = act(t, a, st, "svc:turn_off", nil)
	if f.lastCall() != "light.turn_off [light.living] {}" || !strings.Contains(res.Toast, "好了：客厅灯：关") {
		t.Fatalf("turn off: %q, toast %q", f.lastCall(), res.Toast)
	}
	contains(t, texts(res.View), "→svc:turn_on")
	if _, st2 := act(t, a, st, `svc:turn_on|{"brightness_pct":50,"entity_id":"lock.front"}`, nil); st2.Entity != "light.living" ||
		f.lastCall() != `light.turn_on [light.living] {"brightness_pct":50}` {
		t.Fatalf("a tap retargeted: %q", f.lastCall())
	}

	// a climate: temperature steps, its modes as a choice
	res, st = act(t, a, pageState{}, "open:climate.living_ac", nil)
	contains(t, texts(res.View), `→svc:set_temperature|{"temperature":25}`, `→svc:set_temperature|{"temperature":27}`, "segmented", "<cool>", "→hvac")
	act(t, a, st, "hvac", map[string]string{"hvac": "heat"})
	if f.lastCall() != `climate.set_hvac_mode [climate.living_ac] {"hvac_mode":"heat"}` {
		t.Fatalf("hvac: %q", f.lastCall())
	}

	// a lock asks first; a select takes an option
	res, _ = act(t, a, pageState{}, "open:lock.front", nil)
	contains(t, texts(res.View), "button 开锁", "(confirm 开锁「入户门锁」？)")
	res, st = act(t, a, pageState{}, "open:select.ac_mode", nil)
	contains(t, texts(res.View), "field ||标准||选项|", "<静音>", "→opt")
	act(t, a, st, "opt", map[string]string{"option": "静音"})
	if f.lastCall() != `select.select_option [select.ac_mode] {"option":"静音"}` {
		t.Fatalf("select: %q", f.lastCall())
	}

	// a sensor: a chart of its last day
	res, _ = act(t, a, pageState{}, "open:sensor.living_temp", nil)
	contains(t, texts(res.View), "chart", "<series 30>")

	// search: across areas, hidden ones left out
	res, st = act(t, a, pageState{}, "search", map[string]string{"q": "灯"})
	got = texts(res.View)
	contains(t, got, "搜索「灯」· 2", "客厅灯|灯 · 亮度 50% · 客厅|", "卧室灯")
	if strings.Contains(got, "别处的灯") {
		t.Errorf("search shows a hidden area:\n%s", got)
	}
	res, _ = act(t, a, st, "clear", nil)
	contains(t, texts(res.View), "## 区域 · 3")
}

// TestNotAdmin: a token whose user is not an administrator reads states but not the areas (Home Assistant keeps its
// template API for administrators): the page and the tools group by type and say why, instead of failing as a 401.
func TestNotAdmin(t *testing.T) {
	f := newFakeHA(t)
	f.user = true
	a := newTestApp(t, f, Config{})
	v, _ := a.render(context.Background(), req(nil, "", nil))
	got := check(t, v)
	contains(t, got, "不是 Home Assistant 的管理员", "## 类型 · ", "row 灯|3 个设备，2 个开着|", "→area:@light", "[filter area=\"\"")
	if strings.Contains(got, "不认这个令牌") {
		t.Fatalf("a valid token reads as refused:\n%s", got)
	}
	res, _ := act(t, a, pageState{}, "area:@light", nil)
	contains(t, texts(res.View), "客厅灯", "卧室灯")
	if st := a.status(); st["state"] != "ok" {
		t.Fatalf("status %v", st)
	}
	out, err := connector(t, a, "tools.call", map[string]any{"contribution": id, "name": id + "_overview"})
	if err != nil || !strings.Contains(out["text"].(string), "不是 Home Assistant 的管理员") {
		t.Fatalf("overview: %v %v", out, err)
	}
}

func TestPageNotWritable(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{Writable: []string{"climate"}})
	res, st := act(t, a, pageState{}, "open:light.living", nil)
	contains(t, texts(res.View), "不在扩展设置的可操作类型里")
	if strings.Contains(texts(res.View), "→svc:") {
		t.Fatalf("controls for a domain not writable:\n%s", texts(res.View))
	}
	res, _ = act(t, a, st, "svc:turn_off", nil)
	if f.lastCall() != "" || !strings.Contains(res.Toast, "不在扩展设置") {
		t.Fatalf("a forged tap went through: %q %q", f.lastCall(), res.Toast)
	}
}

func TestPageUnconfigured(t *testing.T) {
	a := newApp(t.Logf)
	v, _ := a.render(context.Background(), req(nil, "", nil))
	contains(t, check(t, v), "还没填 Home Assistant 的地址和令牌", "→refresh")
	a.configure(Config{URL: "ftp://x"}, "t")
	v, _ = a.render(context.Background(), req(nil, "", nil))
	contains(t, check(t, v), "地址要写成 http://")
	if st := a.status(); st["state"] != "not_configured" {
		t.Fatalf("status %v", st)
	}
}

// ---- the connector ----

func connector(t *testing.T, a *app, method string, params any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, err := a.connector(context.Background(), method, raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func TestActions(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})
	if _, err := a.home(context.Background(), 0); err != nil { // warm the caches describe reads
		t.Fatal(err)
	}
	h, _, _ := a.client()
	if _, err := h.Services(context.Background()); err != nil {
		t.Fatal(err)
	}

	card, err := connector(t, a, "actions.describe", map[string]any{"contribution": id, "name": callAction,
		"args": map[string]any{"entity_id": "light.living", "service": "turn_on", "data": map[string]any{"brightness_pct": 30}}})
	if err != nil || !strings.Contains(card["summary"].(string), "对「客厅灯」（light.living，现在开）执行：打开（light.turn_on），参数 {\"brightness_pct\":30}") {
		t.Fatalf("describe: %v %v", card, err)
	}
	if f.lastCall() != "" {
		t.Fatal("describe called a service")
	}
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{callAction, map[string]any{"entity_id": "lock.front", "service": "unlock"}, "homeassistant_secure_call"},
		{secureAction, map[string]any{"entity_id": "light.living", "service": "turn_on"}, "只管门锁和警戒"},
		{callAction, map[string]any{"entity_id": []string{"light.living", "switch.fan_plug"}, "service": "turn_on"}, "同一类型"},
		{callAction, map[string]any{"entity_id": "light.living", "service": "turn_on", "data": map[string]any{"area_id": "living"}}, "不能写 area_id"},
		{callAction, map[string]any{"entity_id": "shell_command.reboot", "service": "turn_on"}, "不在扩展设置的可操作类型里"},
		{callAction, map[string]any{"entity_id": "light.living", "service": "explode"}, "light 没有服务 explode"},
		{callAction, map[string]any{"entity_id": "Light Living", "service": "turn_on"}, "不是实体 id"},
		{callAction, map[string]any{"service": "turn_on"}, "entity_id"},
	} {
		if _, err := connector(t, a, "actions.describe", map[string]any{"contribution": id, "name": c.name, "args": c.args}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: want %q, got %v", c.name, c.args, c.want, err)
		}
	}

	// do: once per request id
	args := map[string]any{"entity_id": "lock.front", "service": "unlock"}
	out, err := connector(t, a, "actions.do", map[string]any{"contribution": id, "name": secureAction, "args": args, "request_id": "r1"})
	if err != nil || !strings.Contains(out["text"].(string), "入户门锁：没锁") || f.lastCall() != "lock.unlock [lock.front] {}" {
		t.Fatalf("do: %v %v %q", out, err, f.lastCall())
	}
	n := len(f.calls)
	if _, err := connector(t, a, "actions.do", map[string]any{"contribution": id, "name": secureAction, "args": args, "request_id": "r1"}); err != nil || len(f.calls) != n {
		t.Fatalf("the same request id ran twice: %v", err)
	}
	if _, err := connector(t, a, "actions.do", map[string]any{"contribution": id, "name": secureAction, "args": args}); err == nil {
		t.Fatal("do without a request id")
	}
}

func TestTools(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})
	call := func(name string, args any) string {
		out, err := connector(t, a, "tools.call", map[string]any{"contribution": id, "name": name, "args": args})
		if err != nil {
			return "ERR " + err.Error()
		}
		return out["text"].(string)
	}
	contains(t, call(id+"_overview", nil), "## 客厅：3 个设备，2 个开着，1 个离线，温度 27.5°", "- 客厅灯（light.living）：开，亮度 50%，在客厅", "## 报警", "厨房漏水：有水")
	if got := call(id+"_overview", map[string]any{"area": "别处"}); !strings.Contains(got, "ERR 没有区域") {
		t.Errorf("a hidden area: %s", got)
	}
	contains(t, call(id+"_search", map[string]any{"query": "空调"}), "找到 2 个", "客厅空调（climate.living_ac）：制冷，室温 27.5° · 设定 26°")
	contains(t, call(id+"_search", map[string]any{"domain": "switch"}), "没找到")
	contains(t, call(id+"_search", map[string]any{"domain": "switch", "include_unavailable": true}), "风扇插座（switch.fan_plug）：离线")
	got := call(id+"_entity", map[string]any{"entity_id": "climate.living_ac"})
	contains(t, got, "状态：制冷（原值 cool）", "区域：客厅", "- temperature: 26", "能调用的服务（用 homeassistant_call）：set_hvac_mode、set_temperature、turn_off")
	contains(t, call(id+"_entity", map[string]any{"entity_id": "light.other_home"}), "ERR "+errNoEntity.Error())
	contains(t, call(id+"_history", map[string]any{"entity_id": "sensor.living_temp"}), "30 次记录", "最小 25°C，最大 29°C，平均 27°C")
	contains(t, call(id+"_history", map[string]any{"entity_id": "light.living", "hours": 2}), "2 次记录")
}

func TestAuthPause(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})
	a.configure(Config{URL: f.srv.URL, HiddenAreas: []string{"别处"}}, "eyJ"+strings.Repeat("x", 120)+".a.b")
	count := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits }
	a.render(context.Background(), req(nil, "", nil))
	n := count()
	for range 5 { // the page refreshing on its own
		a.render(context.Background(), req(nil, "", nil))
	}
	if count() != n {
		t.Fatalf("a refused token was tried %d more times", count()-n)
	}
	act(t, a, pageState{}, "refresh", nil) // Retry
	if count() == n {
		t.Fatal("an explicit refresh did not try again")
	}
	a.configure(Config{URL: f.srv.URL}, f.token) // a new token: at once
	v, _ := a.render(context.Background(), req(nil, "", nil))
	contains(t, check(t, v), "## 区域")
}

func TestStatus(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})
	if err := func() error { h, _, _ := a.client(); return h.ping(context.Background()) }(); err != nil {
		t.Fatal(err)
	}
	if st := a.status(); st["state"] != "ok" || st["last_sync"] == nil {
		t.Fatalf("status %v", st)
	}
	a.configure(Config{URL: f.srv.URL}, "bad")
	v, _ := a.render(context.Background(), req(nil, "", nil))
	contains(t, check(t, v), "不认这个令牌", "看起来不完整（3 个字符，不是 eyJ 开头，0 个点）")
	jwt := "eyJ" + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 40)
	a.configure(Config{URL: f.srv.URL}, jwt)
	v, _ = a.render(context.Background(), req(nil, "", nil))
	if got := check(t, v); !strings.Contains(got, "不认这个令牌") || strings.Contains(got, "看起来不完整") {
		t.Fatalf("a well-formed wrong token:\n%s", got)
	}
	if st := a.status(); st["state"] != "not_signed_in" {
		t.Fatalf("status after a 401 %v", st)
	}
}

// TestProtocol holds the program to the protocol, the way the hub speaks it.
func TestProtocol(t *testing.T) {
	f := newFakeHA(t)
	a := newApp(t.Logf)
	cfg, _ := json.Marshal(Config{URL: f.srv.URL})
	res := exttest.Check(t, manifest, exttest.Serve(t, a.program()), cfg)
	if res.ID != id {
		t.Fatalf("initialize answered %+v", res)
	}
}

func TestTokenShape(t *testing.T) {
	if got := tokenShape("eyJ.x 令"); got != "7 个字符，eyJ 开头，1 个点，有空白、有中文或全角字符" {
		t.Errorf("tokenShape: %q", got)
	}
}

func TestCleanToken(t *testing.T) {
	for in, want := range map[string]string{" abc \n": "abc", "Bearer abc": "abc", "\"abc\"": "abc", "bearer  x.y.z": "x.y.z"} {
		if got := cleanToken(in); got != want {
			t.Errorf("cleanToken(%q) = %q", in, got)
		}
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{"http://ha.local:8123/": "http://ha.local:8123", " https://h:8443 ": "https://h:8443",
		"http://h:8123/ha": "http://h:8123/ha", "": "", "ftp://h": "ERR", "http://": "ERR", "http://h/?a=1": "ERR"} {
		got, err := baseURL(in)
		if (want == "ERR") != (err != nil) || want != "ERR" && got != want {
			t.Errorf("baseURL(%q) = %q %v", in, got, err)
		}
	}
}
