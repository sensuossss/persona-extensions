// Command homeassistant is a Persona extension for Home Assistant (docs/extensions/homeassistant.md): read the home's
// state and control its devices, through Home Assistant's REST API with a long-lived access token.
//
//   - ui (nav_page "home"): an entry on the apps' main navigation; the page shows the areas, each area's devices
//     with their state, a search across every entity, and an entity opened with its state, its controls (on / off,
//     brightness, temperature, open / close, …), its attributes and its recent history.
//   - connectors (id homeassistant): read tools (homeassistant_overview, homeassistant_search, homeassistant_entity,
//     homeassistant_history) for the assistant, and two write actions (homeassistant_call, and
//     homeassistant_secure_call for locks and alarms) that go through the hub's gate: the phone confirms them.
//
// Writes only ever call a service on named entities of a domain the configuration allows; a tap in the page is the
// user's own and calls the service directly.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var manifest []byte

const (
	id     = "homeassistant" // the connector's id and the extension's
	pageUI = "home"          // the ui contribution
)

var version = func() string {
	var m struct{ Version string }
	_ = json.Unmarshal(manifest, &m)
	return m.Version
}()

// Config is the extension's configuration (the manifest's form).
type Config struct {
	URL         string   `json:"url,omitempty"`
	InsecureTLS bool     `json:"insecure_tls,omitempty"`
	Writable    []string `json:"writable_domains,omitempty"`
	HiddenAreas []string `json:"hidden_areas,omitempty"`
}

// app is the running extension: the client and the settings, replaced as a whole by each initialize.
type app struct {
	logf func(string, ...any)

	mu       sync.Mutex
	ha       *ha
	cfg      Config
	writable []string
	cfgErr   error // what is wrong with the settings, for the status and the page
}

func newApp(logf func(string, ...any)) *app { return &app{logf: logf, ha: newHA("", "", false)} }

func (a *app) program() ext.Program {
	return ext.Program{ID: id, Version: version, Manifest: manifest, Init: a.init,
		Namespaces: map[string]ext.Handler{"connector": a.connector},
		UI:         map[string]ext.Page{pageUI: {Render: a.render, Action: a.action}}}
}

func (a *app) init(_ context.Context, in *ext.Init, _ *ext.Hub) (map[string]any, error) {
	var cfg Config
	if err := in.Config(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	a.configure(cfg, in.Secrets["token"])
	return nil, nil
}

// configure takes the settings and the token: an address that is not http(s) leaves the extension unconfigured,
// with the reason kept.
func (a *app) configure(cfg Config, token string) {
	base, err := baseURL(cfg.URL)
	writable := defaultWritable
	if cfg.Writable != nil {
		writable = nil
		for _, d := range cfg.Writable {
			if d = strings.ToLower(strings.TrimSpace(d)); domainRE.MatchString(d) {
				writable = append(writable, d)
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg, a.writable, a.cfgErr = cfg, writable, err
	if err != nil {
		base = ""
	}
	a.ha = newHA(base, cleanToken(token), cfg.InsecureTLS)
}

// cleanToken is the token as pasted, without spaces, quotes or a "Bearer " in front.
func cleanToken(t string) string {
	t = strings.Trim(strings.TrimSpace(t), "\"'")
	if len(t) > 7 && strings.EqualFold(t[:7], "bearer ") {
		t = strings.TrimSpace(t[7:])
	}
	return t
}

// tokenLooksRight says whether t has the shape of a long-lived access token: a JWT (three parts, two dots).
func tokenLooksRight(t string) bool {
	return strings.Count(t, ".") == 2 && len(t) >= 100 && !strings.ContainsAny(t, " \n\t")
}

// tokenShape says what a token looks like without saying what it is: its length, whether it starts as a JWT, its
// dots, and what kinds of characters it has, for a user checking what they pasted.
func tokenShape(t string) string {
	var kinds []string
	if strings.ContainsAny(t, " \t\n") {
		kinds = append(kinds, "有空白")
	}
	if strings.IndexFunc(t, func(r rune) bool { return r > 127 }) >= 0 {
		kinds = append(kinds, "有中文或全角字符")
	}
	start := "不是 eyJ 开头"
	if strings.HasPrefix(t, "eyJ") {
		start = "eyJ 开头"
	}
	out := fmt.Sprintf("%d 个字符，%s，%d 个点", len([]rune(t)), start, strings.Count(t, "."))
	if len(kinds) > 0 {
		out += "，" + strings.Join(kinds, "、")
	}
	return out
}

var domainRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// baseURL reads the configured address: http(s), a host, nothing after the path.
func baseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Home Assistant 的地址要写成 http://主机:8123 这样")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// client is the current client and settings.
func (a *app) client() (*ha, Config, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ha, a.cfg, a.writable
}

// canWrite says why domain's services may not be called (nil: they may). secure is whether the caller is the path
// for locks and alarms.
func (a *app) canWrite(domain string, secure bool) error {
	_, _, writable := a.client()
	switch {
	case !slices.Contains(writable, domain):
		return fmt.Errorf("「%s」（%s）不在扩展设置的可操作类型里", domainName(domain), domain)
	case slices.Contains(secureDomains, domain) && !secure:
		return fmt.Errorf("门锁和警戒要用 homeassistant_secure_call")
	case !slices.Contains(secureDomains, domain) && secure:
		return fmt.Errorf("homeassistant_secure_call 只管门锁和警戒；%s 用 homeassistant_call", domain)
	}
	return nil
}

// hidden says whether an area is hidden by the settings (by its name or id).
func hidden(cfg Config, ar Area) bool {
	for _, h := range cfg.HiddenAreas {
		h = strings.TrimSpace(h)
		if h != "" && (strings.EqualFold(h, ar.Name) || strings.EqualFold(h, ar.ID)) {
			return true
		}
	}
	return false
}

func main() {
	log.SetFlags(0) // to stderr: stdout is the protocol
	a := newApp(log.Printf)
	ext.Main(a.program())
}
