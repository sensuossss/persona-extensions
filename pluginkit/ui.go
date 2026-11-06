package pluginkit

import (
	"encoding/json"
	"math"
	"time"
)

// Declarative UI (docs/reference.md#process-protocol-and-views): a plugin answers ui.render with a View the apps
// draw with their own components, and ui.action with what a button did. Where it is drawn (the slot) and for whom
// (time zone, language, platform, the UI version the app draws) comes with each request.

// UIVersion is the UI this package writes: 2 has the slots nav_page, overview_card, machine_section and inbox_item, the
// items chart, stat_grid and segmented, and refresh_after. An app of version 1 is never sent what it cannot draw (the hub
// leaves it out); a plugin may draw it something else (UIRequest.UIVersion).
const UIVersion = 2

// Sizes of what a plugin is asked to draw (UIRequest.Size).
const (
	SizeCompact = "compact" // a card (overview_card): at most 2 sections and 12 items, no fields
	SizeFull    = "full"
)

// UIRequest is ui.render's and ui.action's parameters.
type UIRequest struct {
	Contribution string            `json:"contribution"`
	State        string            `json:"state"`
	Action       string            `json:"action"`
	Values       map[string]string `json:"values"`
	// Placement is the slot: plugin_page, launcher_section, nav_page, overview_card, machine_section or inbox_item.
	Placement string `json:"placement"`
	Size      string `json:"size"`
	// UIVersion is what the app that asks draws (1 for apps from before UI slots, and from hubs before them: 0).
	UIVersion int `json:"ui_version"`
	// TZ is the user's time zone ("Asia/Shanghai"), "" when the hub does not know it; Locale the app's language;
	// Platform ios, mac, android or web.
	TZ       string `json:"tz"`
	Locale   string `json:"locale"`
	Platform string `json:"platform"`
	// Machine is set for a machine_section.
	Machine *UIMachine `json:"machine"`
	// Launcher is set for a launcher_section: the launcher's gateway, reached with its key through the hub.
	Launcher *struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		QueryID string `json:"query_id"`
	} `json:"launcher"`
	// Item is set for an inbox_item: the inbox item its page is drawn for.
	Item *UIInboxItem `json:"item"`
}

// UIInboxItem is the inbox item an inbox_item is drawn for, as the hub keeps it: the row's own fields (Count, how
// many messages it holds, and StartedAt, when the first came, zero when the hub does not say), the connector's Meta
// as it was stored (null when it has none: the hub never reads it), and Extra, the item's source blocks (lark, doc,
// moment, mail, connector) as the apps get them, for an item that has no meta.
type UIInboxItem struct {
	ID        string                     `json:"id"`
	Title     string                     `json:"title"`
	Summary   string                     `json:"summary"`
	Tier      string                     `json:"tier"`
	Reason    string                     `json:"reason"`
	Done      bool                       `json:"done"`
	Count     int                        `json:"count"`
	StartedAt time.Time                  `json:"started_at"`
	Meta      json.RawMessage            `json:"meta"`
	Extra     map[string]json.RawMessage `json:"extra"`
}

// UIMachine is the machine a machine_section is drawn for.
type UIMachine struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OS     string `json:"os"`
	Online bool   `json:"online"`
}

// Location is the user's time zone, else this machine's.
func (r UIRequest) Location() *time.Location {
	if r.TZ != "" {
		if loc, err := time.LoadLocation(r.TZ); err == nil {
			return loc
		}
	}
	return time.Local
}

// Draws says whether the app draws items of UI version v.
func (r UIRequest) Draws(v int) bool { return r.UIVersion >= v }

// Gateway is a launcher section's way to its gateway (Send puts the key in for KeyPlaceholder).
func (r UIRequest) Gateway() QuotaQuery {
	if r.Launcher == nil {
		return QuotaQuery{}
	}
	return QuotaQuery{Contribution: r.Contribution, QueryID: r.Launcher.QueryID, BaseURL: r.Launcher.BaseURL}
}

