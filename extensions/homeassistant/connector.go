package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

// The connector: its status, the assistant's read tools and the two write actions. Writes run only from
// actions.do, which the hub calls after its gate (the phone's confirmation by default; homeassistant_secure_call
// always, confirm_always in the manifest).

const (
	callAction   = id + "_call"
	secureAction = id + "_secure_call"
	callMost     = 20 // entities in one call
)

// settle is how long a call waits before reading the entities back: most devices report within a second.
var settle = 700 * time.Millisecond

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

var tools = []tool{
	{Name: id + "_overview",
		Description: "Home Assistant 里家的概况：每个区域开着的灯和设备、空调 / 温控的状态、温度湿度，以及离线和报警的东西。先用它了解家里现在怎样。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"area": map[string]any{"type": "string", "description": "只看这个区域（名字或 id），不给就是全部"}}}},
	{Name: id + "_search",
		Description: "在 Home Assistant 里找实体：按名字、实体 id、区域或类型（domain，如 light、switch、climate、sensor）找，每个列出实体 id、名字、区域、状态。要操作设备先用它找到准确的 entity_id。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query":               map[string]any{"type": "string", "description": "名字或实体 id 的一部分，如 客厅灯、空调、temperature"},
			"area":                map[string]any{"type": "string", "description": "区域的名字或 id"},
			"domain":              map[string]any{"type": "string", "description": "类型，如 light、switch、climate、cover、sensor"},
			"include_unavailable": map[string]any{"type": "boolean", "description": "也列离线的，默认 false"},
			"limit":               map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "最多几个，默认 30"}}}},
	{Name: id + "_entity",
		Description: "读一个实体的完整状态：状态、属性（亮度、色温、设定温度、模式、电量……）、区域、最后变化时间，以及能对它调用的服务。",
		InputSchema: map[string]any{"type": "object", "required": []string{"entity_id"}, "properties": map[string]any{
			"entity_id": map[string]any{"type": "string", "description": "实体 id，如 light.living_room"}}}},
	{Name: id + "_history",
		Description: "读一个实体最近的变化：每次状态变成什么、什么时候（用户时区），旧的在前。数值传感器（温度、湿度、功率）给最小、最大、平均。",
		InputSchema: map[string]any{"type": "object", "required": []string{"entity_id"}, "properties": map[string]any{
			"entity_id": map[string]any{"type": "string"},
			"hours":     map[string]any{"type": "integer", "minimum": 1, "maximum": 168, "description": "最近几小时，默认 24"}}}},
}

