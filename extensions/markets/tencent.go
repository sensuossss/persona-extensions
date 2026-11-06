package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the markets' time zones on a hub machine without a zoneinfo database

	"golang.org/x/text/encoding/simplifiedchinese"
)

// The Tencent sources: A-shares (cn), Hong Kong (hk) and US (us) stocks, ETFs and indices, from the endpoints
// Tencent's stock pages (gu.qq.com) read, no key. They are not a documented API: the formats here were read off
// live answers (testdata/tencent holds samples) and may change without notice; no rate limit is published, so
// answers are cached like OKX's. All three markets share one client and its cache.
//
//   - quotes: qt.gtimg.cn/q=sh600519,r_hk00700,usAAPL (GBK, one "~"-separated line per symbol; r_ is Hong Kong
//     in real time, without it 15 minutes late)
//   - search: smartbox.gtimg.cn/s3/?v=2&q=…&t=all (names as \u escapes; every market at once)
//   - daily and weekly bars: web.ifzq.gtimg.cn/appstock/app/fqkline/get (forward-adjusted)
//   - 15-minute and hourly bars: ifzq.gtimg.cn/appstock/app/kline/mkline, A-shares only (Hong Kong and US
//     symbols answer "param error")

// tencentBase are the hosts (variables so a test can point them at a fake).
var tencentBase = tencentHosts{
	quote:  "https://qt.gtimg.cn",
	search: "https://smartbox.gtimg.cn",
	kline:  "https://web.ifzq.gtimg.cn",
	mkline: "https://ifzq.gtimg.cn",
}

type tencentHosts struct{ quote, search, kline, mkline string }

func (h tencentHosts) hosts() []string {
	var out []string
	for _, u := range []string{h.quote, h.search, h.kline, h.mkline} {
		if p, err := url.Parse(u); err == nil {
			out = append(out, p.Hostname())
		}
	}
	return out
}

// tencent is the client the three markets share, with its cache.
type tencent struct {
	base tencentHosts
	http *http.Client

	mu    sync.Mutex
	cache map[string]cached
	// usCode is a US symbol's code with its exchange ("AAPL" → "AAPL.OQ"), which the bars need; learnt from quotes.
	usCode map[string]string
}

func newTencent(base tencentHosts) *tencent {
	return &tencent{base: base, http: &http.Client{Timeout: 15 * time.Second}, cache: map[string]cached{},
		usCode: map[string]string{}}
}

// tencentSources are the three markets, in the order the page lists them.
func tencentSources(base tencentHosts) []Source {
	t := newTencent(base)
	return []Source{&txMarket{t, mCN}, &txMarket{t, mHK}, &txMarket{t, mUS}}
}

// get reads u, from the cache when younger than fresh (fresh 0 reads it again).
func (t *tencent) get(ctx context.Context, u string, fresh time.Duration) ([]byte, error) {
	t.mu.Lock()
	c, ok := t.cache[u]
	t.mu.Unlock()
	if ok && fresh > 0 && time.Since(c.at) < fresh {
		return c.body, nil
	}
	body, err := t.fetch(ctx, u)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.cache) >= cacheKeep {
		var oldest string
		for k, v := range t.cache {
			if oldest == "" || v.at.Before(t.cache[oldest].at) {
				oldest = k
			}
		}
		delete(t.cache, oldest)
	}
	t.cache[u] = cached{body: body, at: time.Now()}
	return body, nil
}

