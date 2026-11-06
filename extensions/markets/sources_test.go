package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// stocks is a second source, the way a stock market would be one: its own symbols, a trading day as the period,
// daily and weekly bars only, a name for each instrument and no website link. Synthetic prices.
type stocks struct{}

var stockList = map[string]Quote{
	"AAPL": {Symbol: "AAPL", Name: "Example Apple", Kind: "股票", Currency: "USD", Last: 210, Base: 200, High: 212, Low: 199, Turnover: 9e9, Period: "今日"},
	"MSFT": {Symbol: "MSFT", Name: "Example Soft", Kind: "股票", Currency: "USD", Last: 400, Base: 404, High: 405, Low: 398, Turnover: 7e9, Period: "今日"},
}

func (stocks) ID() string        { return "us" }
func (stocks) Name() string      { return "美股" }
func (stocks) Market() string    { return "美股" }
func (stocks) Hosts() []string   { return nil }
func (stocks) Bars() []string    { return []string{"1D", "1W"} }
func (stocks) URL(string) string { return "" }
func (stocks) Help() string      { return "美股写 us:AAPL" }

func (stocks) Examples() (string, string) { return "AAPL", "Apple" }

func (stocks) Normalize(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" || len(s) > 5 || strings.ContainsAny(s, "-/ ") {
		return "", false
	}
	return s, true
}

func (stocks) Search(_ context.Context, q string, n int, _ time.Duration) ([]Quote, error) {
	var out []Quote
	for _, sym := range []string{"AAPL", "MSFT"} {
		x := stockList[sym]
		if strings.Contains(sym, strings.ToUpper(q)) || strings.Contains(strings.ToUpper(x.Name), strings.ToUpper(q)) {
			out = append(out, x)
		}
	}
	return out[:min(n, len(out))], nil
}

func (stocks) Quotes(_ context.Context, syms []string, _ time.Duration) (map[string]Quote, error) {
	out := map[string]Quote{}
	for _, s := range syms {
		if q, ok := stockList[s]; ok {
			out[s] = q
		}
	}
	return out, nil
}

func (stocks) Quote(_ context.Context, sym string, _ time.Duration) (Quote, error) {
	if q, ok := stockList[sym]; ok {
		return q, nil
	}
	return Quote{}, ErrNotFound
}

func (stocks) Candles(_ context.Context, sym, bar string, n int, _ time.Duration) ([]Candle, error) {
	var out []Candle
	for i := range n {
		out = append(out, Candle{At: time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC), Open: 200, High: 211, Low: 199, Close: 205 + float64(i%3), Vol: 1e6})
	}
	return out, nil
}

func TestSources(t *testing.T) {
	f := newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = sources{newOKX(f.srv.URL), stocks{}}
	dir := t.TempDir()
	// a watchlist stored before sources: bare symbols are the default source's
	if err := os.WriteFile(filepath.Join(dir, "watchlist.json"), []byte(`["BTC-USDT", "us:aapl", "nope:X", "okx:BTC-USDT"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	if got := refStrings(p.watchlist()); !slices.Equal(got, []string{"okx:BTC-USDT", "us:AAPL"}) {
		t.Fatalf("watchlist %v", got)
	}

	// the watchlist mixes the markets, each row naming its source and its own period
	v := render(t, p, "", 5)
	got := texts(v)
	// grouped by source in the sources' order, a name as the title with the symbol under it
	// (after the order and market controls)
	if v.Sections[1].Title != "加密货币 · 1" || v.Sections[2].Title != "美股 · 1" {
		t.Errorf("groups %q %q", v.Sections[1].Title, v.Sections[2].Title)
	}
	for _, want := range []string{"row BTC-USDT +2000.00", "row Example Apple AAPL · +10 · 成交 90.00亿 USD 210",
		"[+5.00% danger] →open:us:AAPL", "OKX、美股 公开行情", "segmented   all   →mkt"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page has no %q:\n%s", want, got)
		}
	}

	// a search asks every source, a group each
	r := act(t, p, v.State, "search", map[string]string{"q": "a"})
	if len(r.View.Sections) < 2 || !strings.HasPrefix(r.View.Sections[0].Title, "搜索「a」 · 加密货币 · ") ||
		!strings.HasPrefix(r.View.Sections[1].Title, "美股 · ") || r.View.Sections[1].Items[0].Title != "Example Apple" {
		t.Fatalf("search %s", texts(*r.View))
	}

	// a stock's page: its bars only, no link, the trading day's figures
	r = act(t, p, "", "open:us:MSFT", nil)
	got = texts(*r.View)
	for _, want := range []string{"{今日 成交额=7.00B USD", "K 线 · 1 天", "→add:us:MSFT"} {
		if !strings.Contains(got, want) {
			t.Errorf("the stock page has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "link ") || strings.Contains(got, "1 小时") {
		t.Errorf("the stock page:\n%s", got)
	}
	for _, it := range r.View.Sections[1].Items {
		if it.Type == "segmented" && len(it.Choices) != 2 {
			t.Errorf("bars offered %+v", it.Choices)
		}
	}
	if r = act(t, p, r.View.State, "add:us:MSFT", nil); r.Toast != "已加入 MSFT" {
		t.Fatalf("add a stock: %q", r.Toast)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "watchlist.json"))
	var stored []string
	if json.Unmarshal(raw, &stored) != nil || !slices.Equal(stored, []string{"okx:BTC-USDT", "us:AAPL", "us:MSFT"}) {
		t.Fatalf("stored %s", raw)
	}

	// the tools take refs across sources and refuse a bar the source lacks
	call := func(name string, args any) string {
		raw, _ := json.Marshal(map[string]any{"name": name, "args": args})
		out, err := p.cToolsCall(context.Background(), nil, raw)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out.(map[string]any)["text"].(string)
	}
	if got := call("markets_quote", map[string]any{"symbols": []string{"us:aapl", "btc"}}); !strings.Contains(got, "us:AAPL（Example Apple）：最新 210 USD，今日 +5.00%") ||
		!strings.Contains(got, "okx:BTC-USDT：最新 50000.00 USDT") {
		t.Fatalf("quote:\n%s", got)
	}
	if got := call("markets_candles", map[string]any{"symbol": "us:AAPL", "bar": "1H"}); !strings.Contains(got, "ERR 美股 没有") {
		t.Fatalf("a bar the source lacks: %s", got)
	}
	if got := call("markets_candles", map[string]any{"symbol": "us:AAPL", "limit": 2}); !strings.Contains(got, "us:AAPL 的 1D K 线") {
		t.Fatalf("the source's default bar: %s", got)
	}
	var tl struct {
		Tools []Tool `json:"tools"`
	}
	listed, _ := p.cToolsList(context.Background(), nil, nil)
	b, _ := json.Marshal(listed)
	_ = json.Unmarshal(b, &tl)
	if !strings.Contains(tl.Tools[0].Description, "加密货币（OKX）、美股）") {
		t.Errorf("the tools do not say what they cover: %s", tl.Tools[0].Description)
	}
}

func TestParseRef(t *testing.T) {
	ss := sources{newOKX(""), stocks{}}
	for in, want := range map[string]string{"btc": "okx:BTC-USDT", "okx:eth/usdc": "okx:ETH-USDC", " US:aapl ": "us:AAPL",
		"us:BTC-USDT": "", "xx:BTC": "", "": ""} {
		r, ok := ss.parse(in)
		if want == "" && ok || want != "" && r.String() != want {
			t.Errorf("parse(%q) = %v %v", in, r, ok)
		}
	}
}
