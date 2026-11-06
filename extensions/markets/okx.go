package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The OKX source: cryptocurrencies, spot pairs and perpetual swaps, from OKX's public market API
// (https://www.okx.com/docs-v5/en/#public-data-rest-api), no key. Its limit is 20 requests
// per 2 seconds per IP on the endpoints read here; answers are kept a little while so a page drawn by several apps
// at once, or refreshed every 30 seconds, stays far below it.

// okxBase is the API (a variable so a test can point it at a fake).
var okxBase = "https://www.okx.com"

var userAgent = "persona-markets/" + version

// cacheKeep bounds the cache: past it, the oldest answers go.
const cacheKeep = 100

// okx is the source: the client with its cache.
type okx struct {
	base string
	http *http.Client

	mu    sync.Mutex
	cache map[string]cached
	// missing: when an instrument was last found not to exist, so a watchlist keeping one does not ask every time
	missing map[string]time.Time
}

type cached struct {
	body []byte
	at   time.Time
}

func newOKX(base string) *okx {
	return &okx{base: base, http: &http.Client{Timeout: 15 * time.Second}, cache: map[string]cached{}, missing: map[string]time.Time{}}
}

// get reads path and returns the answer's data (OKX wraps it in {code, msg, data}), from the cache when younger
// than fresh (fresh 0 reads it again).
func (o *okx) get(ctx context.Context, path string, fresh time.Duration) (json.RawMessage, error) {
	o.mu.Lock()
	c, ok := o.cache[path]
	o.mu.Unlock()
	if ok && fresh > 0 && time.Since(c.at) < fresh {
		return c.body, nil
	}
	data, err := o.fetch(ctx, path)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.cache) >= cacheKeep {
		var oldest string
		for k, v := range o.cache {
			if oldest == "" || v.at.Before(o.cache[oldest].at) {
				oldest = k
			}
		}
		delete(o.cache, oldest)
	}
	o.cache[path] = cached{body: data, at: time.Now()}
	return data, nil
}

