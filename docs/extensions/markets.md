# Markets

[Documentation](../README.md) · [Source](../../extensions/markets/)

Market prices in Persona: a watchlist on the apps' main navigation, a search, an instrument page with its price chart and volume, and assistant tools. Prices come from **sources** behind one interface: OKX's public market API for cryptocurrencies (`okx`, the default) and Tencent's quote endpoints for A-shares (`cn`), Hong Kong (`hk`) and US (`us`) stocks, ETFs and indices. None needs an account. More can be added, see [Adding a source](#adding-a-source).

| Extension point | Contribution | Behavior |
| --- | --- | --- |
| `ui` | `watchlist`, placement `nav_page`, icon `chart` | A "行情" entry on the main navigation. On apps of UI version 2 and later it starts with 大盘 (the benchmarks of the card, when there are at least two) and, with two or more instruments, a control for the order (自选顺序, 涨幅, 跌幅: within each group, the stored order unchanged) and, with two to four sources in the watchlist, one for the market shown (全部 or one source's group). The page then lists the watchlist in a group per source (加密货币, A股, 港股, 美股; a source with nothing in the watchlist is left out), each row titled with the instrument's name when it has one, with its symbol, the change in price, the period's turnover, the last price and the change; a market that is not trading says "休市 · 截至" and the time of its last quote, in the exchange's own time. A failing source shows its error in its group while the others draw. A search box (UI version 3) finds spot pairs and perpetual swaps by coin or pair, and stocks by code, name or pinyin initials; its groups are ordered by their best match (a symbol or name equal to the query first; OKX last for a query in Chinese), and a query that is itself a code (`cn:600519`, `BTC-USDT-SWAP`) is offered first as its own row, since with a search box the page has no add field. Apps before it add and search through the add field and its buttons. Open one for its figures over the period (24 hours, or the trading day), a stock's further figures (open, previous close, volume, range, turnover rate, P/E, market value, A-share limit prices, the 52-week range), closing prices and volume over the bars its source offers (stocks also today's minute line, 分时), and buttons to move it to the top of its group or one place up or down, remove it or open it on OKX or 腾讯证券. Adding one opened from a search goes back to the search. The page refreshes every 30 seconds while it is on screen, every 5 minutes when no quote on it is from a market trading. |
| `ui` | `card`, placement `overview_card`, opens `watchlist` | A card on 总览: BTC, 上证指数, 恒生指数 and 纳斯达克 (each source's that is installed), then the watchlist's five biggest moves either way; a tap opens the page. The setting `overview_card` (on by default) switches it off: the card then answers `unsupported`, which the apps take as nothing to draw. Refreshes every minute, every 5 when no market on it is trading. |
| `connectors` | `markets`, capabilities `status`, `tools`, `inbox` and `fallback`, kind `price_alert` | `markets_quote` (the watchlist or given pairs), `markets_search` (find pairs by coin or pair) and `markets_candles` (OHLCV bars) for the assistant, across every source. Price alerts that went off, in the inbox (see [Price alerts](#price-alerts)). |

## Sources and references

Everything outside the sources (the page, the tools, the watchlist) uses only the `Source` interface in [`source.go`](../../extensions/markets/source.go): normalize a typed symbol, search, quotes, one quote, the bars offered, candles, the instrument's website and the hosts reached. Answers come in common shapes: a `Quote` (last, the price the change is against, high, low, turnover, currency, and the period's name such as `24h` or `今日`) and `Candle`s.

An instrument is named by a **ref**, `source:symbol` (`okx:BTC-USDT`). The watchlist file, the page's state, action ids and the tools carry refs. A bare symbol, typed or configured, is the default (first) source's. With more than one source the watchlist and a search's results are grouped by source in the sources' order (sources rank in their own ways and currencies, so their lists are not merged), reordering moves an instrument within its group, and a failing source is skipped while another answers. Each source offers its own subset of the bars `15m`, `1H`, `4H`, `1D`, `1W`; the configured default bar falls back to the source's nearest.

## Data, network and rate limits (OKX)

The hub's machine reads `https://www.okx.com/api/v5/market/…`; the phone needs no access to OKX. Endpoints:

- `/market/ticker?instId=…`: one instrument, cached for 10 seconds; up to 5 instruments are read this way, in parallel (about 360 bytes each). One OKX does not list is remembered for as long, so a watchlist keeping it does not ask every time.
- `/market/tickers?instType=SPOT` (and `SWAP` when the watchlist has a perpetual swap, or for a search): the whole list in one request (the spot list is about 380 KB, 100 KB compressed), for more than 5 instruments and for the search, cached for 10 seconds.
- `/market/candles?instId=…&bar=…&limit=…`: the last 60 bars for the page, cached for 30 seconds.

OKX allows about 20 requests per 2 seconds per IP on these endpoints; the page refreshes every 30 seconds while it is on screen. A `429` or OKX error `50011` shows as "rate limited" in the connector status and the page offers a retry.

Instruments are OKX instrument IDs: spot pairs such as `BTC-USDT` or `ETH-USDC`, and perpetual swaps such as `BTC-USDT-SWAP`. A bare coin (`BTC`) means its USDT pair; `/` and `_` are read as `-`. A 24-hour change is against the price 24 hours ago (`open24h`), not a calendar day. OKX's `1D` bars start at 00:00 Hong Kong time.

OKX's search reads the same cached spot and swap ticker lists (no extra requests per keystroke beyond the 10-second cache) and matches case-insensitively: pairs whose base coin is the query first, then ids starting with it, then ids containing it; within each, the highest 24-hour turnover in the quote currency first. It shows at most 30.

## Data, network and limits (Tencent: A-shares, Hong Kong, US)

These are the endpoints Tencent's own stock pages (`gu.qq.com`) read, **not a documented API**: there is no published contract, terms or rate limit, and the formats were read off live answers (samples in [`testdata/tencent`](../../extensions/markets/testdata/tencent/), which the tests run against). Tencent may change them without notice; the sources then fail with "读不懂" until updated. The three markets share one client and cache.

- `qt.gtimg.cn/q=sh600519,r_hk00700,usAAPL`: quotes, every symbol of a market in one request (GBK text, `~`-separated), cached for 10 seconds. Hong Kong is read through `r_` (real time; without it Tencent answers 15 minutes late). US quotes may be delayed; outside US trading hours they are the last close.
- `smartbox.gtimg.cn/s3/`: the search, every market in one answer (shared by the three sources), then one quote request per market for its hits. Warrants, callable bull/bear contracts and off-exchange funds are left out.
- `web.ifzq.gtimg.cn/appstock/app/fqkline/get`: daily and weekly bars, forward-adjusted (`qfq`) where Tencent has them.
- `web.ifzq.gtimg.cn/appstock/app/minute/query`: the last trading day's minute line (`T`, 分时) for every market: one point per minute with the cumulative volume, gathered into as many minutes a point as keep it within 120 points. Before the day's first trade it is empty (for US stocks that is the whole Beijing morning), and the page says so.
- `ifzq.gtimg.cn/appstock/app/kline/mkline`: 15-minute and hourly bars, A-shares only (Tencent answers "param error" for Hong Kong and US codes), so Hong Kong and US offer `T`, `1D` and `1W`.

Symbols, typed after the source (`cn:600519`):

| Source | Symbols | Notes |
| --- | --- | --- |
| `cn` A股 | `600519.SH`, `000001.SZ`, `920000.BJ`; typed also `600519`, `sh600519` | A bare six-digit code is Shanghai when it starts with 6, 5 or 9, Beijing with 4, 8 or 92, else Shenzhen; indices that share a code with a stock need the exchange (`000001.SH` 上证指数). Volumes come in lots of 100 and are given in shares. Currency CNY. |
| `hk` 港股 | `00700`, `HSI`; typed also `700`, `0700.HK` | HKD, or CNY for RMB counters. |
| `us` 美股 | `AAPL`, `BRK.B`, `.DJI`, `.IXIC`, `.INX`; typed also `AAPL.OQ`, `BRK-B` | The exchange suffix Tencent needs for bars is learnt from a quote. Times are New York's. |

The period is the trading day (`当日`): the change is against the previous close, and high, low and turnover are the day's. Prices are written with two decimals (three for A-share funds and ETFs and for Hong Kong prices under 1, four for US prices under 1). A quote more than 20 minutes old is shown as the market not trading (closed, at its lunch break, or the instrument suspended). A stock's further figures are read from fields of the quote line whose meaning was checked against live answers (the 52-week range against a year of daily bars, turnover rates against volume over shares; see `facts` in `tencent.go`); an index has no valuation, turnover rate or limits. A minute bar is stamped by Tencent with its end and a weekly bar with the week's last trading day; both are shown from their start (the week's Monday). Times are each market's own (Asia/Shanghai, Asia/Hong_Kong, America/New_York) before the chart's time zone is applied.

## Price alerts

An instrument's page has a 价格提醒 section: its alerts (tap one to delete it) and a form for a new one, 涨到 or 跌到 a price, or 涨跌幅超 a percentage of the period's change either way. One that the quote already meets is refused, since it would go off at once. The watchlist page lists every alert under the groups, each opening its instrument.

The extension checks the waiting alerts every minute on its own (each source asked once for all of them, quotes up to 30 seconds old). An alert goes off once: it is marked met with the price and time, the hub is woken (`connector.changed`, scope `inbox`), and `connector.inbox.collect` (answered from memory) hands it over as a thing of kind `price_alert`: thread `alert:<id>`, title such as `贵州茅台 600519.SH 涨到 1500.00`, the price, the change and when it was set in the preview, a link to the instrument's page, tier `action` (and `extra.rule_tier` `action`, so no triage model is needed), tag `symbol`, and the user's `triage_rules` when set. A met alert stays on the pages, marked 到了, until deleted; its thing is handed over for a week. Alerts are kept in the data directory (`alerts.json`, at most 50).

## Configuration

[Manifest and settings schema](../../extensions/markets/persona-extension.json)

| Setting | Meaning |
| --- | --- |
| `watchlist` | The initial watchlist (default `BTC-USDT`, `ETH-USDT`, `SOL-USDT`); stocks with their source, `cn:600519`, `hk:00700`, `us:AAPL`. Used only until the page first changes it; after that the watchlist lives in the extension's data directory (`watchlist.json`, at most 50 instruments). |
| `bar` | Default bar for the instrument page and `markets_candles`: `T` (stocks' minute line), `15m`, `1H` (default), `4H`, `1D` or `1W`; a source without it uses its nearest. |
| `triage_rules` | The user's own rules for sorting the alerts in the inbox (the standard setting every inbox extension has); empty uses the inbox's global rules. |
| `overview_card` | The card on 总览 (default on). Off, the apps draw no card. |
| `colors` | How a change is coloured: `red_up` (红涨绿跌, the default) or `green_up` (绿涨红跌). The page sends a rise as tone `danger` and a fall as `positive` with `red_up`, the other way round with `green_up`. |
| `tz` | Time zone for chart labels and tool output; empty uses the app's (the page) or the hub machine's (tools). |

## App view and compatibility

The charts are a line of closing prices and bars of volume; the declarative UI has no candlestick chart, so each point's detail shows close, high and low instead. Apps before UI version 2 get the figures as metrics and the bar choice as rows, without charts.

## Adding a source

1. Write a type implementing `Source` in its own file (`<id>.go`; `tencent.go` is three sources sharing one client), with its own HTTP client, cache and rate limiting; map the provider's errors onto `ErrNotFound` and `ErrRateLimited` (wrapped, so `errors.Is` works). Its `ID` is stored in users' watchlists: choose it once (`us`, `cn`, …) and never change it.
2. List it in `newSources()` in [`plugin.go`](../../extensions/markets/plugin.go). The first one stays the default for bare symbols, so append new sources rather than putting them first.
3. Add its `Hosts()` to the manifest's `permissions.network` and the connector's `hosts`; `TestManifest` fails otherwise. A provider that needs a key gets a manifest secret and reads it in `configure`.
4. Test it against a fake server, as `markets_test.go` does for OKX; `sources_test.go` shows a second, stock-like source mixed with OKX.

## Limits

- The stock sources rely on Tencent's undocumented endpoints (above). A keyed provider (e.g. Alpaca for real-time US quotes and intraday bars) could be added as another source.
- Hong Kong and US have no intraday bars.
- Alerts are checked every minute while the extension runs; a move between two checks that comes back is missed. The assistant cannot set alerts yet.
