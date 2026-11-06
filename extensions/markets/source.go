package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// A Source is where prices come from: OKX for cryptocurrencies today; a stock market (US, A-shares, …) would be
// another. The page, the tools and the watchlist only ever talk to this interface, so adding a market is writing
// one more Source and listing it in newSources; nothing else changes (docs/extensions/markets.md「Adding a
// source」). A Source keeps its own cache, rate limits and wire format; what it answers is in the shapes below.
type Source interface {
	// ID names the source in references ("okx" in "okx:BTC-USDT"): ^[a-z][a-z0-9]{0,15}$, never changed once
	// shipped, since watchlists store it.
	ID() string
	// Name is how the page names it ("OKX"), Market what it covers ("加密货币").
	Name() string
	Market() string
	// Normalize reads what a user typed as one of its symbols, the way the source writes them ("btc" →
	// "BTC-USDT"); false when it cannot be one. It does not ask the source whether the symbol exists.
	Normalize(typed string) (string, bool)
	// Search finds instruments matching q, the best first, at most n.
	Search(ctx context.Context, q string, n int, fresh time.Duration) ([]Quote, error)
	// Quotes reads the quotes of symbols (Normalized), keyed by symbol; symbols it does not list are missing.
	Quotes(ctx context.Context, symbols []string, fresh time.Duration) (map[string]Quote, error)
	// Quote reads one; ErrNotFound when the source has no such instrument.
	Quote(ctx context.Context, symbol string, fresh time.Duration) (Quote, error)
	// Bars are the periods it has candles for, out of allBars, in order.
	Bars() []string
	// Candles reads the last n candles of symbol over bar, oldest first.
	Candles(ctx context.Context, symbol, bar string, n int, fresh time.Duration) ([]Candle, error)
	// Hosts are the hosts it reaches; each must be in the manifest's network permission and connector hosts.
	Hosts() []string
	// URL is the instrument's page on the source's website ("" when none), opened from the app.
	URL(symbol string) string
	// Help says how its symbols are written, with the ref prefix, for the tools ("OKX 的现货写 okx:BTC-USDT…").
	Help() string
	// Examples are a symbol as typed ("BTC-USDT", "600519") and a search ("BTC", "茅台"), for the page's hints.
	Examples() (typed, search string)
}

var (
	// ErrNotFound: the source has no such instrument.
	ErrNotFound = errors.New("没有这个代码")
	// ErrRateLimited: the source asks to slow down; the connector's status says so.
	ErrRateLimited = errors.New("请求太多，过一会儿再试")
)

// Quote is an instrument's price now and over its period (24 hours for a market that never closes, the trading day
// for one that does). Zero numbers are unknown.
type Quote struct {
	Symbol string
	Name   string // a longer name when the source has one ("Apple Inc."), else ""
	Kind   string // what it is, for the page: 现货, 永续合约, 股票, ETF…
	// Currency is what the prices and the turnover are in (USDT, USD, CNY).
	Currency string
	Last     float64
	// Base is the price the change is against: the price 24 hours ago, or the previous close.
	Base      float64
	High, Low float64
	// Turnover is the period's traded value in Currency.
	Turnover float64
	// Period names the period for the page: "24h", "当日".
	Period string
	At     time.Time
	// Decimals is how many decimals its prices are written with (2 for most stocks); 0: as many as the size needs.
	Decimals int
	// Idle: no trade for a while, the market being closed (or at a break, or the instrument suspended); the page says
	// so with At. A market that never closes leaves it false.
	Idle bool
	// Facts are the source's further figures, in the order to show them (今开, 昨收, 市盈率…); written already.
	Facts []Fact
}

// Fact is one further figure of a quote.
type Fact struct{ Label, Value string }

// PriceLike writes f (a difference of prices) with as many decimals as like is written with.
func (q Quote) PriceLike(f, like float64) string {
	if q.Decimals > 0 {
		return strconv.FormatFloat(f, 'f', q.Decimals, 64)
	}
	l := fmtPrice(like)
	if _, frac, ok := strings.Cut(l, "."); ok {
		return strconv.FormatFloat(f, 'f', len(frac), 64)
	}
	return strconv.FormatFloat(f, 'f', 0, 64)
}

// Price writes one of its prices with its decimals; "—" when unknown.
func (q Quote) Price(f float64) string {
	if q.Decimals > 0 && f != 0 {
		return strconv.FormatFloat(f, 'f', q.Decimals, 64)
	}
	return fmtPrice(f)
}

// Change is the change over the period as a fraction, and whether it could be worked out.
func (q Quote) Change() (float64, bool) {
	if q.Base == 0 || q.Last == 0 {
		return 0, false
	}
	return (q.Last - q.Base) / q.Base, true
}

// Candle is one bar: its start, open, high, low, close and volume (in the instrument's own unit: coins, shares).
type Candle struct {
	At                     time.Time
	Open, High, Low, Close float64
	Vol                    float64
}

// allBars are every period a source may offer, in order, with the page's titles. A Source's Bars are a subset.
var allBars = []struct{ id, short, title string }{
	{"T", "分时", "今日分时"}, // the last trading day's line, minute by minute (gathered into a few minutes a point)
	{"15m", "15分", "15 分钟"},
	{"1H", "1时", "1 小时"},
	{"4H", "4时", "4 小时"},
	{"1D", "1天", "1 天"},
	{"1W", "1周", "1 周"},
}

func barKnown(b string) bool {
	for _, x := range allBars {
		if x.id == b {
			return true
		}
	}
	return false
}

func barTitle(b string) string {
	for _, x := range allBars {
		if x.id == b {
			return x.title
		}
	}
	return b
}

// A Ref names an instrument across sources: "okx:BTC-USDT". The watchlist, the page's state and the tools carry
// refs; a bare symbol (a watchlist from before sources, what a user types) belongs to the default source.
type Ref struct {
	Source string
	Symbol string
}

func (r Ref) String() string { return r.Source + ":" + r.Symbol }

// sources is the running extension's sources, the default first.
type sources []Source

func (ss sources) get(id string) Source {
	for _, s := range ss {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

// parse reads a ref: "okx:BTC-USDT", or a bare symbol for the default source, normalized by its source. False when
// the source is unknown or the symbol cannot be one of its.
func (ss sources) parse(s string) (Ref, bool) {
	s = strings.TrimSpace(s)
	src := ss[0]
	if id, rest, ok := strings.Cut(s, ":"); ok {
		if src = ss.get(strings.ToLower(strings.TrimSpace(id))); src == nil {
			return Ref{}, false
		}
		s = rest
	}
	sym, ok := src.Normalize(s)
	if !ok {
		return Ref{}, false
	}
	return Ref{Source: src.ID(), Symbol: sym}, true
}

// label is how the page writes a ref: the symbol, with the source's name when there is more than one source.
func (ss sources) label(r Ref) string {
	if len(ss) <= 1 {
		return r.Symbol
	}
	if s := ss.get(r.Source); s != nil {
		return r.Symbol + " · " + s.Name()
	}
	return r.String()
}

// markets says what the sources cover, for the page and the tools: "加密货币（OKX）、A股" (the name only when it is
// the market).
func (ss sources) markets() string {
	var parts []string
	for _, s := range ss {
		if s.Market() == s.Name() {
			parts = append(parts, s.Name())
		} else {
			parts = append(parts, s.Market()+"（"+s.Name()+"）")
		}
	}
	return strings.Join(parts, "、")
}