func (o *okx) fetch(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("连不上 OKX：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读 OKX 的回答出错：%w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("OKX 说%w", ErrRateLimited)
	}
	var env struct {
		Code string          `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("OKX 回了 HTTP %d，读不懂", resp.StatusCode)
	}
	switch env.Code {
	case "0":
		return env.Data, nil
	case "51001": // instrument ID doesn't exist
		return nil, fmt.Errorf("OKX 上%w", ErrNotFound)
	case "50011": // too many requests
		return nil, fmt.Errorf("OKX 说%w", ErrRateLimited)
	}
	return nil, fmt.Errorf("OKX 回了错误 %s：%s", env.Code, clip(env.Msg, 200))
}

// okxTicker is OKX's ticker: an instrument's last price and 24-hour figures (OKX gives every number as a string; kept as given for
// display, parsed for arithmetic).
type okxTicker struct {
	InstID    string `json:"instId"`
	InstType  string `json:"instType"`
	Last      string `json:"last"`
	Open24h   string `json:"open24h"`
	High24h   string `json:"high24h"`
	Low24h    string `json:"low24h"`
	Vol24h    string `json:"vol24h"`    // in the base currency (contracts for SWAP)
	VolCcy24h string `json:"volCcy24h"` // in the quote currency for SPOT, the base currency for SWAP
	TS        string `json:"ts"`
}

// quote is the ticker in the shape every source answers.
func (t okxTicker) quote() Quote {
	f := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	q := Quote{Symbol: t.InstID, Kind: "现货", Currency: quoteCcy(t.InstID), Last: f(t.Last), Base: f(t.Open24h),
		High: f(t.High24h), Low: f(t.Low24h), Turnover: f(t.VolCcy24h), Period: "24h"}
	if instType(t.InstID) == "SWAP" { // a swap's volCcy24h is in the base currency
		q.Kind, q.Turnover = "永续合约", q.Turnover*q.Last
	}
	if ms, err := strconv.ParseInt(t.TS, 10, 64); err == nil {
		q.At = time.UnixMilli(ms)
	}
	return q
}

// instType is the OKX instrument type of an instrument id: perpetual swaps end in -SWAP, the rest read here are spot.
func instType(id string) string {
	if strings.HasSuffix(id, "-SWAP") {
		return "SWAP"
	}
	return "SPOT"
}

// list reads every ticker of an instrument type (SPOT or SWAP): one request for the whole market.
func (o *okx) list(ctx context.Context, typ string, fresh time.Duration) ([]okxTicker, error) {
	data, err := o.get(ctx, "/api/v5/market/tickers?instType="+typ, fresh)
	if err != nil {
		return nil, err
	}
	var list []okxTicker
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("OKX 的行情读不懂：%w", err)
	}
	return list, nil
}

// fewQuotes: up to this many instruments are read one by one (in parallel; a ticker is about 360 bytes), more
// through the whole list of their type (one request, but the spot list is some 380 KB, 100 KB compressed).
const fewQuotes = 5

// Quotes reads the tickers of ids, keyed by id; ids OKX does not list are missing.
func (o *okx) Quotes(ctx context.Context, ids []string, fresh time.Duration) (map[string]Quote, error) {
	if len(ids) <= fewQuotes {
		return o.each(ctx, ids, fresh)
	}
	types, want := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		types[instType(id)] = true
		want[id] = true
	}
	out := map[string]Quote{}
	for _, typ := range []string{"SPOT", "SWAP"} {
		if !types[typ] {
			continue
		}
		list, err := o.list(ctx, typ, fresh)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if want[t.InstID] {
				out[t.InstID] = t.quote()
			}
		}
	}
	return out, nil
}

// each reads ids one ticker each, in parallel; one OKX does not list is missing, another error fails them all.
func (o *okx) each(ctx context.Context, ids []string, fresh time.Duration) (map[string]Quote, error) {
	type answer struct {
		id  string
		q   Quote
		err error
	}
	ch := make(chan answer, len(ids))
	for _, id := range ids {
		go func() {
			q, err := o.Quote(ctx, id, fresh)
			ch <- answer{id, q, err}
		}()
	}
	out := map[string]Quote{}
	var first error
	for range ids {
		a := <-ch
		switch {
		case a.err == nil:
			out[a.id] = a.q
		case errors.Is(a.err, ErrNotFound):
		case first == nil:
			first = a.err
		}
	}
	if first != nil {
		return nil, first
	}
	return out, nil
}

// search finds instruments, spot and perpetual swaps, whose id matches q (case-insensitive; "/", "_" and spaces
// read as "-"): first those whose base coin is q, then whose base starts with it, then whose id contains it; in
// each, the busiest (24-hour turnover in the quote currency) first, spot before swap when equal. At most n.
func (o *okx) Search(ctx context.Context, q string, n int, fresh time.Duration) ([]Quote, error) {
	q = strings.ToUpper(strings.TrimSpace(q))
	q = strings.NewReplacer("/", "-", "_", "-", " ", "").Replace(q)
	if q == "" {
		return nil, nil
	}
	type hit struct {
		t    Quote
		rank int
		turn float64
	}
	var hits []hit
	for _, typ := range []string{"SPOT", "SWAP"} {
		list, err := o.list(ctx, typ, fresh)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			id := t.InstID
			if typ == "SWAP" && !strings.HasSuffix(id, "-SWAP") {
				continue // delivery futures and the like are not offered
			}
			base, _, _ := strings.Cut(id, "-")
			rank := -1
			switch {
			case id == q || base == q:
				rank = 0
			case strings.HasPrefix(id, q):
				rank = 1
			case strings.Contains(id, q):
				rank = 2
			}
			if rank >= 0 {
				qt := t.quote()
				hits = append(hits, hit{qt, rank, qt.Turnover})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].turn > hits[j].turn
	})
	var out []Quote
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, h.t)
	}
	return out, nil
}

// quoteCcy is the currency an instrument is priced in: USDT for BTC-USDT and BTC-USDT-SWAP.
func quoteCcy(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// Quote reads one instrument's ticker.
func (o *okx) Quote(ctx context.Context, id string, fresh time.Duration) (Quote, error) {
	o.mu.Lock()
	at, gone := o.missing[id]
	o.mu.Unlock()
	if gone && fresh > 0 && time.Since(at) < fresh {
		return Quote{}, fmt.Errorf("OKX 上%w", ErrNotFound)
	}
	data, err := o.get(ctx, "/api/v5/market/ticker?instId="+url.QueryEscape(id), fresh)
	if errors.Is(err, ErrNotFound) {
		o.mu.Lock()
		if len(o.missing) >= cacheKeep {
			clear(o.missing)
		}
		o.missing[id] = time.Now()
		o.mu.Unlock()
	}
	if err != nil {
		return Quote{}, err
	}
	var list []okxTicker
	if err := json.Unmarshal(data, &list); err != nil {
		return Quote{}, fmt.Errorf("OKX 的行情读不懂：%w", err)
	}
	if len(list) == 0 {
		return Quote{}, fmt.Errorf("OKX 上%w", ErrNotFound)
	}
	return list[0].quote(), nil
}

// Bars: OKX has more; these are the ones offered.
func (o *okx) Bars() []string { return []string{"15m", "1H", "4H", "1D", "1W"} }

// Candles reads the last n bars of id, oldest first. OKX's 1D bars start at midnight Hong Kong time by default;
// "1Dutc" would be UTC, but the user's time zone is usually nearer the former.
func (o *okx) Candles(ctx context.Context, id, bar string, n int, fresh time.Duration) ([]Candle, error) {
	q := url.Values{"instId": {id}, "bar": {bar}, "limit": {strconv.Itoa(n)}}
	data, err := o.get(ctx, "/api/v5/market/candles?"+q.Encode(), fresh)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("OKX 的 K 线读不懂：%w", err)
	}
	out := make([]Candle, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // OKX answers newest first
		r := rows[i]
		if len(r) < 6 {
			continue
		}
		ms, err := strconv.ParseInt(r[0], 10, 64)
		if err != nil {
			continue
		}
		var f [5]float64
		bad := false
		for j := range f {
			if f[j], err = strconv.ParseFloat(r[j+1], 64); err != nil {
				bad = true
			}
		}
		if bad {
			continue
		}
		out = append(out, Candle{At: time.UnixMilli(ms), Open: f[0], High: f[1], Low: f[2], Close: f[3], Vol: f[4]})
	}
	return out, nil
}

var instRE = regexp.MustCompile(`^[A-Z0-9]{1,20}-[A-Z0-9]{1,20}(-SWAP)?$`)

// Normalize reads what a user typed as an instrument id: upper case, "/" or "_" as "-", a bare coin as its USDT
// pair. It says false when it cannot be an OKX spot pair or perpetual swap.
func (o *okx) Normalize(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("/", "-", "_", "-", " ", "").Replace(s)
	if s != "" && !strings.Contains(s, "-") {
		s += "-USDT"
	}
	return s, instRE.MatchString(s)
}

func (o *okx) ID() string     { return "okx" }
func (o *okx) Name() string   { return "OKX" }
func (o *okx) Market() string { return "加密货币" }

func (o *okx) Hosts() []string { return []string{"www.okx.com"} }

func (o *okx) Examples() (string, string) { return "BTC-USDT", "BTC" }

func (o *okx) Help() string {
	return "OKX 的现货写 okx:BTC-USDT、okx:ETH-USDC，永续合约 okx:BTC-USDT-SWAP，只写币名（BTC）当成对 USDT"
}

// URL is the instrument's trading page on OKX.
func (o *okx) URL(id string) string {
	kind := "spot"
	if instType(id) == "SWAP" {
		kind = "swap"
	}
	return "https://www.okx.com/trade-" + kind + "/" + strings.ToLower(id)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
