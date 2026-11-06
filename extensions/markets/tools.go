package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// The connector's read tools, for the assistant: the hub's machine reaches the sources where the user's computers
// may not. They take and answer refs ("okx:BTC-USDT"; a bare symbol is the default source's), whatever the source.

// Tool is a tool as connector.tools.list lists it.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (p *plugin) tools() []Tool {
	covers := p.srcs.markets()
	var barIDs []string
	for _, b := range allBars {
		barIDs = append(barIDs, b.id)
	}
	var helps []string
	for _, s := range p.srcs {
		helps = append(helps, s.Help())
	}
	refs := fmt.Sprintf("代码写成「来源:代码」（如 okx:BTC-USDT），不写来源就是 %s 的；%s。", p.srcs[0].Name(), strings.Join(helps, "；"))
	return []Tool{
		{Name: "markets_quote",
			Description: "查实时行情（" + covers + "）：每个代码的最新价、这段时间（加密货币是 24 小时）的涨跌、最高、最低和成交额。" + refs + "symbols 不给就是用户的自选。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"symbols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": watchMost,
					"description": "代码，不给就是用户的自选"}}}},
		{Name: "markets_search",
			Description: "按名称或代码搜能查的品种（" + covers + "），列出对得上的代码、最新价、涨跌和成交额，每个来源最靠前的在前。不知道准确的代码时先用它，再用 markets_quote / markets_candles。",
			InputSchema: map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "名称或代码的一部分，如 PEPE、ETH-USDC"},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": searchMost, "description": "最多几个，默认 10"}}}},
		{Name: "markets_candles",
			Description: "读一个代码的 K 线（" + covers + "）：每根的开始时间（用户时区）、开、高、低、收和成交量，旧的在前。" + refs + "来源没有的周期会报错。",
			InputSchema: map[string]any{"type": "object", "required": []string{"symbol"}, "properties": map[string]any{
				"symbol": map[string]any{"type": "string", "description": "代码，如 BTC-USDT"},
				"bar":    map[string]any{"type": "string", "enum": barIDs, "description": "周期，默认用户设的（一般是 1H）"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "最近几根，默认 24"}}}},
	}
}

func (p *plugin) cToolsList(context.Context, *pluginkit.Hub, json.RawMessage) (any, error) {
	return map[string]any{"tools": p.tools()}, nil
}

func (p *plugin) cToolsCall(ctx context.Context, _ *pluginkit.Hub, raw json.RawMessage) (any, error) {
	var in struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var args struct {
		Symbols []string `json:"symbols"`
		Symbol  string   `json:"symbol"`
		Bar     string   `json:"bar"`
		Query   string   `json:"query"`
		Limit   int      `json:"limit"`
	}
	if len(in.Args) > 0 && string(in.Args) != "null" {
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return nil, fmt.Errorf("参数不对：%w", err)
		}
	}
	var text string
	var err error
	switch in.Name {
	case "markets_quote":
		text, err = p.toolQuote(ctx, args.Symbols)
	case "markets_search":
		limit := args.Limit
		if limit <= 0 || limit > searchMost {
			limit = 10
		}
		text, err = p.toolSearch(ctx, args.Query, limit)
	case "markets_candles":
		limit := args.Limit
		if limit <= 0 || limit > 100 {
			limit = 24
		}
		text, err = p.toolCandles(ctx, args.Symbol, args.Bar, limit)
	default:
		return nil, fmt.Errorf("没有工具 %s", in.Name)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": text}, nil
}

