package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

func TestAlertsManifest(t *testing.T) {
	m, err := pluginkit.CheckManifest(manifest)
	if err != nil || len(m.Connectors) != 1 || !m.Connectors[0].Fallback {
		t.Fatalf("inbox manifest: %+v %v", m, err)
	}
	if err := pluginkit.CheckStandardSettings(manifest); err != nil {
		t.Error(err)
	}
}

func TestAlerts(t *testing.T) {
	ft, fo := newFakeTencent(t), newFakeOKX(t)
	p := newPlugin(t.Logf)
	p.srcs = append(sources{newOKX(fo.srv.URL)}, tencentSources(ft.hosts())...)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return time.Date(2026, 11, 6, 11, 40, 0, 0, marketInfo[mCN].loc) }
	dir := t.TempDir()
	var ip pluginkit.InitParams
	ip.Plugin.DataDir = dir
	ip.Config = []byte(`{"triage_rules": "都需要我"}`)
	if err := p.configure(ip); err != nil {
		t.Fatal(err)
	}
	var woken atomic.Int32
	p.alerts.wake = func() { woken.Add(1) }

	// the form on the instrument page
	r := act(t, p, "", "open:cn:600519.SH", nil)
	if got := texts(*r.View); !strings.Contains(got, "## 价格提醒") || !strings.Contains(got, "→alert:cn:600519.SH") {
		t.Fatalf("no alert form:\n%s", got)
	}
	st := r.View.State
	set := func(kind, value string) string {
		return act(t, p, st, "alert:cn:600519.SH", map[string]string{"alert_kind": kind, "alert_value": value}).Toast
	}
	// 1440.49 now, -0.79%: one already met is refused, so is a bad number or kind
	for _, c := range [][2]string{{"above", "1400"}, {"below", "1500"}, {"move", "0.5"}, {"above", "x"}, {"above", "-3"}, {"sideways", "1"}, {"move", "300"}} {
		if toast := set(c[0], c[1]); strings.HasPrefix(toast, "已设") {
			t.Errorf("%v accepted: %s", c, toast)
		}
	}
	if toast := set("above", "1450"); toast != "已设提醒：600519.SH 涨到 1450.00" {
		t.Fatalf("above: %q", toast)
	}
	if toast := set("move", "3%"); toast != "已设提醒：600519.SH 涨跌幅超 3%" {
		t.Fatalf("move: %q", toast)
	}
	if toast := set("above", "1450"); !strings.Contains(toast, "已经有") {
		t.Errorf("a repeat: %q", toast)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "alerts.json")); !bytes.Contains(raw, []byte(`"kind": "above"`)) {
		t.Errorf("not kept: %s", raw)
	}
	// nothing met: no thing, no wake
	p.checkAlerts(context.Background(), 0)
	if woken.Load() != 0 || len(collect(t, p)) != 0 {
		t.Fatalf("went off early")
	}

	// the price rises past 1450: the above one goes off, once
	ft.quotes["sh600519"] = bytes.Replace(ft.quotes["sh600519"], []byte("~1440.49~"), []byte("~1460.00~"), 1)
	p.checkAlerts(context.Background(), 0)
	p.checkAlerts(context.Background(), 0)
	if woken.Load() != 1 {
		t.Errorf("woken %d times", woken.Load())
	}
	things := collect(t, p)
	if len(things) != 1 {
		t.Fatalf("things %+v", things)
	}
	th := things[0]
	if th.Kind != alertKind || th.Tier != pluginkit.TierAction || th.Extra.RuleTier != pluginkit.TierAction || th.Rules != "都需要我" ||
		th.Title != "贵州茅台 600519.SH 涨到 1450.00" || !strings.HasPrefix(th.Preview, "贵州茅台 600519.SH 现价 1460.00，+0.55%") ||
		th.URL != "https://gu.qq.com/sh600519" || !strings.HasPrefix(th.Thread, "alert:") {
		t.Errorf("thing %+v", th)
	}
	raw, _ := json.Marshal(things)
	if err := pluginkit.CheckThings(raw, "都需要我", true); err != nil {
		t.Errorf("the hub's rules: %v", err)
	}

	// both on the list page, the met one marked; deleted from the instrument page
	v := render(t, p, "", 5)
	got := texts(v)
	if !strings.Contains(got, "## 价格提醒 · 2") || !strings.Contains(got, "row 贵州茅台 涨到 1450.00 已提醒") || !strings.Contains(got, "[到了 caution]") {
		t.Errorf("list:\n%s", got)
	}
	id := strings.TrimPrefix(th.Thread, "alert:")
	if toast := act(t, p, st, "unalert:"+id, nil).Toast; toast != "已删掉 600519.SH 的提醒" {
		t.Errorf("delete: %q", toast)
	}
	if len(collect(t, p)) != 0 || len(p.alerts.all()) != 1 {
		t.Errorf("after delete: %v", p.alerts.all())
	}
	// kept across a restart
	q := newPlugin(t.Logf)
	q.srcs = p.srcs
	if err := q.configure(ip); err != nil || len(q.alerts.all()) != 1 {
		t.Errorf("reloaded: %v %v", q.alerts.all(), err)
	}
}

func collect(t *testing.T, p *plugin) []pluginkit.InboxThing[pluginkit.ThingExtra] {
	t.Helper()
	out, err := p.cInboxCollect(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["things"].([]pluginkit.InboxThing[pluginkit.ThingExtra])
}