func (t *tencent) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("连不上腾讯行情：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读腾讯行情的回答出错：%w", err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("腾讯行情说%w", ErrRateLimited)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("腾讯行情回了 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// quoteLines reads the quotes of codes (Tencent's: sh600519, r_hk00700, usAAPL), one request, keyed by code; codes
// it does not know are missing.
func (t *tencent) quoteLines(ctx context.Context, codes []string, fresh time.Duration) (map[string][]string, error) {
	body, err := t.get(ctx, t.base.quote+"/q="+strings.Join(codes, ","), fresh)
	if err != nil {
		return nil, err
	}
	if utf, err := simplifiedchinese.GBK.NewDecoder().Bytes(body); err == nil {
		body = utf
	}
	out := map[string][]string{}
	for line := range bytes.SplitSeq(body, []byte(";")) {
		k, v, ok := strings.Cut(strings.TrimSpace(string(line)), "=")
		if !ok || !strings.HasPrefix(k, "v_") || k == "v_pv_none_match" {
			continue
		}
		out[strings.TrimPrefix(k, "v_")] = strings.Split(strings.Trim(v, `"`), "~")
	}
	return out, nil
}

// A txMarket is one of the three markets.
type txMarket struct {
	t *tencent
	m market
}

type market int

const (
	mCN market = iota
	mHK
	mUS
)

var marketInfo = [...]struct {
	id, name string
	loc      *time.Location
	bars     []string
}{
	mCN: {"cn", "A股", mustLoc("Asia/Shanghai"), []string{"T", "15m", "1H", "1D", "1W"}},
	mHK: {"hk", "港股", mustLoc("Asia/Hong_Kong"), []string{"T", "1D", "1W"}},
	mUS: {"us", "美股", mustLoc("America/New_York"), []string{"T", "1D", "1W"}},
}

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // time/tzdata is linked in
	}
	return l
}

func (s *txMarket) ID() string      { return marketInfo[s.m].id }
func (s *txMarket) Name() string    { return marketInfo[s.m].name }
func (s *txMarket) Market() string  { return marketInfo[s.m].name }
func (s *txMarket) Bars() []string  { return marketInfo[s.m].bars }
func (s *txMarket) Hosts() []string { return s.t.base.hosts() }

func (s *txMarket) Examples() (string, string) {
	switch s.m {
	case mCN:
		return "600519", "茅台"
	case mHK:
		return "00700", "腾讯"
	}
	return "AAPL", "AAPL"
}

// idleAfter: a quote this much older than now is from a market not trading.
const idleAfter = 20 * time.Minute

// now is the clock (a variable so a test can fix it).
var now = time.Now

func (s *txMarket) Help() string {
	switch s.m {
	case mCN:
		return "A 股写 cn:600519 或 cn:600519.SH（只写 6 位数字时 6、5、9 开头当上交所，0、1、2、3 当深交所，4、8、92 当北交所；上证指数写 cn:000001.SH）"
	case mHK:
		return "港股写 hk:00700 或 hk:700，指数如 hk:HSI"
	}
	return "美股写 us:AAPL、us:BRK.B，指数如 us:.DJI、us:.IXIC、us:.INX"
}

var (
	cnRE   = regexp.MustCompile(`^(?:(SH|SZ|BJ))?(\d{6})(?:\.(SH|SZ|BJ))?$`)
	hkNum  = regexp.MustCompile(`^\d{1,5}$`)
	hkName = regexp.MustCompile(`^[A-Z]{2,10}$`)
	usRE   = regexp.MustCompile(`^\.?[A-Z][A-Z0-9]{0,9}(\.[A-Z])?$`)
	// usExchanges are the exchange suffixes Tencent adds to US codes (Nasdaq, NYSE, NYSE American, OTC).
	usExchanges = []string{".OQ", ".N", ".AM", ".PS"}
)

// Normalize: cn "600519.SH", hk "00700" or an index's letters ("HSI"), us "AAPL", "BRK.B", ".DJI".
func (s *txMarket) Normalize(typed string) (string, bool) {
	x := strings.ToUpper(strings.TrimSpace(typed))
	switch s.m {
	case mCN:
		m := cnRE.FindStringSubmatch(x)
		if m == nil || m[1] != "" && m[3] != "" {
			return "", false
		}
		ex := m[1] + m[3]
		if ex == "" {
			ex = cnExchange(m[2])
		}
		return m[2] + "." + ex, true
	case mHK:
		x = strings.TrimSuffix(x, ".HK")
		if len(x) > 2 && strings.HasPrefix(x, "HK") && x[2] >= '0' && x[2] <= '9' {
			x = x[2:]
		}
		if hkNum.MatchString(x) {
			return fmt.Sprintf("%05s", x), true
		}
		return x, hkName.MatchString(x)
	}
	for _, ex := range usExchanges {
		if r, ok := strings.CutSuffix(x, ex); ok && r != "" && r != "." {
			x = r
			break
		}
	}
	if len(x) > 2 && x[len(x)-2] == '-' { // BRK-B, the way some sites write it
		x = x[:len(x)-2] + "." + x[len(x)-1:]
	}
	return x, usRE.MatchString(x)
}