// quoteLine is one quote as the tools write it.
func quoteLine(r Ref, q Quote) string {
	ch := "—"
	if c, ok := q.Change(); ok {
		ch = fmtChange(c)
	}
	name := ""
	if q.Name != "" {
		name = "（" + q.Name + "）"
	}
	idle := ""
	if q.Idle && !q.At.IsZero() {
		idle = "（休市，截至交易所当地时间 " + q.At.Format("2006-01-02 15:04") + "）"
	}
	line := fmt.Sprintf("- %s%s：最新 %s %s%s，%s %s，最高 %s，最低 %s，成交额 %s %s", r, name, q.Price(q.Last), q.Currency, idle, q.Period, ch,
		q.Price(q.High), q.Price(q.Low), fmtCompactFloat(q.Turnover), q.Currency)
	var facts []string
	for _, f := range q.Facts {
		facts = append(facts, f.Label+" "+f.Value)
	}
	if len(facts) > 0 {
		line += "；" + strings.Join(facts, "，")
	}
	return line
}

func (p *plugin) toolQuote(ctx context.Context, symbols []string) (string, error) {
	var refs []Ref
	var bad []string
	if len(symbols) == 0 {
		if refs = p.watchlist(); len(refs) == 0 {
			return "用户的自选是空的；给 symbols 查具体的代码", nil
		}
	}
	for _, s := range symbols {
		if r, ok := p.srcs.parse(s); ok {
			refs = append(refs, r)
		} else {
			bad = append(bad, s)
		}
	}
	if len(refs) > watchMost {
		refs = refs[:watchMost]
	}
	qs, err := p.quotes(ctx, refs, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "行情，%s：\n", time.Now().UTC().Format(time.RFC3339))
	for _, r := range refs {
		if q, ok := qs[r]; ok {
			b.WriteString(quoteLine(r, q) + "\n")
		} else {
			fmt.Fprintf(&b, "- %s：查不到这个代码\n", r)
		}
	}
	for _, s := range bad {
		fmt.Fprintf(&b, "- 「%s」不像能查的代码\n", clip(s, 40))
	}
	return b.String(), nil
}

func (p *plugin) toolSearch(ctx context.Context, query string, limit int) (string, error) {
	q := cleanQuery(query)
	if q == "" {
		return "", fmt.Errorf("query 要给名称或代码")
	}
	hits, err := p.search(ctx, q, limit, false)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("没有和「%s」对得上的（%s）", q, p.srcs.markets()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "和「%s」对得上的 %d 个：\n", q, len(hits))
	for _, h := range hits {
		line := quoteLine(h.Ref, h.Quote)
		if h.Quote.Kind != "" {
			line += "，" + h.Quote.Kind
		}
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}

func (p *plugin) toolCandles(ctx context.Context, symbol, bar string, limit int) (string, error) {
	r, ok := p.srcs.parse(symbol)
	if !ok {
		return "", fmt.Errorf("「%s」不像能查的代码，写成 BTC-USDT 或 okx:BTC-USDT 这样", clip(symbol, 40))
	}
	src := p.srcs.get(r.Source)
	if bar == "" {
		bar = p.bar(src)
	}
	if !slices.Contains(src.Bars(), bar) {
		return "", fmt.Errorf("%s 没有 %q 周期的 K 线，能用：%s", src.Name(), bar, strings.Join(src.Bars(), "、"))
	}
	cs, err := p.candles(ctx, r, bar, limit)
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return fmt.Sprintf("%s 没给 %s 的 %s K 线", src.Name(), r, bar), nil
	}
	loc := time.Local
	if p.loc != nil {
		loc = p.loc
	}
	zone := loc.String()
	if loc == time.Local { // "Local" says nothing: the offset does
		zone = "UTC" + time.Now().In(loc).Format("-07:00")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的 %s K 线，最近 %d 根（时间是 %s）：时间 开 高 低 收 量\n", r, bar, len(cs), zone)
	for _, c := range cs {
		fmt.Fprintf(&b, "%s %s %s %s %s %s\n", c.At.In(loc).Format("2006-01-02 15:04"), fmtFloat(c.Open), fmtFloat(c.High),
			fmtFloat(c.Low), fmtFloat(c.Close), fmtCompactFloat(c.Vol))
	}
	return b.String(), nil
}
