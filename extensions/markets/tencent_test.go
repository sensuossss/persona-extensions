package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/exttest"
)

// fakeTencent answers like Tencent's endpoints from the samples in testdata/tencent, recorded from the live ones:
// quotes for the codes asked that the sample has, the search for "tengxun", and the bars there are files for.
type fakeTencent struct {
	srv    *httptest.Server
	mu     sync.Mutex
	asked  []string // every request, path and query
	quotes map[string][]byte
}

func newFakeTencent(t *testing.T) *fakeTencent {
	t.Helper()
	f := &fakeTencent{quotes: map[string][]byte{}}
	raw, err := os.ReadFile("testdata/tencent/quotes.gbk")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(raw, []byte(";")) {
		line = bytes.TrimSpace(line)
		if k, _, ok := bytes.Cut(line, []byte("=")); ok {
			f.quotes[strings.TrimPrefix(string(k), "v_")] = append(line, ";\n"...)
		}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.RequestURI())
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/q="):
			var out []byte
			for _, c := range strings.Split(strings.TrimPrefix(r.URL.Path, "/q="), ",") {
				out = append(out, f.quotes[c]...)
			}
			if len(out) == 0 {
				out = []byte(`v_pv_none_match="1";` + "\n")
			}
			_, _ = w.Write(out)
		case r.URL.Path == "/s3/":
			name := "search-none.txt"
			if r.URL.Query().Get("q") == "tengxun" {
				name = "search-tengxun.txt"
			}
			serveFile(t, w, name)
		case r.URL.Path == "/appstock/app/minute/query":
			name := "minute-" + r.URL.Query().Get("code") + ".json"
			if _, err := os.Stat(filepath.Join("testdata/tencent", name)); err != nil {
				_, _ = w.Write([]byte(`{"code":-1,"msg":"param error","data":[]}`))
				return
			}
			serveFile(t, w, name)
		case r.URL.Path == "/appstock/app/fqkline/get", r.URL.Path == "/appstock/app/kline/mkline":
			p := strings.Split(r.URL.Query().Get("param"), ",")
			prefix := "kline-"
			if strings.HasSuffix(r.URL.Path, "mkline") {
				prefix = "mkline-"
			}
			name := prefix + p[0] + "-" + p[1] + ".json"
			if _, err := os.Stat(filepath.Join("testdata/tencent", name)); err != nil {
				_, _ = w.Write([]byte(`{"code":-1,"msg":"param error","data":[]}`))
				return
			}
			serveFile(t, w, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func serveFile(t *testing.T, w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join("testdata/tencent", name))
	if err != nil {
		t.Error(err)
	}
	_, _ = w.Write(b)
}

func (f *fakeTencent) hosts() tencentHosts {
	return tencentHosts{quote: f.srv.URL, search: f.srv.URL, kline: f.srv.URL, mkline: f.srv.URL}
}

func (f *fakeTencent) requests(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func txSources(t *testing.T) (*fakeTencent, Source, Source, Source) {
	f := newFakeTencent(t)
	ss := tencentSources(f.hosts())
	return f, ss[0], ss[1], ss[2]
}

func TestTencentNormalize(t *testing.T) {
	_, cn, hk, us := txSources(t)
	for _, c := range []struct {
		src  Source
		in   string
		want string // "" when it cannot be one
	}{
		{cn, "600519", "600519.SH"}, {cn, "sh600519", "600519.SH"}, {cn, "600519.sh", "600519.SH"},
		{cn, "000001", "000001.SZ"}, {cn, "000001.SH", "000001.SH"}, {cn, "300750", "300750.SZ"},
		{cn, "920000", "920000.BJ"}, {cn, "430047", "430047.BJ"}, {cn, "510300", "510300.SH"},
		{cn, "SH600519.SZ", ""}, {cn, "60051", ""}, {cn, "AAPL", ""},
		{hk, "700", "00700"}, {hk, "00700", "00700"}, {hk, "hk00700", "00700"}, {hk, "0700.HK", "00700"},
		{hk, "hsi", "HSI"}, {hk, "123456", ""}, {hk, "腾讯", ""},
		{us, "aapl", "AAPL"}, {us, "AAPL.OQ", "AAPL"}, {us, "brk.b.n", "BRK.B"}, {us, "BRK-B", "BRK.B"},
		{us, ".dji", ".DJI"}, {us, "tcehy.ps", "TCEHY"}, {us, "BTC-USDT", ""}, {us, "", ""},
	} {
		got, ok := c.src.Normalize(c.in)
		if c.want == "" && ok || c.want != "" && got != c.want {
			t.Errorf("%s Normalize(%q) = %q %v, want %q", c.src.ID(), c.in, got, ok, c.want)
		}
	}
}

func TestTencentQuotes(t *testing.T) {
	f, cn, hk, us := txSources(t)
	ctx := context.Background()
	qs, err := cn.Quotes(ctx, []string{"600519.SH", "000001.SH", "300750.SZ", "600000.SH"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m := qs["600519.SH"]
	if m.Name != "贵州茅台" || m.Kind != "股票" || m.Currency != "CNY" || m.Last != 1440.49 || m.Base != 1452 ||
		m.High != 1457.72 || m.Low != 1436 || m.Turnover < 3e9 || m.Turnover > 4e9 || m.Period != "当日" {
		t.Errorf("600519: %+v", m)
	}
	if m.At.Location() != marketInfo[mCN].loc || m.At.Format("2006-01-02 15") != "2026-11-06 11" {
		t.Errorf("600519 at %v", m.At)
	}
	if qs["000001.SH"].Kind != "指数" || qs["000001.SH"].Name != "上证指数" {
		t.Errorf("index: %+v", qs["000001.SH"])
	}
	if _, ok := qs["600000.SH"]; ok || len(qs) != 3 {
		t.Errorf("one the sample lacks is missing: %v", qs)
	}
	if got := f.requests("/q=sh600519,sh000001,sz300750,sh600000"); got != 1 {
		t.Errorf("one request for the batch, got %d: %v", got, f.asked)
	}

	h, err := hk.Quote(ctx, "00700", time.Minute)
	if err != nil || h.Name != "腾讯控股" || h.Currency != "HKD" || h.Kind != "股票" || h.Turnover < 1e9 || h.At.Location() != marketInfo[mHK].loc {
		t.Errorf("00700: %+v %v", h, err)
	}
	if f.requests("/q=r_hk00700") != 1 {
		t.Errorf("Hong Kong in real time (r_): %v", f.asked)
	}
	if x, err := hk.Quote(ctx, "HSI", time.Minute); err != nil || x.Kind != "指数" {
		t.Errorf("HSI: %+v %v", x, err)
	}

	u, err := us.Quotes(ctx, []string{"AAPL", "BRK.B", ".DJI", "ZZZQQQ"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a := u["AAPL"]; a.Name != "苹果" || a.Currency != "USD" || a.Last != 344.17 || a.Base != 349.51 || a.Kind != "股票" ||
		!a.At.Equal(time.Date(2026, 11, 5, 16, 0, 2, 0, marketInfo[mUS].loc)) {
		t.Errorf("AAPL: %+v", a)
	}
	if u[".DJI"].Kind != "指数" || u["BRK.B"].Name != "伯克希尔B" || len(u) != 3 {
		t.Errorf("us: %+v", u)
	}
	if _, err := us.Quote(ctx, "ZZZQQQ", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if got := us.URL("AAPL"); got != "https://gu.qq.com/usAAPL.OQ" {
		t.Errorf("url %s", got)
	}
	if got := cn.URL("600519.SH"); got != "https://gu.qq.com/sh600519" {
		t.Errorf("url %s", got)
	}
}

func TestTencentSearch(t *testing.T) {
	f, cn, hk, us := txSources(t)
	ctx := context.Background()
	h, err := hk.Search(ctx, "tengxun", 5, time.Minute)
	if err != nil || len(h) != 1 || h[0].Symbol != "00700" || h[0].Name != "腾讯控股" {
		t.Errorf("hk search: %+v %v", h, err) // the others are not in the quote sample
	}
	if _, err := cn.Search(ctx, "tengxun", 5, time.Minute); err != nil {
		t.Error(err)
	}
	if _, err := us.Search(ctx, "tengxun", 5, time.Minute); err != nil {
		t.Error(err)
	}
	if n := f.requests("/s3/"); n != 1 {
		t.Errorf("the three markets share one search answer: %d requests", n)
	}
	// each market asks quotes for its own hits only, warrants (QZ) left out
	for _, want := range []string{"/q=sh000847", "/q=r_hk00700,r_hk80700,r_hk01698", "/q=usTCEHY,usTCTZF,usTME"} {
		if f.requests(want) != 1 {
			t.Errorf("no request %s: %v", want, f.asked)
		}
	}
	if got, err := cn.Search(ctx, "zzzzqqq", 5, time.Minute); err != nil || len(got) != 0 {
		t.Errorf("nothing found: %v %v", got, err)
	}
}

func TestTencentCandles(t *testing.T) {
	_, cn, hk, us := txSources(t)
	ctx := context.Background()
	d, err := cn.Candles(ctx, "600519.SH", "1D", 3, time.Minute)
	if err != nil || len(d) != 3 {
		t.Fatalf("day: %+v %v", d, err)
	}
	// 2026-11-05: open 1446.7, close 1452, high 1466, low 1430, 46712 lots
	if c := d[1]; !c.At.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, marketInfo[mCN].loc)) || c.Open != 1446.7 || c.Close != 1452 ||
		c.High != 1466 || c.Low != 1430 || c.Vol != 4671200 {
		t.Errorf("day bar: %+v", c)
	}
	h, err := cn.Candles(ctx, "600519.SH", "1H", 3, time.Minute)
	if err != nil || len(h) != 3 {
		t.Fatalf("hour: %+v %v", h, err)
	}
	if !h[0].At.Equal(time.Date(2026, 11, 5, 14, 0, 0, 0, marketInfo[mCN].loc)) || !h[1].At.Equal(time.Date(2026, 11, 6, 9, 30, 0, 0, marketInfo[mCN].loc)) {
		t.Errorf("hourly bars start an hour before Tencent's stamp: %v %v", h[0].At, h[1].At)
	}

	// US bars need the exchange, which a quote tells first
	w, err := us.Candles(ctx, "AAPL", "1W", 3, time.Minute)
	if err != nil || len(w) != 3 {
		t.Fatalf("week: %+v %v", w, err)
	}
	if !w[0].At.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, marketInfo[mUS].loc)) || !w[2].At.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, marketInfo[mUS].loc)) ||
		w[2].Vol != 293234411 {
		t.Errorf("weekly bars start on the Monday: %+v", w)
	}

	if _, err := hk.Candles(ctx, "00700", "15m", 3, time.Minute); err == nil || !strings.Contains(err.Error(), "没有") {
		t.Errorf("Hong Kong has no minute bars: %v", err)
	}
	if _, err := hk.Candles(ctx, "00700", "1D", 3, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("a code Tencent answers param error for: %v", err)
	}
	if !slices.Equal(cn.Bars(), []string{"T", "15m", "1H", "1D", "1W"}) || !slices.Equal(us.Bars(), []string{"T", "1D", "1W"}) {
		t.Errorf("bars %v %v", cn.Bars(), us.Bars())
	}
}

func TestTencentInPlugin(t *testing.T) {
	ft := newFakeTencent(t)
	fo := newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	if r, ok := p.srcs.parse("cn:600519"); !ok || r.String() != "cn:600519.SH" {
		t.Errorf("parse %v %v", r, ok)
	}
	if got := p.srcs.markets(); got != "加密货币（OKX）、A股、港股、美股" {
		t.Errorf("markets %q", got)
	}
	r := act(t, p, "", "open:cn:600519.SH", nil)
	got := texts(*r.View)
	for _, want := range []string{"当日 成交额=3.", "CNY", "K 线 · 1 小时", "→add:cn:600519.SH", "link 在 腾讯证券 打开"} {
		if !strings.Contains(got, want) {
			t.Errorf("the A-share page has no %q:\n%s", want, got)
		}
	}
	if r.View.Title != "600519.SH 贵州茅台" {
		t.Errorf("title %q", r.View.Title)
	}
	var link string
	for _, s := range r.View.Sections {
		for _, it := range s.Items {
			if it.Type == "link" {
				link = it.URL
			}
		}
	}
	if link != "https://gu.qq.com/sh600519" {
		t.Errorf("link %q", link)
	}
}

func TestLayoutGroups(t *testing.T) {
	ft, fo := newFakeTencent(t), newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	defer func(old func() time.Time) { now = old }(now)
	// 2026-11-06 11:40 in Shanghai: A-shares and Hong Kong are trading, the US closed the day before
	now = func() time.Time { return time.Date(2026, 11, 6, 11, 40, 0, 0, marketInfo[mCN].loc) }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"),
		[]byte(`["us:AAPL","okx:BTC-USDT","cn:600519.SH","hk:00700","us:.DJI","cn:000001.SH","okx:ETH-USDT"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	v := render(t, p, "", 5)
	var titles []string
	for _, s := range v.Sections {
		titles = append(titles, s.Title)
	}
	if !slices.Equal(titles[:6], []string{"大盘", "", "加密货币 · 2", "A股 · 2", "港股 · 1", "美股 · 2"}) {
		t.Fatalf("groups %q", titles)
	}
	got := texts(v)
	for _, want := range []string{
		"row 贵州茅台 600519.SH · -11.51 · 成交 35.57亿 CNY 1440.49   [-0.79% positive]",
		"row 苹果 AAPL · -5.34 · 成交 262.40亿 USD · 休市 · 截至 11/5 16:00 344.17",
		"## A股 · 2 | 涨跌按上个交易日的收盘价算，时间是交易所当地的",
		"## 美股 · 2 | 涨跌按上个交易日的收盘价算，时间是交易所当地的。点一行看 K 线"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}
	if v.Search == nil || v.Search.Placeholder != "搜代码或名称：BTC、茅台、腾讯、AAPL" {
		t.Errorf("search box %+v", v.Search)
	}
	if strings.Contains(got, "腾讯控股 00700 · -7.40 · 成交 67.96亿 HKD · 休市") || !strings.Contains(got, "腾讯控股 00700 · -7.40") {
		t.Errorf("Hong Kong is trading:\n%s", got)
	}

	// moves stay within the group: 000001.SH up passes over the others to 600519.SH
	act(t, p, v.State, "up:cn:000001.SH", nil)
	if got := refStrings(p.watchlist()); !slices.Equal(got, []string{"us:AAPL", "okx:BTC-USDT", "cn:000001.SH", "hk:00700", "us:.DJI", "cn:600519.SH", "okx:ETH-USDT"}) {
		t.Errorf("moved %v", got)
	}
	r := act(t, p, "", "open:cn:000001.SH", nil)
	if s := texts(*r.View); strings.Contains(s, "→up:") || !strings.Contains(s, "→down:cn:000001.SH") {
		t.Errorf("first of its group: no up, a down:\n%s", s)
	}

	// a source that fails says so in its group; the others draw
	ft.srv.Close()
	got = texts(*act(t, p, "", "refresh", nil).View) // past the cache
	if !strings.Contains(got, "row BTC-USDT") || !strings.Contains(got, "连不上腾讯行情") || !strings.Contains(got, "## 加密货币 · 2") {
		t.Errorf("one source down:\n%s", got)
	}

	// green_up turns the tones round
	ip.Config = []byte(`{"colors": "green_up"}`)
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	if got := texts(render(t, p, "", 5)); !strings.Contains(got, "[+4.17% positive] →open:okx:BTC-USDT") {
		t.Errorf("green_up:\n%s", got)
	}
}

func TestDecimals(t *testing.T) {
	for _, c := range []struct {
		m    market
		kind string
		last float64
		want string
	}{{mCN, "股票", 1440.5, "1440.50"}, {mCN, "ETF", 4.135, "4.135"}, {mHK, "股票", 423, "423.00"}, {mHK, "股票", 0.315, "0.315"},
		{mUS, "股票", 0.4567, "0.4567"}, {mUS, "指数", 51523.06, "51523.06"}} {
		q := Quote{Decimals: decimals(c.m, c.kind, c.last)}
		if got := q.Price(c.last); got != c.want {
			t.Errorf("%v %s %v: %s, want %s", c.m, c.kind, c.last, got, c.want)
		}
	}
	if got := (Quote{}).Price(3.50210); got != "3.5021" {
		t.Errorf("auto: %s", got)
	}
}

func TestFactsAndMinutes(t *testing.T) {
	_, cn, hk, us := txSources(t)
	ctx := context.Background()
	q, err := cn.Quote(ctx, "600519.SH", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{}
	var order []string
	for _, f := range q.Facts {
		facts[f.Label] = f.Value
		order = append(order, f.Label)
	}
	for label, want := range map[string]string{"今开": "1450.00", "昨收": "1452.00", "涨停": "1597.20", "跌停": "1306.80",
		"52 周最高": "1539.98", "52 周最低": "1151.01", "市盈率": "21.92", "换手率": "0.20%"} {
		if facts[label] != want {
			t.Errorf("600519 %s = %q, want %q (%v)", label, facts[label], want, q.Facts)
		}
	}
	if !strings.HasSuffix(facts["总市值"], "万亿 CNY") || !strings.HasSuffix(facts["成交量"], "万") || order[0] != "今开" {
		t.Errorf("600519 facts %v", q.Facts)
	}
	idx, _ := cn.Quote(ctx, "000001.SH", time.Minute)
	for _, f := range idx.Facts {
		if f.Label == "市盈率" || f.Label == "涨停" || f.Label == "总市值" {
			t.Errorf("an index has no %s", f.Label)
		}
	}
	h, _ := hk.Quote(ctx, "00700", time.Minute)
	if len(h.Facts) == 0 || h.Facts[len(h.Facts)-1].Label != "52 周最低" {
		t.Errorf("00700 facts %v", h.Facts)
	}

	// the minute line: A-shares 121 minutes gathered two a point, from 9:30; volumes in shares
	m, err := cn.Candles(ctx, "600519.SH", "T", 0, time.Minute)
	if err != nil || len(m) != 61 {
		t.Fatalf("minutes %d %v", len(m), err)
	}
	if !m[0].At.Equal(time.Date(2026, 11, 6, 9, 30, 0, 0, marketInfo[mCN].loc)) || m[0].Open != 1450 || m[0].Close != 1450.98 || m[0].Vol != 446500 {
		t.Errorf("first point %+v", m[0])
	}
	if m[1].Vol <= 0 || !m[1].At.Equal(m[0].At.Add(2*time.Minute)) {
		t.Errorf("second point %+v", m[1])
	}
	if hm, err := hk.Candles(ctx, "00700", "T", 0, time.Minute); err != nil || len(hm) == 0 || len(hm) > minutePoints {
		t.Errorf("hk minutes %d %v", len(hm), err)
	}
	// before the US day's first trade the line is empty
	if um, err := us.Candles(ctx, "AAPL", "T", 0, time.Minute); err != nil || len(um) != 0 {
		t.Errorf("us minutes %v %v", um, err)
	}
}

func TestBatchOnePage(t *testing.T) {
	ft, fo := newFakeTencent(t), newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return time.Date(2026, 11, 6, 11, 40, 0, 0, marketInfo[mCN].loc) }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"), []byte(`["cn:600519.SH","us:AAPL","cn:300750.SZ","cn:000001.SH"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}

	// the instrument page: the facts in grids of at most six, the minute line offered
	r := act(t, p, "", "open:cn:000001.SH", nil)
	got := texts(*r.View)
	for _, want := range []string{"{今开=", "{52 周最高=", "→top:cn:000001.SH", "→up:cn:000001.SH"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}
	r = act(t, p, `{"i":"cn:600519.SH"}`, "bar", map[string]string{"bar": "T"})
	if got := texts(*r.View); !strings.Contains(got, "K 线 · 今日分时") || !strings.Contains(got, "开盘以来") || !strings.Contains(got, "<收盘价 61>") {
		t.Errorf("the minute line:\n%s", got)
	}
	// 置顶 moves it to the top of its group, over the others of its source only
	act(t, p, `{"i":"cn:000001.SH"}`, "top:cn:000001.SH", nil)
	if got := refStrings(p.watchlist()); !slices.Equal(got, []string{"cn:000001.SH", "us:AAPL", "cn:600519.SH", "cn:300750.SZ"}) {
		t.Errorf("top %v", got)
	}
	// a US stock before its day: the minute line says so; a closed market's page refreshes slowly
	r = act(t, p, `{"i":"us:AAPL","b":"T"}`, "refresh", nil)
	if got := texts(*r.View); !strings.Contains(got, "还没有分时") || r.View.RefreshAfter != refreshIdle {
		t.Errorf("us before the day (refresh %d):\n%s", r.View.RefreshAfter, got)
	}

	// added from a search's result: back to the search, the row marked
	r = act(t, p, "", "search", map[string]string{"q": "tengxun"})
	r = act(t, p, r.View.State, "open:hk:00700", nil)
	r = act(t, p, r.View.State, "add:hk:00700", nil)
	if r.Toast != "已加入 00700" || r.View.Title != "行情" || !strings.Contains(texts(*r.View), "已在自选") {
		t.Errorf("after adding from a search: %q\n%s", r.Toast, texts(*r.View))
	}

	// the watchlist refreshes as usual while a market trades, slowly when none does
	if v := render(t, p, "", 5); v.RefreshAfter != refreshEvery {
		t.Errorf("A-shares trading: refresh %d", v.RefreshAfter)
	}
	now = func() time.Time { return time.Date(2026, 11, 6, 20, 0, 0, 0, marketInfo[mCN].loc) }
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...) // a cache from before
	if v := render(t, p, "", 5); v.RefreshAfter != refreshIdle {
		t.Errorf("every market closed: refresh %d", v.RefreshAfter)
	}
}

func TestCard(t *testing.T) {
	ft, fo := newFakeTencent(t), newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"), []byte(`["cn:600519.SH","us:.DJI","BTC-USDT","hk:00700"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	draw := func() (pluginkit.View, error) {
		raw, _ := json.Marshal(map[string]any{"contribution": "card", "placement": "overview_card", "size": "compact", "ui_version": 5})
		out, err := p.handlers()["ui.render"](context.Background(), nil, raw) // as the hub calls it
		if err != nil {
			return pluginkit.View{}, err
		}
		return out.(pluginkit.View), nil
	}
	v, err := draw()
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(v); exttest.CheckView(raw, true) != nil { // a card: compact limits
		t.Fatalf("the hub would refuse the card: %v", exttest.CheckView(raw, true))
	}
	got := texts(v)
	// the benchmarks: BTC, then the sample's 上证指数 and 恒生指数 (纳斯达克 is not in it)
	for _, want := range []string{"{BTC=50000.00 +4.17% danger}", "{上证指数=3649.15 -1.10% positive}", "{恒生指数=",
		"## 自选里动得最多的", "row BTC-USDT"} {
		if !strings.Contains(got, want) {
			t.Errorf("the card has no %q:\n%s", want, got)
		}
	}
	// the biggest move first: BTC +4.17%, then .DJI -3.44%
	rows := v.Sections[1].Items
	if len(rows) != 4 || rows[0].Title != "BTC-USDT" || rows[1].Title != "道琼斯" || rows[0].Action != nil {
		t.Errorf("moves %+v", rows)
	}

	// off: unsupported, which the apps draw as nothing
	ip.Config = []byte(`{"overview_card": false}`)
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	_, err = draw()
	var pe *pluginkit.Error
	if !errors.As(err, &pe) || pe.Code != pluginkit.CodeUnsupported {
		t.Errorf("off: %v", err)
	}
}