// cnExchange guesses the exchange of a bare six-digit A-share code.
func cnExchange(code string) string {
	switch {
	case strings.HasPrefix(code, "92"), code[0] == '4', code[0] == '8':
		return "BJ"
	case code[0] == '6', code[0] == '5', code[0] == '9':
		return "SH"
	}
	return "SZ"
}

// code is Tencent's code for a symbol: the quote's, and the bars' (Hong Kong without r_, US with its exchange
// when known).
func (s *txMarket) code(sym string, bars bool) string {
	switch s.m {
	case mCN:
		num, ex, _ := strings.Cut(sym, ".")
		return strings.ToLower(ex) + num
	case mHK:
		if bars {
			return "hk" + sym
		}
		return "r_hk" + sym
	}
	if bars {
		s.t.mu.Lock()
		c, ok := s.t.usCode[sym]
		s.t.mu.Unlock()
		if ok {
			return "us" + c
		}
	}
	return "us" + sym
}

func (s *txMarket) Quotes(ctx context.Context, syms []string, fresh time.Duration) (map[string]Quote, error) {
	if len(syms) == 0 {
		return map[string]Quote{}, nil
	}
	codes := make([]string, len(syms))
	for i, sym := range syms {
		codes[i] = s.code(sym, false)
	}
	lines, err := s.t.quoteLines(ctx, codes, fresh)
	if err != nil {
		return nil, err
	}
	out := map[string]Quote{}
	for i, sym := range syms {
		if f, ok := lines[codes[i]]; ok {
			if q, ok := s.parseQuote(sym, f); ok {
				out[sym] = q
			}
		}
	}
	return out, nil
}

func (s *txMarket) Quote(ctx context.Context, sym string, fresh time.Duration) (Quote, error) {
	qs, err := s.Quotes(ctx, []string{sym}, fresh)
	if err != nil {
		return Quote{}, err
	}
	q, ok := qs[sym]
	if !ok {
		return Quote{}, fmt.Errorf("%s里%w", s.Name(), ErrNotFound)
	}
	return q, nil
}

// parseQuote reads one quote line. Common to the markets: 1 name, 2 code, 3 last, 4 previous close, 33 high,
// 34 low, 30 time; the rest differs (see below). False when the line is too short or has no price.
func (s *txMarket) parseQuote(sym string, f []string) (Quote, bool) {
	at := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	num := func(i int) float64 { v, _ := strconv.ParseFloat(at(i), 64); return v }
	if len(f) < 50 || num(3) == 0 {
		return Quote{}, false
	}
	q := Quote{Symbol: sym, Name: at(1), Last: num(3), Base: num(4), High: num(33), Low: num(34), Period: "当日"}
	var layout, kind string
	switch s.m {
	case mCN: // 35 "last/volume/turnover in yuan", 61 type (GP-A, ETF, ZS…), 82 currency
		layout, kind, q.Currency = "20060102150405", at(61), at(82)
		if parts := strings.Split(at(35), "/"); len(parts) == 3 {
			q.Turnover, _ = strconv.ParseFloat(parts[2], 64)
		}
	case mHK: // 37 turnover, 63 type, 75 currency
		layout, kind, q.Currency, q.Turnover = "2006/01/02 15:04:05", at(63), at(75), num(37)
	case mUS: // 35 currency, 37 turnover, 56 type; 2 the code with its exchange
		layout, kind, q.Currency, q.Turnover = "2006-01-02 15:04:05", at(56), at(35), num(37)
		if c := at(2); c != "" {
			s.t.mu.Lock()
			s.t.usCode[sym] = c
			s.t.mu.Unlock()
		}
		if strings.HasPrefix(sym, ".") {
			kind = "ZS"
		}
	}
	if q.Currency == "" {
		q.Currency = map[market]string{mCN: "CNY", mHK: "HKD", mUS: "USD"}[s.m]
	}
	q.Kind = kindName(kind)
	if t, err := time.ParseInLocation(layout, at(30), marketInfo[s.m].loc); err == nil {
		q.At = t
		q.Idle = now().Sub(t) > idleAfter
	}
	q.Decimals = decimals(s.m, q.Kind, q.Last)
	q.Facts = s.facts(q, num)
	return q, true
}

