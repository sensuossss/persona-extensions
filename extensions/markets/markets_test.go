package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/exttest"
)

// ---- a fake OKX, with synthetic prices ----

type fakeOKX struct {
	srv  *httptest.Server
	hits atomic.Int64
	fail atomic.Bool
}

func newFakeOKX(t *testing.T) *fakeOKX {
	f := &fakeOKX{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		q := r.URL.Query()
		ok := func(data string) { fmt.Fprintf(w, `{"code":"0","msg":"","data":%s}`, data) }
		switch r.URL.Path {
		case "/api/v5/market/tickers":
			if q.Get("instType") == "SWAP" {
				ok(`[` + tickerJSON("BTC-USDT-SWAP", "SWAP", "50010", "49000") + `]`)
				return
			}
			ok(`[` + tickerJSON("BTC-USDT", "SPOT", "50000", "48000") + `,` + tickerJSON("ETH-USDT", "SPOT", "2900", "3000") +
				`,` + tickerJSON("DOGE-USDT", "SPOT", "0.00001234", "0.00001234") + `,` + tickerJSON("WBTC-USDT", "SPOT", "49990", "48000") +
				`,` + tickerJSON("BTC-USDC", "SPOT", "50001", "48000") + `]`)
		case "/api/v5/market/ticker":
			prices := map[string][2]string{"BTC-USDT": {"50000", "48000"}, "ETH-USDT": {"2900", "3000"},
				"DOGE-USDT": {"0.00001234", "0.00001234"}, "BTC-USDC": {"50001", "48000"}, "WBTC-USDT": {"49990", "48000"},
				"BTC-USDT-SWAP": {"50010", "49000"}}
			id := q.Get("instId")
			if pr, found := prices[id]; found {
				ok(`[` + tickerJSON(id, instType(id), pr[0], pr[1]) + `]`)
				return
			}
			fmt.Fprint(w, `{"code":"51001","msg":"Instrument ID doesn't exist.","data":[]}`)
		case "/api/v5/market/candles":
			if id := q.Get("instId"); id != "BTC-USDT" && id != "ETH-USDT" && id != "BTC-USDC" {
				fmt.Fprint(w, `{"code":"51001","msg":"Instrument ID doesn't exist.","data":[]}`)
				return
			}
			n := 0
			fmt.Sscan(q.Get("limit"), &n)
			var rows []string
			start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			for i := n - 1; i >= 0; i-- { // newest first, as OKX answers
				o := 48000 + float64(i)*30
				rows = append(rows, fmt.Sprintf(`["%d","%g","%g","%g","%g","%g","0","0","1"]`,
					start.Add(time.Duration(i)*time.Hour).UnixMilli(), o, o+50, o-40, o+20, 10+float64(i)))
			}
			ok("[" + strings.Join(rows, ",") + "]")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func tickerJSON(id, typ, last, open string) string {
	return fmt.Sprintf(`{"instType":%q,"instId":%q,"last":%q,"open24h":%q,"high24h":"51000","low24h":"47000","vol24h":"1234.5","volCcy24h":"61725000.25","ts":"1767312000000"}`,
		typ, id, last, open)
}

func newTestPlugin(t *testing.T, f *fakeOKX, config string) (*plugin, string) {
	t.Helper()
	p := newPlugin(t.Logf)
	p.srcs = sources{newOKX(f.srv.URL)}
	dir := t.TempDir()
	var ip pluginkit.InitParams
	ip.Config = json.RawMessage(config)
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	return p, dir
}

// texts is a view's words, one item a line, an action after its item as →id.
func texts(v pluginkit.View) string {
	var b strings.Builder
	for _, s := range v.Sections {
		fmt.Fprintf(&b, "## %s | %s\n", s.Title, s.Footer)
		for _, it := range s.Items {
			fmt.Fprintf(&b, "%s %s %s %s %s %s", it.Type, it.Title, it.Subtitle, it.Value, it.Text, it.Label)
			if it.Badge != nil {
				fmt.Fprintf(&b, " [%s %s]", it.Badge.Text, it.Badge.Tone)
			}
			for _, st := range it.Stats {
				fmt.Fprintf(&b, " {%s=%s %s %s}", st.Label, st.Value, st.Detail, st.Tone)
			}
			for _, se := range it.Series {
				fmt.Fprintf(&b, " <%s %d>", se.Name, len(se.Points))
			}
			if it.Action != nil {
				fmt.Fprintf(&b, " →%s", it.Action.ID)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// checkView holds a view to what the apps read (the SDK's schema and limits), full size.
func checkView(t *testing.T, v pluginkit.View) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := exttest.CheckView(raw, false); err != nil {
		t.Fatalf("the hub would refuse the view: %v\n%s", err, texts(v))
	}
}

func req(state, action string, values map[string]string, uiVersion int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"contribution": "watchlist", "state": state, "action": action, "values": values,
		"ui_version": uiVersion, "tz": "Asia/Shanghai"})
	return raw
}

func render(t *testing.T, p *plugin, state string, uiVersion int) pluginkit.View {
	t.Helper()
	out, err := p.uiRender(context.Background(), nil, req(state, "", nil, uiVersion))
	if err != nil {
		t.Fatal(err)
	}
	v := out.(pluginkit.View)
	checkView(t, v)
	return v
}

func act(t *testing.T, p *plugin, state, action string, values map[string]string) pluginkit.ActionResult {
	t.Helper()
	out, err := p.uiAction(context.Background(), nil, req(state, action, values, 5))
	if err != nil {
		t.Fatal(err)
	}
	r := out.(pluginkit.ActionResult)
	if r.View == nil {
		t.Fatalf("%s: no view", action)
	}
	checkView(t, *r.View)
	return r
}

func TestWatchlistPage(t *testing.T) {
	f := newFakeOKX(t)
	p, dir := newTestPlugin(t, f, `{"watchlist": ["btc", "eth/usdt", "nope-usdt", "BTC-USDT", "bad pair!"]}`)
	if got := refStrings(p.watchlist()); !slices.Equal(got, []string{"okx:BTC-USDT", "okx:ETH-USDT", "okx:NOPE-USDT"}) {
		t.Fatalf("seeded watchlist %v", got)
	}
	v := render(t, p, "", 5)
	got := texts(v)
	for _, want := range []string{"自选 · 3", "row BTC-USDT +2000.00 · 成交 6172.50万 USDT 50000.00", "[+4.17% danger] →open:okx:BTC-USDT",
		"row ETH-USDT -100.00 · ", "[-3.33% positive] →open:okx:ETH-USDT", "24h 涨跌按 24 小时前的价格算。点一行看 K 线",
		"NOPE-USDT 现在查不到这个代码的行情", "segmented   order   →sort", "→refresh"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}
	// with a search box, codes are added through it: no add field; before it the field stays
	if strings.Contains(got, "→add") || strings.Contains(got, "## 大盘") {
		t.Errorf("v5: an add field, or 大盘 with OKX alone:\n%s", got)
	}
	if got := texts(render(t, p, "", 2)); !strings.Contains(got, "→add") {
		t.Errorf("v2 has no add field:\n%s", got)
	}
	if v.RefreshAfter < 30 {
		t.Errorf("refresh_after %d", v.RefreshAfter)
	}

	// a request per instrument (a few are read one by one), and none while they are fresh
	hits := f.hits.Load()
	render(t, p, "", 5)
	if f.hits.Load() != hits {
		t.Errorf("drawn again within %s: %d more requests", quotesFor, f.hits.Load()-hits)
	}

	// add, refuse a missing pair and a malformed one, remove, reorder; all kept in the data directory
	if r := act(t, p, v.State, "add", map[string]string{"inst": " doge "}); r.Toast != "已加入 DOGE-USDT" {
		t.Fatalf("add: %q", r.Toast)
	}
	if r := act(t, p, v.State, "add", map[string]string{"inst": "zzz"}); !strings.Contains(r.Toast, "没有这个代码") {
		t.Fatalf("add a missing pair: %q", r.Toast)
	}
	if r := act(t, p, v.State, "add", map[string]string{"inst": "!!"}); !strings.Contains(r.Toast, "不像") {
		t.Fatalf("add a malformed pair: %q", r.Toast)
	}
	act(t, p, v.State, "remove:NOPE-USDT", nil)
	act(t, p, v.State, "up:ETH-USDT", nil)
	want := []string{"okx:ETH-USDT", "okx:BTC-USDT", "okx:DOGE-USDT"}
	if got := refStrings(p.watchlist()); !slices.Equal(got, want) {
		t.Fatalf("watchlist %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "watchlist.json"))
	var stored []string
	if err != nil || json.Unmarshal(raw, &stored) != nil || !slices.Equal(stored, want) {
		t.Fatalf("stored %s %v", raw, err)
	}

	// a restart reads the stored list, not the configuration
	p2 := newPlugin(t.Logf)
	var ip pluginkit.InitParams
	ip.Config = json.RawMessage(`{"watchlist": ["SOL"]}`)
	ip.Plugin.DataDir = dir
	if err := p2.configure(ip); err != nil || !slices.Equal(refStrings(p2.watchlist()), want) {
		t.Fatalf("after a restart: %v %v", p2.watchlist(), err)
	}
}

func TestInstrumentPage(t *testing.T) {
	f := newFakeOKX(t)
	p, _ := newTestPlugin(t, f, `{"watchlist": ["BTC-USDT", "ETH-USDT"]}`)
	r := act(t, p, "", "open:BTC-USDT", nil)
	got := texts(*r.View)
	for _, want := range []string{"{最新价=50000.00 24h +4.17% danger}", "{24h 成交额=61.73M USDT", "segmented", "K 线 · 1 小时",
		"<收盘价 60>", "<成交量 60>", "{60 根涨跌=", "→down:okx:BTC-USDT", "→remove:okx:BTC-USDT", "→back"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "→up:okx:BTC-USDT") {
		t.Errorf("the first one can move up:\n%s", got)
	}
	var st pageState
	_ = json.Unmarshal([]byte(r.View.State), &st)
	if st.Inst != "okx:BTC-USDT" || st.Bar != "1H" {
		t.Fatalf("state %+v", st)
	}

	// the chart's labels are in the user's time zone, oldest first
	for _, it := range r.View.Sections[1].Items {
		if it.Type == "chart" && it.Kind == "line" {
			pts := it.Series[0].Points
			if pts[0].Label != "1/2 08:00" || pts[0].Value != 48020 || pts[0].Display != "收 48020.00 高 48050.00 低 47960.00" {
				t.Errorf("first point %+v", pts[0])
			}
		}
	}

	// another bar through the segmented control
	r = act(t, p, r.View.State, "bar", map[string]string{"bar": "1D"})
	if got := texts(*r.View); !strings.Contains(got, "K 线 · 1 天") {
		t.Fatalf("1D:\n%s", got)
	}

	// an instrument not in the watchlist offers to add it; one OKX lacks says so
	r = act(t, p, "", "look", map[string]string{"inst": "eth"})
	if got := texts(*r.View); !strings.Contains(got, "→remove:okx:ETH-USDT") {
		t.Fatalf("eth:\n%s", got)
	}
	_, _ = p.removeWatch(Ref{"okx", "ETH-USDT"})
	r = act(t, p, r.View.State, "refresh", nil)
	if got := texts(*r.View); !strings.Contains(got, "→add:okx:ETH-USDT") {
		t.Fatalf("eth outside the watchlist:\n%s", got)
	}
	r = act(t, p, "", "open:FOO-USDT", nil)
	if got := texts(*r.View); !strings.Contains(got, "OKX 上没有这个代码") || !strings.Contains(got, "→back") {
		t.Fatalf("missing pair:\n%s", got)
	}

	// an app before UI version 2: no charts, no stat grids, no segmented
	v := render(t, p, `{"i":"BTC-USDT"}`, 1)
	if got := texts(v); strings.Contains(got, "chart") || strings.Contains(got, "stat_grid") || strings.Contains(got, "segmented") ||
		!strings.Contains(got, "metric") || !strings.Contains(got, "最新价") {
		t.Fatalf("v1:\n%s", got)
	}
}

func TestFailures(t *testing.T) {
	f := newFakeOKX(t)
	p, _ := newTestPlugin(t, f, ``)
	f.fail.Store(true)
	v := render(t, p, "", 5)
	if got := texts(v); !strings.Contains(got, ErrRateLimited.Error()) || !strings.Contains(got, "→refresh") {
		t.Fatalf("rate limited:\n%s", got)
	}
	st, _ := p.cStatus(context.Background(), nil, nil)
	if s := st.(connectorStatus); s.State != "rate_limited" {
		t.Fatalf("status %+v", s)
	}
	f.fail.Store(false)
	act(t, p, "", "refresh", nil)
	st, _ = p.cStatus(context.Background(), nil, nil)
	if s := st.(connectorStatus); s.State != "ok" || s.LastSync == nil {
		t.Fatalf("status after %+v", s)
	}
}

func TestTools(t *testing.T) {
	f := newFakeOKX(t)
	p, _ := newTestPlugin(t, f, `{"tz": "Asia/Shanghai"}`)
	call := func(name string, args any) string {
		raw, _ := json.Marshal(map[string]any{"name": name, "args": args})
		out, err := p.cToolsCall(context.Background(), nil, raw)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out.(map[string]any)["text"].(string)
	}
	got := call("markets_quote", nil) // the watchlist: the default one
	if !strings.Contains(got, "okx:BTC-USDT：最新 50000.00 USDT，24h +4.17%") || !strings.Contains(got, "okx:SOL-USDT：查不到这个代码") {
		t.Fatalf("quote:\n%s", got)
	}
	if got := call("markets_quote", map[string]any{"symbols": []string{"btc-usdt-swap", "??"}}); !strings.Contains(got, "okx:BTC-USDT-SWAP：最新 50010.00") ||
		!strings.Contains(got, "「??」不像") {
		t.Fatalf("quote swap:\n%s", got)
	}
	got = call("markets_candles", map[string]any{"symbol": "BTC", "limit": 3})
	if lines := strings.Split(strings.TrimSpace(got), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[1], "2026-01-02 08:00 48000.00 48050.00 47960.00 48020.00") {
		t.Fatalf("candles:\n%s", got)
	}
	if got := call("markets_candles", map[string]any{"symbol": "BTC", "bar": "3m"}); !strings.Contains(got, "ERR") {
		t.Fatalf("bad bar: %s", got)
	}
	if got := call("nope", nil); !strings.Contains(got, "ERR 没有工具") {
		t.Fatalf("unknown tool: %s", got)
	}
}

func TestSearch(t *testing.T) {
	f := newFakeOKX(t)
	p, _ := newTestPlugin(t, f, `{"watchlist": ["BTC-USDT"]}`)

	// the search box is there for an app that draws it, and a search lists what matches, base coin first
	v := render(t, p, "", 5)
	raw, _ := json.Marshal(v)
	if v.Search == nil || v.Search.Action.ID != "search" || !strings.Contains(string(raw), `"search":{`) {
		t.Fatalf("no search box: %s", raw)
	}
	r := act(t, p, v.State, "search", map[string]string{"q": " btc "})
	got := texts(*r.View)
	var order []string
	for _, it := range r.View.Sections[0].Items {
		if it.Type == "row" {
			order = append(order, it.Title)
		}
	}
	if len(order) != 4 || order[3] != "WBTC-USDT" || !slices.Contains(order, "BTC-USDT-SWAP") || !slices.Contains(order, "BTC-USDC") {
		t.Fatalf("search order %v:\n%s", order, got)
	}
	for _, want := range []string{"搜索「btc」", "现货 · 成交 6172.50万 USDT · 已在自选", "永续合约", "→open:okx:BTC-USDC", "→clear"} {
		if !strings.Contains(got, want) {
			t.Errorf("results have no %q:\n%s", want, got)
		}
	}
	if r.View.Search == nil || r.View.Search.Value != "btc" {
		t.Errorf("the box does not keep the search: %+v", r.View.Search)
	}

	// opening one and going back lands on the results; clearing returns to the watchlist
	r = act(t, p, r.View.State, "open:okx:BTC-USDC", nil)
	if got := texts(*r.View); !strings.Contains(got, "→add:okx:BTC-USDC") {
		t.Fatalf("BTC-USDC:\n%s", got)
	}
	r = act(t, p, r.View.State, "back", nil)
	if got := texts(*r.View); !strings.Contains(got, "搜索「btc」") {
		t.Fatalf("back:\n%s", got)
	}
	r = act(t, p, r.View.State, "clear", nil)
	if got := texts(*r.View); !strings.Contains(got, "自选 · 1") {
		t.Fatalf("clear:\n%s", got)
	}
	r = act(t, p, r.View.State, "search", map[string]string{"q": "zzz"})
	if got := texts(*r.View); !strings.Contains(got, "没有和「zzz」对得上") {
		t.Fatalf("nothing:\n%s", got)
	}

	// an app before UI version 3: no box; the add field searches through a button
	v = render(t, p, "", 2)
	if v.Search != nil || !strings.Contains(texts(v), "button 搜索") {
		t.Fatalf("v2:\n%s", texts(v))
	}
	out, err := p.uiAction(context.Background(), nil, req(v.State, "search", map[string]string{"inst": "doge"}, 2))
	if err != nil || !strings.Contains(texts(*out.(pluginkit.ActionResult).View), "→open:okx:DOGE-USDT") {
		t.Fatalf("v2 search: %v", err)
	}

	// the assistant's tool
	raw, _ = json.Marshal(map[string]any{"name": "markets_search", "args": map[string]any{"query": "btc", "limit": 2}})
	res, err := p.cToolsCall(context.Background(), nil, raw)
	if text := fmt.Sprint(res); err != nil || !strings.Contains(text, "对得上的 2 个") {
		t.Fatalf("tool: %v %v", text, err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"btc": "BTC-USDT", "eth/usdc": "ETH-USDC", " sol_usdt ": "SOL-USDT",
		"btc-usdt-swap": "BTC-USDT-SWAP", "1000SATS": "1000SATS-USDT", "": "", "a-b-c": "", "<x>": ""} {
		got, ok := newOKX("").Normalize(in)
		if want == "" && ok || want != "" && got != want {
			t.Errorf("normalize(%q) = %q %v", in, got, ok)
		}
	}
}

func TestFormat(t *testing.T) {
	for in, want := range map[float64]string{76330.1: "76330.10", 3.50210: "3.5021", 1: "1", 0.00001234: "0.00001234", 0.0053: "0.0053", 0: "0"} {
		if got := fmtFloat(in); got != want {
			t.Errorf("fmtFloat(%v) = %q, want %q", in, got, want)
		}
	}
	if got := fmtCompactFloat(1182265000.19); got != "1.18B" {
		t.Errorf("compact %q", got)
	}
}

func refStrings(rs []Ref) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.String()
	}
	return out
}

// ---- the process protocol, as the hub speaks it ----

type wireMsg struct {
	ID     string           `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  *pluginkit.Error `json:"error"`
}

type pipeHub struct {
	w       io.WriteCloser
	n       int
	mu      sync.Mutex
	answers map[string]chan wireMsg
}

func startProtocol(t *testing.T, p *plugin) *pipeHub {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		_ = pluginkit.Serve(context.Background(), inR, outW, pluginkit.Plugin{ID: "markets", Version: version, Manifest: manifest,
			Contributes: contributes(), Init: func(ip pluginkit.InitParams, _ *pluginkit.Hub) error { return p.configure(ip) }, Handlers: p.handlers()})
		close(done)
	}()
	h := &pipeHub{w: inW, answers: map[string]chan wireMsg{}}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var m wireMsg
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			h.mu.Lock()
			if ch := h.answers[m.ID]; ch != nil {
				ch <- m
			}
			h.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the extension did not stop within 5 s of stdin closing")
		}
		_ = outW.Close()
	})
	return h
}

func (h *pipeHub) call(t *testing.T, method string, params any) wireMsg {
	t.Helper()
	h.mu.Lock()
	h.n++
	id := fmt.Sprintf("h%d", h.n)
	ch := make(chan wireMsg, 1)
	h.answers[id] = ch
	h.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := h.w.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		return m
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no answer", method)
	}
	return wireMsg{}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func TestProtocol(t *testing.T) {
	f := newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = sources{newOKX(f.srv.URL)}
	h := startProtocol(t, p)

	if m := h.call(t, "ping", nil); m.Error == nil || m.Error.Code != pluginkit.CodeNotInitialized {
		t.Fatalf("ping before initialize: %+v", m)
	}
	m := h.call(t, "initialize", map[string]any{"protocol": 1, "config": map[string]any{"bar": "4H"},
		"plugin": map[string]any{"id": "markets", "version": version, "data_dir": t.TempDir()},
		"active": map[string][]string{"connectors": {"markets"}, "ui": {"watchlist"}}})
	if m.Error != nil {
		t.Fatalf("initialize: %+v", m.Error)
	}
	var init struct {
		ID          string          `json:"id"`
		Version     string          `json:"version"`
		Contributes json.RawMessage `json:"contributes"`
	}
	_ = json.Unmarshal(m.Result, &init)
	var want struct {
		Contributes json.RawMessage `json:"contributes"`
	}
	_ = json.Unmarshal(manifest, &want)
	if init.ID != "markets" || init.Version != version || !jsonEqual(init.Contributes, want.Contributes) {
		t.Fatalf("initialize answered %s", m.Result)
	}

	m = h.call(t, "ui.render", map[string]any{"contribution": "watchlist", "state": "", "ui_version": 5})
	var v pluginkit.View
	if m.Error != nil || json.Unmarshal(m.Result, &v) != nil {
		t.Fatalf("ui.render: %s %+v", m.Result, m.Error)
	}
	checkView(t, v)
	m = h.call(t, "ui.action", map[string]any{"contribution": "watchlist", "state": v.State, "action": "open:BTC-USDT", "ui_version": 5})
	var r pluginkit.ActionResult
	if m.Error != nil || json.Unmarshal(m.Result, &r) != nil || r.View == nil || !strings.Contains(texts(*r.View), "K 线 · 4 小时") {
		t.Fatalf("ui.action: %s %+v", m.Result, m.Error)
	}

	m = h.call(t, "connector.tools.list", map[string]any{"contribution": "markets"})
	var tl struct {
		Tools []Tool `json:"tools"`
	}
	_ = json.Unmarshal(m.Result, &tl)
	if len(tl.Tools) != 3 || tl.Tools[0].Name != "markets_quote" || tl.Tools[1].Name != "markets_search" || tl.Tools[2].Name != "markets_candles" {
		t.Fatalf("tools: %s", m.Result)
	}
	m = h.call(t, "connector.status", map[string]any{"contribution": "markets"})
	var st connectorStatus
	if json.Unmarshal(m.Result, &st) != nil || st.State != "ok" || st.LastSync == nil {
		t.Fatalf("status: %s", m.Result)
	}
	for _, c := range []struct{ method, contribution string }{{"connector.status", "github"}, {"ui.render", "panel"}} {
		if m := h.call(t, c.method, map[string]any{"contribution": c.contribution}); m.Error == nil || m.Error.Code != pluginkit.CodeNoContribution {
			t.Fatalf("%s for %s: %+v", c.method, c.contribution, m)
		}
	}
	var answered map[string]any
	_ = json.Unmarshal(manifest, &answered)
	delete(answered, "tags") // the market's: hubs from before them refuse the field (nwverse/persona#2424)
	answeredRaw, _ := json.Marshal(answered)
	if m := h.call(t, "manifest", nil); !jsonEqual(m.Result, answeredRaw) {
		t.Fatal("manifest is not persona-extension.json without its tags")
	}
}

// TestManifest holds persona-extension.json to the code: what it contributes is what it answers, its settings are
// Config, its bars are the page's.
func TestManifest(t *testing.T) {
	var m struct {
		ID          string `json:"id"`
		Contributes struct {
			Connectors []struct {
				ID           string   `json:"id"`
				Capabilities []string `json:"capabilities"`
				Hosts        []string `json:"hosts"`
			} `json:"connectors"`
			UI []struct {
				ID        string `json:"id"`
				Placement string `json:"placement"`
			} `json:"ui"`
		} `json:"contributes"`
		Configuration struct {
			Properties map[string]struct {
				OneOf []struct {
					Const string `json:"const"`
				} `json:"oneOf"`
			} `json:"properties"`
		} `json:"configuration"`
		Runtime struct {
			Command map[string]string `json:"command"`
		} `json:"runtime"`
		Permissions struct {
			Network []string `json:"network"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "markets" || len(m.Contributes.Connectors) != 1 || m.Contributes.Connectors[0].ID != "markets" ||
		len(m.Contributes.UI) != 2 || m.Contributes.UI[0].ID != "watchlist" || m.Contributes.UI[0].Placement != "nav_page" ||
		m.Contributes.UI[1].ID != "card" || m.Contributes.UI[1].Placement != "overview_card" {
		t.Fatalf("manifest: %+v", m)
	}
	h := newPlugin(t.Logf).handlers()
	for capability, method := range map[string]string{"status": "connector.status", "tools": "connector.tools.list", "inbox": "connector.inbox.collect"} {
		if slices.Contains(m.Contributes.Connectors[0].Capabilities, capability) != (h[method] != nil) {
			t.Errorf("capability %s and %s disagree", capability, method)
		}
	}
	var keys []string
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		keys = append(keys, strings.Split(ct.Field(i).Tag.Get("json"), ",")[0])
	}
	for key := range m.Configuration.Properties {
		if !slices.Contains(keys, key) {
			t.Errorf("configuration %s is not in Config", key)
		}
	}
	var barIDs, offered []string
	for _, b := range allBars {
		barIDs = append(barIDs, b.id)
	}
	for _, o := range m.Configuration.Properties["bar"].OneOf {
		offered = append(offered, o.Const)
	}
	if !slices.Equal(barIDs, offered) {
		t.Errorf("the bar setting offers %v, the page has %v", offered, barIDs)
	}
	for _, src := range newSources() {
		for _, host := range src.Hosts() {
			if !slices.Contains(m.Permissions.Network, host) || !slices.Contains(m.Contributes.Connectors[0].Hosts, host) {
				t.Errorf("source %s reaches %s: not in the manifest's network permission and connector hosts", src.ID(), host)
			}
		}
	}
	for _, plat := range []string{"linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64"} {
		if m.Runtime.Command[plat] != "bin/markets-"+plat {
			t.Errorf("runtime for %s: %q", plat, m.Runtime.Command[plat])
		}
	}
}

// Every ui contribution in the manifest is served by the handlers; another is refused.
func TestUIContributions(t *testing.T) {
	var m struct {
		Contributes struct {
			UI []struct {
				ID string `json:"id"`
			} `json:"ui"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	h := newPlugin(t.Logf).handlers()
	for _, ui := range m.Contributes.UI {
		raw, _ := json.Marshal(map[string]any{"contribution": ui.ID, "ui_version": 5})
		if _, err := h["ui.render"](context.Background(), nil, raw); err != nil && strings.Contains(err.Error(), "no contribution") {
			t.Errorf("ui %s: %v", ui.ID, err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"contribution": "nope", "ui_version": 5})
	if _, err := h["ui.render"](context.Background(), nil, raw); err == nil {
		t.Error("an unknown contribution is drawn")
	}
}