func TestBatchTwoPage(t *testing.T) {
	ft, fo := newFakeTencent(t), newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"),
		[]byte(`["cn:600519.SH","BTC-USDT","cn:300750.SZ","ETH-USDT","cn:000001.SH","us:AAPL"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	rowTitles := func(v pluginkit.View, group string) []string {
		var out []string
		for _, s := range v.Sections {
			if strings.HasPrefix(s.Title, group) {
				for _, it := range s.Items {
					out = append(out, it.Title)
				}
			}
		}
		return out
	}

	// 大盘 first, then the controls: the order, and the markets the watchlist has
	v := render(t, p, "", 5)
	checkView(t, v)
	if v.Sections[0].Title != "大盘" || len(v.Sections[0].Items[0].Stats) != 3 {
		t.Fatalf("大盘: %s", texts(v))
	}
	ctl := v.Sections[1].Items
	if len(ctl) != 2 || ctl[0].Name != "sort" || ctl[1].Name != "mkt" || len(ctl[1].Choices) != 4 || ctl[1].Choices[3].Title != "美股" {
		t.Fatalf("controls: %+v", ctl)
	}

	// the biggest fall first within each group: 宁德时代 -3.68%, 上证指数 -1.10%, 贵州茅台 -0.79%
	r := act(t, p, v.State, "sort", map[string]string{"sort": "down"})
	if got := rowTitles(*r.View, "A股"); !slices.Equal(got, []string{"宁德时代", "上证指数", "贵州茅台"}) {
		t.Errorf("by fall: %v", got)
	}
	if got := rowTitles(*r.View, "加密货币"); !slices.Equal(got, []string{"ETH-USDT", "BTC-USDT"}) {
		t.Errorf("crypto by fall: %v", got)
	}
	r = act(t, p, r.View.State, "sort", map[string]string{"sort": "up"})
	if got := rowTitles(*r.View, "A股"); !slices.Equal(got, []string{"贵州茅台", "上证指数", "宁德时代"}) {
		t.Errorf("by rise: %v", got)
	}
	// one market only; the order kept in the state; the stored order untouched
	r = act(t, p, r.View.State, "mkt", map[string]string{"mkt": "cn"})
	var groups []string
	for _, s := range r.View.Sections {
		if strings.Contains(s.Title, " · ") {
			groups = append(groups, s.Title)
		}
	}
	if !slices.Equal(groups, []string{"A股 · 3"}) || !strings.Contains(r.View.State, `"s":"up"`) {
		t.Errorf("filtered: %v %s", groups, r.View.State)
	}
	if got := refStrings(p.watchlist()); got[0] != "cn:600519.SH" || got[2] != "cn:300750.SZ" {
		t.Errorf("stored order changed: %v", got)
	}
	r = act(t, p, r.View.State, "mkt", map[string]string{"mkt": "all"})
	if strings.Contains(r.View.State, `"m"`) {
		t.Errorf("all markets: %s", r.View.State)
	}

	// a query that is itself a code comes first as its own row (the search box being the way to add one)
	r = act(t, p, "", "search", map[string]string{"q": "cn:300750"})
	got := texts(*r.View)
	if r.View.Sections[0].Title != "代码 cn:300750.SZ" || !strings.Contains(got, "row 宁德时代 300750.SZ · 股票") {
		t.Errorf("a code searched:\n%s", got)
	}
}

func TestMatchRank(t *testing.T) {
	_, cn, hk, _ := txSources(t)
	okxSrc := newOKX("")
	for _, c := range []struct {
		q    string
		src  Source
		h    hit
		want int
	}{
		{"700", hk, hit{Ref{"hk", "00700"}, Quote{Name: "腾讯控股"}}, 0},
		{"腾讯控股", hk, hit{Ref{"hk", "00700"}, Quote{Name: "腾讯控股"}}, 0},
		{"腾讯", hk, hit{Ref{"hk", "00700"}, Quote{Name: "腾讯控股"}}, 1},
		{"腾讯", cn, hit{Ref{"cn", "000847.SH"}, Quote{Name: "腾讯济安"}}, 1},
		{"tengxun", hk, hit{Ref{"hk", "00700"}, Quote{Name: "腾讯控股"}}, 2},
		{"btc", okxSrc, hit{Ref{"okx", "BTC-USDT"}, Quote{}}, 0},
		{"茅台", okxSrc, hit{Ref{"okx", "MT-USDT"}, Quote{}}, 3},
	} {
		if got := matchRank(c.q, c.src, c.h); got != c.want {
			t.Errorf("matchRank(%q, %s) = %d, want %d", c.q, c.h.Ref, got, c.want)
		}
	}
}

func TestOKXFewOrMany(t *testing.T) {
	f := newFakeOKX(t)
	o := newOKX(f.srv.URL)
	ctx := context.Background()
	before := f.hits.Load()
	qs, err := o.Quotes(ctx, []string{"BTC-USDT", "ETH-USDT", "NOPE-USDT"}, time.Minute)
	if err != nil || len(qs) != 2 || f.hits.Load()-before != 3 {
		t.Fatalf("few: %v %v, %d requests", qs, err, f.hits.Load()-before)
	}
	// the missing one is remembered while fresh
	before = f.hits.Load()
	if _, err := o.Quotes(ctx, []string{"NOPE-USDT"}, time.Minute); err != nil || f.hits.Load() != before {
		t.Errorf("missing asked again: %v, %d requests", err, f.hits.Load()-before)
	}
	// more than a few: the whole list, one request
	before = f.hits.Load()
	ids := []string{"BTC-USDT", "ETH-USDT", "DOGE-USDT", "WBTC-USDT", "BTC-USDC", "NOPE-USDT"}
	if qs, err := o.Quotes(ctx, ids, time.Minute); err != nil || len(qs) != 5 || f.hits.Load()-before != 1 {
		t.Errorf("many: %d %v, %d requests", len(qs), err, f.hits.Load()-before)
	}
}