// facts are a quote's further figures. Where Tencent keeps them (checked against live answers: the 52-week range
// against a year of daily bars, turnover rates against volume over shares): 5 open, 4 previous close, 6 volume (A-share
// lots), 43 the day's range as a percentage of the previous close, 39 P/E, 45 market value in 亿; A-shares 38 turnover
// rate, 47 / 48 the limit-up and limit-down prices, 67 / 68 the 52-week high and low; Hong Kong 59 turnover rate,
// 48 / 49 the 52-week range; US 38 turnover rate, 48 / 49 the 52-week range. Unknown (zero or negative) ones are left
// out, and an index has no valuation, turnover rate or limits.
func (s *txMarket) facts(q Quote, num func(int) float64) []Fact {
	var out []Fact
	add := func(label string, v float64, write func(float64) string) {
		if v > 0 {
			out = append(out, Fact{label, write(v)})
		}
	}
	pct := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) + "%" }
	plain := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	vol := num(6)
	if s.m == mCN {
		vol *= 100
	}
	index := q.Kind == "指数"
	turn, hi52, lo52 := map[market]int{mCN: 38, mHK: 59, mUS: 38}[s.m], 48, 49
	if s.m == mCN {
		hi52, lo52 = 67, 68
	}
	add("今开", num(5), q.Price)
	add("昨收", num(4), q.Price)
	add("成交量", vol, fmtCN)
	add("振幅", num(43), pct)
	if !index {
		add("换手率", num(turn), pct)
		add("市盈率", num(39), plain)
		add("总市值", num(45)*1e8, func(v float64) string { return fmtCN(v) + " " + q.Currency })
		if s.m == mCN {
			add("涨停", num(47), q.Price)
			add("跌停", num(48), q.Price)
		}
	}
	add("52 周最高", num(hi52), q.Price)
	add("52 周最低", num(lo52), q.Price)
	return out
}