// View is what a plugin draws.
type View struct {
	Title    string    `json:"title,omitempty"`
	Sections []Section `json:"sections"`
	State    string    `json:"state,omitempty"`
	// RefreshAfter: the apps draw it again after this many seconds while it is on screen (30 at least; 0 never).
	RefreshAfter int `json:"refresh_after,omitempty"`
	// Search is a search box on top of the page (UI version 3): the app calls ui.action with its action and values
	// {"q": the text} once the user stops typing. Leave it out for an app that draws less (UIRequest.Draws(3)).
	Search *Search `json:"search,omitempty"`
}

// Search is a View's search box.
type Search struct {
	Placeholder string   `json:"placeholder,omitempty"`
	Value       string   `json:"value,omitempty"`
	Action      UIAction `json:"action"`
}

// Section is a group of items.
type Section struct {
	Title  string `json:"title,omitempty"`
	Footer string `json:"footer,omitempty"`
	Items  []Item `json:"items"`
}

// Item is one item (Persona's plugin.UIElem): text, metric, progress, row, button, link, field, divider; and, UI
// version 2, chart (Kind bar | line, Series, Unit, Stacked), stat_grid (Stats) and segmented (Name, Choices, Value,
// Action: one of a few, acted on when chosen, the choice in values[Name]).
type Item struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	Style       string        `json:"style,omitempty"`
	Label       string        `json:"label,omitempty"`
	Value       string        `json:"value,omitempty"`
	Detail      string        `json:"detail,omitempty"`
	Tone        string        `json:"tone,omitempty"`
	Ratio       *float64      `json:"ratio,omitempty"`
	Title       string        `json:"title,omitempty"`
	Subtitle    string        `json:"subtitle,omitempty"`
	Badge       *Badge        `json:"badge,omitempty"`
	Action      *UIAction     `json:"action,omitempty"`
	Role        string        `json:"role,omitempty"`
	URL         string        `json:"url,omitempty"`
	Name        string        `json:"name,omitempty"`
	Kind        string        `json:"kind,omitempty"`
	Choices     []Choice      `json:"choices,omitempty"`
	Placeholder string        `json:"placeholder,omitempty"`
	Series      []ChartSeries `json:"series,omitempty"`
	Unit        string        `json:"unit,omitempty"`
	Stacked     bool          `json:"stacked,omitempty"`
	Stats       []Stat        `json:"stats,omitempty"`
}

type Badge struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

type UIAction struct {
	ID      string `json:"id"`
	Confirm string `json:"confirm,omitempty"`
	Submit  bool   `json:"submit,omitempty"`
}

// Choice is one of a choice field's or a segmented's.
type Choice struct {
	Value string `json:"value"`
	Title string `json:"title,omitempty"`
}

// ChartSeries is one line or one color of bars; every series of a chart has the same labels in the same order. No
// Tone: the app's own series colors.
type ChartSeries struct {
	Name   string       `json:"name,omitempty"`
	Tone   string       `json:"tone,omitempty"`
	Points []ChartPoint `json:"points"`
}

// ChartPoint is a value at a label; Display is how to write it ("12k", "$3.40"), the value with the chart's unit
// when empty.
type ChartPoint struct {
	Label   string  `json:"label"`
	Value   float64 `json:"value"`
	Display string  `json:"display,omitempty"`
}

// Stat is one metric of a stat_grid; Tone colors its detail.
type Stat struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Tone   string `json:"tone,omitempty"`
}

// ActionResult is ui.action's answer.
type ActionResult struct {
	View    *View  `json:"view,omitempty"`
	Toast   string `json:"toast,omitempty"`
	OpenURL string `json:"open_url,omitempty"`
}

// Ratio is a pointer to r, clamped to 0…1 (progress items need one). NaN, as from 0 used of a 0 limit, is 0: JSON
// cannot carry it.
func Ratio(r float64) *float64 {
	if math.IsNaN(r) {
		r = 0
	}
	r = min(max(r, 0), 1)
	return &r
}

// Badge sets the badge on a nav_page's entry (no text clears it): the hub shows it on the apps' navigation. Send it
// again after initialize: the hub forgets a plugin's badges when it restarts.
func (h *Hub) Badge(contribution, text, tone string) error {
	return h.Notify("ui.badge", map[string]string{"contribution": contribution, "text": text, "tone": tone})
}