func callSchema(secure bool) map[string]any {
	what := "要调用的服务，如 turn_on、turn_off、toggle、set_temperature、open_cover、select_option、set_value"
	if secure {
		what = "要调用的服务：门锁 lock / unlock / open，警戒 alarm_arm_home / alarm_arm_away / alarm_disarm"
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"entity_id", "service"}, "properties": map[string]any{
		"entity_id": map[string]any{"description": "一个实体 id，或同一类型（domain）的几个", "oneOf": []any{
			map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": callMost}}},
		"service": map[string]any{"type": "string", "description": what},
		"data":    map[string]any{"type": "object", "description": "服务的参数，如 {\"brightness_pct\": 50}、{\"temperature\": 24}、{\"option\": \"静音\"}；不要写 entity_id"},
	}}
}

var actions = []tool{
	{Name: callAction,
		Description: "在 Home Assistant 里操作设备：对一个或几个同类型的实体调用一个服务（开关灯、调亮度、调空调温度和模式、开关窗帘、执行场景……）。只能操作扩展设置允许的类型；门锁和警戒用 " + secureAction + "。用户在手机上确认后才执行。",
		InputSchema: callSchema(false)},
	{Name: secureAction,
		Description: "在 Home Assistant 里操作门锁和警戒（上锁、开锁、布防、撤防）。每次都要用户在手机上确认。",
		InputSchema: callSchema(true)},
}

func (a *app) connector(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p struct {
		Contribution string          `json:"contribution"`
		Name         string          `json:"name"`
		Args         json.RawMessage `json:"args"`
		RequestID    string          `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &ext.Error{Code: "invalid_params", Message: err.Error()}
	}
	if p.Contribution != "" && p.Contribution != id {
		return nil, ext.NoContribution(p.Contribution)
	}
	switch method {
	case "status":
		return a.status(), nil
	case "tools.list":
		return map[string]any{"tools": tools}, nil
	case "tools.call":
		text, err := a.callTool(ctx, p.Name, p.Args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	case "actions.list":
		return map[string]any{"tools": actions}, nil
	case "actions.describe":
		c, err := a.readCall(p.Name, p.Args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"action": map[string]string{"name": p.Name, "visible": "家里的设备（Home Assistant）"}, "summary": a.summary(c)}, nil
	case "actions.do":
		c, err := a.readCall(p.Name, p.Args)
		if err != nil {
			return nil, err
		}
		return a.do(ctx, p.RequestID, c)
	}
	return nil, ext.UnknownMethod("connector." + method)
}

// status answers from memory: not configured, or how the last request went.
func (a *app) status() map[string]any {
	h, _, _ := a.client()
	a.mu.Lock()
	cfgErr := a.cfgErr
	a.mu.Unlock()
	if cfgErr != nil || !h.configured() {
		why := errNotConfigured.Error()
		if cfgErr != nil {
			why = cfgErr.Error()
		}
		return map[string]any{"state": "not_configured", "error": why, "guide": []string{
			"在扩展设置里填 Home Assistant 的地址（如 http://homeassistant.local:8123）",
			"在 Home Assistant 的「个人资料 › 安全」里新建一个长期访问令牌，填进扩展的密钥 token"}}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := map[string]any{"state": "ok", "account": h.host()}
	if !h.lastOK.IsZero() {
		st["last_sync"] = h.lastOK
	}
	if h.lastErr != nil && h.lastErrAt.After(h.lastOK) {
		st["error"] = clip(oneLine(h.lastErr.Error()), 200)
		st["state"] = "error"
		if errors.Is(h.lastErr, errUnauthorized) {
			st["state"] = "not_signed_in"
		}
	}
	return st
}

// a call is one write, read from the action's args.
type call struct {
	Secure   bool
	Domain   string
	Service  string
	Entities []string
	Data     map[string]any
}

var (
	entityRE  = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z0-9_]+$`)
	serviceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// readCall checks an action's args without the network (actions.describe must not reach out): entity ids of one
// domain, a service name, the domain writable on this path, and data that does not retarget the call.
func (a *app) readCall(name string, raw json.RawMessage) (call, error) {
	var c call
	switch name {
	case callAction:
	case secureAction:
		c.Secure = true
	default:
		return c, ext.UnknownMethod("connector.action " + name)
	}
	var args struct {
		EntityID json.RawMessage `json:"entity_id"`
		Service  string          `json:"service"`
		Data     map[string]any  `json:"data"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return c, bad("参数要有 entity_id 和 service")
	}
	var one string
	if json.Unmarshal(args.EntityID, &one) == nil {
		c.Entities = []string{one}
	} else if json.Unmarshal(args.EntityID, &c.Entities) != nil {
		return c, bad("entity_id 要是一个实体 id 或几个")
	}
	if len(c.Entities) == 0 || len(c.Entities) > callMost {
		return c, bad(fmt.Sprintf("entity_id 要有 1 到 %d 个", callMost))
	}
	for _, e := range c.Entities {
		if !entityRE.MatchString(e) {
			return c, bad(fmt.Sprintf("「%s」不是实体 id（像 light.living_room 这样）", clip(e, 60)))
		}
		d, _, _ := strings.Cut(e, ".")
		if c.Domain == "" {
			c.Domain = d
		} else if d != c.Domain {
			return c, bad("一次只能操作同一类型（domain）的实体")
		}
	}
	if c.Service = strings.TrimSpace(args.Service); !serviceRE.MatchString(c.Service) {
		return c, bad("service 要是服务名，如 turn_on")
	}
	if err := a.canWrite(c.Domain, c.Secure); err != nil {
		return c, bad(err.Error())
	}
	for _, k := range []string{"entity_id", "device_id", "area_id", "floor_id", "label_id"} {
		if _, ok := args.Data[k]; ok {
			return c, bad("data 里不能写 " + k + "：要操作的实体只能写在 entity_id 里")
		}
	}
	c.Data = args.Data
	h, _, _ := a.client()
	h.mu.Lock()
	services := h.services
	h.mu.Unlock()
	if ds, ok := services[c.Domain]; ok && services != nil {
		if _, ok := ds[c.Service]; !ok {
			return c, bad(fmt.Sprintf("%s 没有服务 %s", c.Domain, c.Service))
		}
	}
	return c, nil
}

func bad(msg string) error { return &ext.Error{Code: "invalid_params", Message: msg} }

// summary is the card's text: which devices, what is done, with what.
func (a *app) summary(c call) string {
	h, _, _ := a.client()
	var names []string
	for _, e := range c.Entities {
		if s, ok := h.cachedState(e); ok {
			names = append(names, fmt.Sprintf("「%s」（%s，现在%s）", s.Name(), e, orDash(stateText(s))))
		} else {
			names = append(names, e)
		}
	}
	verb := serviceText(c.Domain, c.Service)
	out := fmt.Sprintf("对%s执行：%s", strings.Join(names, "、"), verb)
	if len(c.Data) > 0 {
		b, _ := json.Marshal(c.Data)
		out += "，参数 " + clip(string(b), 300)
	}
	if c.Secure {
		out = "【门锁 / 警戒】" + out
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

var serviceNames = map[string]string{
	"turn_on": "打开", "turn_off": "关闭", "toggle": "切换开关", "open_cover": "打开", "close_cover": "关上", "stop_cover": "停下",
	"set_temperature": "设温度", "set_hvac_mode": "设模式", "set_fan_mode": "设风速", "lock": "上锁", "unlock": "开锁", "open": "打开门",
	"alarm_arm_home": "在家警戒", "alarm_arm_away": "离家警戒", "alarm_disarm": "撤防", "press": "按下", "select_option": "选择",
	"set_value": "设数值", "media_play_pause": "播放 / 暂停", "volume_set": "设音量", "start": "开始", "return_to_base": "回充",
}

func serviceText(domain, service string) string {
	if n, ok := serviceNames[service]; ok {
		return n + "（" + domain + "." + service + "）"
	}
	return domain + "." + service
}

// done remembers the answers of actions.do by request id: the hub may ask again for the same one.
var (
	doneMu sync.Mutex
	done   = map[string]string{}
)

func (a *app) do(ctx context.Context, requestID string, c call) (any, error) {
	if requestID == "" || len(requestID) > 256 {
		return nil, bad("没有 request_id")
	}
	doneMu.Lock()
	if text, ok := done[requestID]; ok {
		doneMu.Unlock()
		return map[string]any{"text": text}, nil
	}
	doneMu.Unlock()
	text, err := a.run(ctx, c)
	if err != nil {
		return nil, err
	}
	doneMu.Lock()
	if len(done) > 500 {
		done = map[string]string{}
	}
	done[requestID] = text
	doneMu.Unlock()
	return map[string]any{"text": text}, nil
}

// run calls the service and reads the entities back.
func (a *app) run(ctx context.Context, c call) (string, error) {
	h, _, _ := a.client()
	data := map[string]any{}
	for k, v := range c.Data {
		data[k] = v
	}
	data["entity_id"] = c.Entities
	if err := h.Call(ctx, c.Domain, c.Service, data); err != nil {
		return "", err
	}
	time.Sleep(settle)
	var lines []string
	for _, e := range c.Entities {
		if s, err := h.State(ctx, e); err == nil {
			line := fmt.Sprintf("%s：%s", s.Name(), orDash(stateText(s)))
			if d := detail(s); d != "" {
				line += "（" + d + "）"
			}
			lines = append(lines, line)
		}
	}
	return "已执行 " + c.Domain + "." + c.Service + "。现在：" + strings.Join(lines, "；"), nil
}

// ---- the read tools ----

func (a *app) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var args struct {
		Area        string `json:"area"`
		Query       string `json:"query"`
		Domain      string `json:"domain"`
		EntityID    string `json:"entity_id"`
		Unavailable bool   `json:"include_unavailable"`
		Limit       int    `json:"limit"`
		Hours       int    `json:"hours"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("参数不对：%w", err)
		}
	}
	switch name {
	case id + "_overview":
		return a.toolOverview(ctx, args.Area)
	case id + "_search":
		limit := args.Limit
		if limit <= 0 || limit > 100 {
			limit = 30
		}
		return a.toolSearch(ctx, args.Query, args.Area, args.Domain, args.Unavailable, limit)
	case id + "_entity":
		return a.toolEntity(ctx, args.EntityID)
	case id + "_history":
		hours := args.Hours
		if hours <= 0 || hours > 168 {
			hours = 24
		}
		return a.toolHistory(ctx, args.EntityID, hours)
	}
	return "", fmt.Errorf("没有工具 %s", name)
}

// home is one read of everything the page and the tools draw from: the states and the areas, indexed.
type home struct {
	States []State
	ByID   map[string]State
	Areas  []Area           // the visible ones, in Home Assistant's order
	AreaOf map[string]*Area // entity → its visible area; missing: none, or a hidden one
	Hidden map[string]bool  // entities in a hidden area
	// ByType says the areas are not Home Assistant's: it would not give them (its template API needs an admin's
	// token), so the "areas" are the device types. Note says why, for the page and the tools.
	ByType bool
	Note   string
}

// errNotAdmin: the token reads states but not the template API, which Home Assistant keeps for administrators.
var errNotAdmin = errors.New("这个令牌的用户不是 Home Assistant 的管理员，读不到区域（区域要经模板接口读，只有管理员能用）：先按类型显示；要按区域，用管理员账号新建令牌")

func (a *app) home(ctx context.Context, fresh time.Duration) (*home, error) {
	h, cfg, _ := a.client()
	if !h.configured() {
		a.mu.Lock()
		err := a.cfgErr
		a.mu.Unlock()
		if err == nil {
			err = errNotConfigured
		}
		return nil, err
	}
	states, err := h.States(ctx, fresh)
	if err != nil {
		return nil, err
	}
	hm := &home{States: states, ByID: map[string]State{}, AreaOf: map[string]*Area{}, Hidden: map[string]bool{}}
	for _, s := range states {
		hm.ByID[s.EntityID] = s
	}
	areas, err := h.Areas(ctx)
	switch {
	case errors.Is(err, errUnauthorized): // the states were read with this token: it is the template API
		areas, hm.ByType, hm.Note = byType(states), true, errNotAdmin.Error()
		h.note(nil)
	case err != nil:
		areas, hm.ByType, hm.Note = byType(states), true, "读不到区域："+err.Error()
	}
	for _, ar := range areas {
		if hidden(cfg, ar) {
			for _, e := range ar.Entities {
				hm.Hidden[e] = true
			}
			continue
		}
		hm.Areas = append(hm.Areas, ar)
	}
	for i := range hm.Areas {
		for _, e := range hm.Areas[i].Entities {
			hm.AreaOf[e] = &hm.Areas[i]
		}
	}
	return hm, nil
}

// byType makes stand-in areas of the device types, in deviceDomains' order: "@light" 灯, "@switch" 开关…
func byType(states []State) []Area {
	var out []Area
	for _, d := range deviceDomains {
		ar := Area{ID: "@" + d, Name: domainName(d)}
		for _, s := range states {
			if s.Domain() == d {
				ar.Entities = append(ar.Entities, s.EntityID)
			}
		}
		if len(ar.Entities) > 0 {
			out = append(out, ar)
		}
	}
	return out
}

// area finds an area by name or id (case-insensitive), or nil.
func (hm *home) area(q string) *Area {
	q = strings.TrimSpace(q)
	for i := range hm.Areas {
		if strings.EqualFold(hm.Areas[i].Name, q) || strings.EqualFold(hm.Areas[i].ID, q) {
			return &hm.Areas[i]
		}
	}
	return nil
}

func (hm *home) areaName(e string) string {
	if ar := hm.AreaOf[e]; ar != nil {
		return ar.Name
	}
	return ""
}

// match finds entities by query (name or id), area and domain; hidden areas' entities never.
func (hm *home) match(query string, ar *Area, domain string, unavailable bool) []State {
	q := strings.ToLower(strings.TrimSpace(query))
	var in map[string]bool
	if ar != nil {
		in = map[string]bool{}
		for _, e := range ar.Entities {
			in[e] = true
		}
	}
	var out []State
	for _, s := range hm.States {
		switch {
		case hm.Hidden[s.EntityID] && hm.AreaOf[s.EntityID] == nil:
		case in != nil && !in[s.EntityID]:
		case domain != "" && s.Domain() != domain:
		case !unavailable && s.Unavailable():
		case q != "" && !strings.Contains(strings.ToLower(s.Name()), q) && !strings.Contains(s.EntityID, q) &&
			!strings.Contains(strings.ToLower(hm.areaName(s.EntityID)), q):
		default:
			out = append(out, s)
		}
	}
	// the name matches first, then devices before the rest
	sort.SliceStable(out, func(i, j int) bool {
		ni, nj := strings.Contains(strings.ToLower(out[i].Name()), q), strings.Contains(strings.ToLower(out[j].Name()), q)
		if ni != nj {
			return ni
		}
		di, dj := slices.Contains(deviceDomains, out[i].Domain()), slices.Contains(deviceDomains, out[j].Domain())
		return di && !dj
	})
	return out
}

func entityLine(hm *home, s State) string {
	line := fmt.Sprintf("- %s（%s）：%s", s.Name(), s.EntityID, orDash(stateText(s)))
	if d := detail(s); d != "" {
		line += "，" + d
	}
	if n := hm.areaName(s.EntityID); n != "" {
		line += "，在" + n
	}
	return line
}

func (a *app) toolOverview(ctx context.Context, areaQ string) (string, error) {
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	areas := hm.Areas
	if areaQ != "" {
		ar := hm.area(areaQ)
		if ar == nil {
			return "", fmt.Errorf("没有区域「%s」；有：%s", clip(areaQ, 30), strings.Join(areaNames(hm.Areas), "、"))
		}
		areas = []Area{*ar}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Home Assistant，%s：\n", time.Now().Format("2006-01-02 15:04"))
	if hm.Note != "" {
		b.WriteString("（" + hm.Note + "）\n")
	}
	for _, ar := range areas {
		sum := summarizeArea(hm, ar)
		fmt.Fprintf(&b, "## %s：%s\n", ar.Name, sum.line())
		for _, s := range sum.notable {
			b.WriteString(entityLine(hm, s) + "\n")
		}
	}
	if areaQ == "" {
		var alarms []string
		for _, s := range hm.States {
			if stateTone(s) == "danger" && !hm.Hidden[s.EntityID] {
				alarms = append(alarms, s.Name()+"："+stateText(s))
			}
		}
		if len(alarms) > 0 {
			b.WriteString("## 报警\n- " + strings.Join(alarms, "\n- ") + "\n")
		}
	}
	return b.String(), nil
}

func areaNames(as []Area) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

// areaSummary is what is going on in an area: devices on, climates running, its temperature and humidity.
type areaSummary struct {
	devices, on, offline int
	temp, humid          []float64
	notable              []State // on devices, running climates, alarms
}

func summarizeArea(hm *home, ar Area) areaSummary {
	var s areaSummary
	for _, e := range ar.Entities {
		st, ok := hm.ByID[e]
		if !ok {
			continue
		}
		d := st.Domain()
		if slices.Contains(deviceDomains, d) && d != "scene" && d != "script" {
			s.devices++
			switch {
			case st.Unavailable():
				s.offline++
			case stateTone(st) == "positive" || stateTone(st) == "danger":
				s.on++
				if len(s.notable) < 12 {
					s.notable = append(s.notable, st)
				}
			}
		}
		if d == "sensor" && !st.Unavailable() {
			var f float64
			if _, err := fmt.Sscan(st.State, &f); err == nil {
				// a room's air: a fridge's or a water heater's own sensor is a temperature too, so only what a room
				// can be (10–40 °C) counts
				switch st.attr("device_class") {
				case "temperature":
					if c, ok := celsius(f, st.attr("unit_of_measurement")); ok && c >= 10 && c <= 40 {
						s.temp = append(s.temp, c)
					}
				case "humidity":
					if f >= 0 && f <= 100 {
						s.humid = append(s.humid, f)
					}
				}
			}
		}
		if stateTone(st) == "danger" && !slices.ContainsFunc(s.notable, func(x State) bool { return x.EntityID == st.EntityID }) {
			s.notable = append(s.notable, st)
		}
	}
	return s
}

func (s areaSummary) line() string {
	parts := []string{fmt.Sprintf("%d 个设备，%d 个开着", s.devices, s.on)}
	if s.offline > 0 {
		parts = append(parts, fmt.Sprintf("%d 个离线", s.offline))
	}
	if len(s.temp) > 0 {
		parts = append(parts, "温度 "+tidy(avg(s.temp))+"°")
	}
	if len(s.humid) > 0 {
		parts = append(parts, "湿度 "+tidy(avg(s.humid))+"%")
	}
	return strings.Join(parts, "，")
}

// celsius reads a temperature in °C or °F as °C; false for another unit.
func celsius(v float64, unit string) (float64, bool) {
	switch unit {
	case "°C", "":
		return v, true
	case "°F":
		return (v - 32) * 5 / 9, true
	}
	return 0, false
}

func avg(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return float64(int(t/float64(len(xs))*10+0.5)) / 10
}

func (a *app) toolSearch(ctx context.Context, query, areaQ, domain string, unavailable bool, limit int) (string, error) {
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	var ar *Area
	if areaQ != "" {
		if ar = hm.area(areaQ); ar == nil {
			return "", fmt.Errorf("没有区域「%s」；有：%s", clip(areaQ, 30), strings.Join(areaNames(hm.Areas), "、"))
		}
	}
	found := hm.match(query, ar, strings.ToLower(strings.TrimSpace(domain)), unavailable)
	if len(found) == 0 {
		return "没找到对得上的实体", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 个", len(found))
	if len(found) > limit {
		fmt.Fprintf(&b, "，列前 %d 个（写得具体些，或加 area / domain）", limit)
		found = found[:limit]
	}
	b.WriteString("：\n")
	for _, s := range found {
		b.WriteString(entityLine(hm, s) + "\n")
	}
	return b.String(), nil
}

// noisyAttrs are left out of an entity's attributes: drawn elsewhere or of no use to read.
var noisyAttrs = []string{"friendly_name", "icon", "entity_picture", "supported_features", "attribution", "editable", "id", "restored"}

func (a *app) toolEntity(ctx context.Context, eid string) (string, error) {
	eid = strings.TrimSpace(eid)
	if !entityRE.MatchString(eid) {
		return "", fmt.Errorf("「%s」不是实体 id", clip(eid, 60))
	}
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	s, ok := hm.ByID[eid]
	if !ok || (hm.Hidden[eid] && hm.AreaOf[eid] == nil) {
		return "", errNoEntity
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）\n状态：%s（原值 %s）\n", s.Name(), eid, orDash(stateText(s)), s.State)
	if n := hm.areaName(eid); n != "" {
		fmt.Fprintf(&b, "区域：%s\n", n)
	}
	fmt.Fprintf(&b, "最后变化：%s\n", s.LastChanged.Local().Format("2006-01-02 15:04:05"))
	var keys []string
	for k := range s.Attributes {
		if !slices.Contains(noisyAttrs, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("属性：\n")
		for _, k := range keys {
			v, _ := json.Marshal(s.Attributes[k])
			fmt.Fprintf(&b, "- %s: %s\n", k, clip(string(v), 200))
		}
	}
	if err := a.canWrite(s.Domain(), slices.Contains(secureDomains, s.Domain())); err == nil {
		h, _, _ := a.client()
		if svcs, err := h.Services(ctx); err == nil && len(svcs[s.Domain()]) > 0 {
			var names []string
			for n := range svcs[s.Domain()] {
				names = append(names, n)
			}
			sort.Strings(names)
			via := callAction
			if slices.Contains(secureDomains, s.Domain()) {
				via = secureAction
			}
			fmt.Fprintf(&b, "能调用的服务（用 %s）：%s\n", via, strings.Join(names, "、"))
		}
	} else {
		fmt.Fprintf(&b, "不能操作：%s\n", err)
	}
	return b.String(), nil
}

func (a *app) toolHistory(ctx context.Context, eid string, hours int) (string, error) {
	eid = strings.TrimSpace(eid)
	if !entityRE.MatchString(eid) {
		return "", fmt.Errorf("「%s」不是实体 id", clip(eid, 60))
	}
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	s, ok := hm.ByID[eid]
	if !ok || (hm.Hidden[eid] && hm.AreaOf[eid] == nil) {
		return "", errNoEntity
	}
	h, _, _ := a.client()
	changes, err := h.History(ctx, eid, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return fmt.Sprintf("%s 最近 %d 小时没有记录", s.Name(), hours), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）最近 %d 小时，%d 次记录：\n", s.Name(), eid, hours, len(changes))
	if nums := numeric(changes); len(nums) >= len(changes)/2 && len(nums) > 0 {
		lo, hi := nums[0], nums[0]
		for _, x := range nums {
			lo, hi = min(lo, x), max(hi, x)
		}
		unit := s.attr("unit_of_measurement")
		fmt.Fprintf(&b, "最小 %s%s，最大 %s%s，平均 %s%s\n", tidy(lo), unit, tidy(hi), unit, tidy(avg(nums)), unit)
	}
	shown := changes
	if len(shown) > 60 {
		shown = shown[len(shown)-60:]
		b.WriteString("（只列最近 60 次）\n")
	}
	for _, c := range shown {
		fmt.Fprintf(&b, "%s %s\n", c.At.Local().Format("01-02 15:04"), orDash(stateText(State{EntityID: eid, State: c.State, Attributes: s.Attributes})))
	}
	return b.String(), nil
}

func numeric(cs []Change) []float64 {
	var out []float64
	for _, c := range cs {
		var f float64
		if _, err := fmt.Sscan(c.State, &f); err == nil {
			out = append(out, f)
		}
	}
	return out
}