// fmtCN writes a large number the Chinese way: 2.43万, 18.03亿, 1.80万亿.
func fmtCN(v float64) string {
	for _, u := range []struct {
		n float64
		s string
	}{{1e12, "万亿"}, {1e8, "亿"}, {1e4, "万"}} {
		if v >= u.n {
			return strconv.FormatFloat(v/u.n, 'f', 2, 64) + u.s
		}
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// decimals are the decimals a market writes a price with: two, three for A-share funds (their tick is 0.001) and
// Hong Kong prices under 1, four for US prices under 1.
func decimals(m market, kind string, last float64) int {
	switch {
	case m == mCN && (kind == "ETF" || kind == "基金"), m == mHK && last < 1:
		return 3
	case m == mUS && last < 1:
		return 4
	}
	return 2
}

// kindName is how the page names Tencent's instrument type.
func kindName(typ string) string {
	switch {
	case strings.Contains(typ, "ETF"):
		return "ETF"
	case typ == "ZS":
		return "指数"
	case strings.HasPrefix(typ, "GP-FUND"), strings.HasPrefix(typ, "LOF"), strings.HasPrefix(typ, "JJ"):
		return "基金"
	case strings.HasPrefix(typ, "GP"):
		return "股票"
	}
	return ""
}

// Search asks Tencent's search (every market at once; the three sources share the cached answer) and keeps this
// market's stocks, ETFs, funds and indices, in Tencent's order, with their quotes (one more request).
func (s *txMarket) Search(ctx context.Context, q string, n int, fresh time.Duration) ([]Quote, error) {
	q = strings.TrimSpace(q)
	if q == "" || n <= 0 {
		return nil, nil
	}
	body, err := s.t.get(ctx, s.t.base.search+"/s3/?"+url.Values{"v": {"2"}, "q": {q}, "t": {"all"}, "c": {"1"}}.Encode(), fresh)
	if err != nil {
		return nil, err
	}
	_, v, ok := strings.Cut(strings.TrimSpace(string(body)), "=")
	if !ok {
		return nil, fmt.Errorf("腾讯行情的搜索读不懂")
	}
	v = strings.TrimSuffix(v, ";")
	if u, err := strconv.Unquote(v); err == nil {
		v = u
	} else {
		v = strings.Trim(v, `"`)
	}
	if v == "N" || v == "" {
		return nil, nil
	}
	var syms []string
	for item := range strings.SplitSeq(v, "^") {
		f := strings.Split(item, "~")
		if len(f) < 5 || !s.listed(f[0], f[4]) {
			continue
		}
		code := f[1]
		if s.m == mCN {
			code += "." + f[0]
		}
		if sym, ok := s.Normalize(code); ok && !contains(syms, sym) {
			syms = append(syms, sym)
		}
		if len(syms) == n {
			break
		}
	}
	qs, err := s.Quotes(ctx, syms, fresh)
	if err != nil {
		return nil, err
	}
	var out []Quote
	for _, sym := range syms {
		if x, ok := qs[sym]; ok {
			out = append(out, x)
		}
	}
	return out, nil
}

// listed says whether a search result (its market and type) is one this source offers: warrants, callable bull/bear
// contracts and off-exchange funds are not.
func (s *txMarket) listed(mkt, typ string) bool {
	switch s.m {
	case mCN:
		if mkt != "sh" && mkt != "sz" && mkt != "bj" {
			return false
		}
	case mHK:
		if mkt != "hk" {
			return false
		}
	case mUS:
		if mkt != "us" {
			return false
		}
	}
	return kindName(typ) != ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Candles: 1D and 1W from the forward-adjusted daily and weekly bars, 15m and 1H (A-shares) from the minute bars.
// Tencent stamps a minute bar with its end and a weekly bar with the week's last trading day; both are moved to
// their start. A-share volumes come in lots of 100 shares and are given in shares.
func (s *txMarket) Candles(ctx context.Context, sym, bar string, n int, fresh time.Duration) ([]Candle, error) {
	if !contains(s.Bars(), bar) {
		return nil, fmt.Errorf("%s没有 %s 的 K 线", s.Name(), barTitle(bar))
	}
	if s.m == mUS {
		// the bars need the exchange suffix, which a quote tells
		s.t.mu.Lock()
		_, known := s.t.usCode[sym]
		s.t.mu.Unlock()
		if !known {
			if _, err := s.Quote(ctx, sym, time.Hour); err != nil {
				return nil, err
			}
		}
	}
	code := s.code(sym, true)
	if bar == "T" {
		return s.minutes(ctx, code, fresh)
	}
	var u, key, layout string
	var span time.Duration
	switch bar {
	case "15m", "1H":
		m := map[string]string{"15m": "m15", "1H": "m60"}[bar]
		span = map[string]time.Duration{"15m": 15 * time.Minute, "1H": time.Hour}[bar]
		u = s.t.base.mkline + "/appstock/app/kline/mkline?param=" + code + "," + m + ",," + strconv.Itoa(n)
		key, layout = m, "200601021504"
	default:
		p := map[string]string{"1D": "day", "1W": "week"}[bar]
		u = s.t.base.kline + "/appstock/app/fqkline/get?param=" + code + "," + p + ",,," + strconv.Itoa(n) + ",qfq"
		key, layout = p, "2006-01-02"
	}
	body, err := s.t.get(ctx, u, fresh)
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int                                   `json:"code"`
		Msg  string                                `json:"msg"`
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		// data is [] rather than an object when the code is unknown
		if json.Unmarshal(body, &struct {
			Code int `json:"code"`
		}{}) == nil && bytes.Contains(body, []byte(`"data":[]`)) {
			return nil, fmt.Errorf("%s里%w", s.Name(), ErrNotFound)
		}
		return nil, fmt.Errorf("腾讯行情的 K 线读不懂：%w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("腾讯行情回了错误 %d：%s", env.Code, clip(env.Msg, 200))
	}
	d, ok := env.Data[code]
	if !ok {
		return nil, fmt.Errorf("%s里%w", s.Name(), ErrNotFound)
	}
	raw, ok := d["qfq"+key]
	if !ok {
		raw = d[key] // indices, and some stocks, are not adjusted
	}
	var rows [][]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("腾讯行情的 K 线读不懂：%w", err)
		}
	}
	loc := marketInfo[s.m].loc
	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		var f [6]string
		bad := false
		for j := range f {
			if json.Unmarshal(r[j], &f[j]) != nil {
				bad = true
			}
		}
		if bad {
			continue
		}
		t, err := time.ParseInLocation(layout, f[0], loc)
		if err != nil {
			continue
		}
		switch bar {
		case "15m", "1H":
			t = t.Add(-span)
		case "1W":
			t = t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) // the Monday
		}
		var v [5]float64
		for j := range v {
			if v[j], err = strconv.ParseFloat(f[j+1], 64); err != nil {
				bad = true
			}
		}
		if bad {
			continue
		}
		// Tencent's order is open, close, high, low, volume
		c := Candle{At: t, Open: v[0], Close: v[1], High: v[2], Low: v[3], Vol: v[4]}
		if s.m == mCN {
			c.Vol *= 100
		}
		out = append(out, c)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// Site names the website URL opens.
func (s *txMarket) Site() string { return "腾讯证券" }

// minutePoints bounds the minute line: the minutes are gathered into as few minutes a point as keep it within this
// (a chart takes 120 points a series).
const minutePoints = 120

// minutes reads the last trading day's minute line (web.ifzq.gtimg.cn/appstock/app/minute/query): lines "HHMM price
// cumulative-volume [cumulative-turnover]" and the day ("20261106", empty before the day's first trade, when the
// line is empty too). Each point is a candle of the minutes it gathers: its first price as the open, the last as the
// close, the range, and the volume traded in them.
func (s *txMarket) minutes(ctx context.Context, code string, fresh time.Duration) ([]Candle, error) {
	body, err := s.t.get(ctx, s.t.base.kline+"/appstock/app/minute/query?"+url.Values{"code": {code}}.Encode(), fresh)
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data map[string]struct {
			Data struct {
				Data []string `json:"data"`
				Date string   `json:"date"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		if bytes.Contains(body, []byte(`"data":[]`)) {
			return nil, fmt.Errorf("%s里%w", s.Name(), ErrNotFound)
		}
		return nil, fmt.Errorf("腾讯行情的分时读不懂：%w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("腾讯行情回了错误 %d：%s", env.Code, clip(env.Msg, 200))
	}
	d, ok := env.Data[code]
	if !ok {
		return nil, fmt.Errorf("%s里%w", s.Name(), ErrNotFound)
	}
	day, err := time.ParseInLocation("20060102", d.Data.Date, marketInfo[s.m].loc)
	if err != nil {
		return nil, nil // no trade yet
	}
	type minute struct {
		at         time.Time
		price, cum float64
	}
	var ms []minute
	for _, line := range d.Data.Data {
		f := strings.Fields(line)
		if len(f) < 3 || len(f[0]) != 4 {
			continue
		}
		hh, err1 := strconv.Atoi(f[0][:2])
		mm, err2 := strconv.Atoi(f[0][2:])
		price, err3 := strconv.ParseFloat(f[1], 64)
		cum, err4 := strconv.ParseFloat(f[2], 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || price == 0 {
			continue
		}
		ms = append(ms, minute{day.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute), price, cum})
	}
	per := (len(ms) + minutePoints - 1) / minutePoints
	var out []Candle
	prev := 0.0 // the cumulative volume before the point
	for i := 0; i < len(ms); i += per {
		g := ms[i:min(i+per, len(ms))]
		c := Candle{At: g[0].at, Open: g[0].price, Close: g[len(g)-1].price, High: g[0].price, Low: g[0].price,
			Vol: max(g[len(g)-1].cum-prev, 0)}
		for _, m := range g {
			c.High, c.Low = max(c.High, m.price), min(c.Low, m.price)
		}
		prev = g[len(g)-1].cum
		if s.m == mCN {
			c.Vol *= 100
		}
		out = append(out, c)
	}
	return out, nil
}

// URL is the instrument's page on gu.qq.com.
func (s *txMarket) URL(sym string) string {
	return "https://gu.qq.com/" + s.code(sym, true)
}
