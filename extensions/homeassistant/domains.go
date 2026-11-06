package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// What the extension knows about Home Assistant's domains: their names, which are devices one controls (listed on
// the page by default), which may be changed at all, which only with the phone's confirmation, and the controls the
// page offers for an entity.

// defaultWritable are the domains whose services may be called, unless the configuration says otherwise. Left out
// on purpose: homeassistant, hassio, shell_command, rest_command, recorder, system_log, notify, … (they act on the
// whole system, run commands or reach outside), and anything that is only read.
var defaultWritable = []string{"light", "switch", "fan", "cover", "climate", "media_player", "vacuum", "humidifier",
	"water_heater", "input_boolean", "input_number", "input_select", "number", "select", "button", "input_button",
	"scene", "script", "remote", "siren", "valve", "lawn_mower", "lock", "alarm_control_panel"}

// secureDomains open doors or disarm alarms: the assistant reaches them only through homeassistant_secure_call,
// which the phone always confirms (confirm_always), and the page asks before each tap.
var secureDomains = []string{"lock", "alarm_control_panel"}

// deviceDomains are what the page lists as devices (the rest, sensors and the like, through search and areas).
var deviceDomains = []string{"light", "switch", "fan", "cover", "climate", "media_player", "vacuum", "humidifier",
	"water_heater", "lock", "alarm_control_panel", "scene", "script", "input_boolean", "valve", "lawn_mower", "siren", "remote"}

// sensorDomains are read-only things worth a look.
var sensorDomains = []string{"sensor", "binary_sensor", "weather", "person", "device_tracker"}

var domainNames = map[string]string{
	"light": "灯", "switch": "开关", "fan": "风扇", "cover": "窗帘/门", "climate": "空调/温控", "media_player": "播放器",
	"vacuum": "扫地机", "humidifier": "加湿器", "water_heater": "热水器", "lock": "门锁", "alarm_control_panel": "警戒",
	"scene": "场景", "script": "脚本", "input_boolean": "开关（辅助）", "valve": "阀门", "lawn_mower": "割草机", "siren": "警报器",
	"remote": "遥控", "sensor": "传感器", "binary_sensor": "传感器", "weather": "天气", "person": "人", "device_tracker": "位置",
	"number": "数值", "select": "选项", "button": "按钮", "input_number": "数值（辅助）", "input_select": "选项（辅助）", "camera": "摄像头",
	"automation": "自动化", "update": "更新", "event": "事件",
}

func domainName(d string) string {
	if n, ok := domainNames[d]; ok {
		return n
	}
	return d
}

var stateNames = map[string]string{
	"on": "开", "off": "关", "open": "开着", "opening": "正在打开", "closed": "关着", "closing": "正在关闭", "stopped": "停了",
	"playing": "播放中", "paused": "暂停", "idle": "空闲", "standby": "待机", "buffering": "缓冲中",
	"locked": "已上锁", "unlocked": "没锁", "locking": "正在上锁", "unlocking": "正在开锁", "jammed": "卡住了",
	"heat": "制热", "cool": "制冷", "heat_cool": "自动", "auto": "自动", "dry": "除湿", "fan_only": "送风",
	"cleaning": "清扫中", "docked": "在充电座", "returning": "回充中", "error": "出错",
	"home": "在家", "not_home": "不在家", "unavailable": "离线", "unknown": "未知",
	"armed_home": "在家警戒", "armed_away": "离家警戒", "armed_night": "夜间警戒", "disarmed": "已撤防", "triggered": "已触发",
	"scening": "", "above_horizon": "白天", "below_horizon": "夜里",
}

// stateText is how the page writes an entity's state: localized, with its unit, numbers tidied.
func stateText(s State) string {
	if s.Domain() == "scene" || s.Domain() == "script" || s.Domain() == "button" || s.Domain() == "input_button" {
		if s.Domain() == "script" && s.State == "on" {
			return "运行中"
		}
		return ""
	}
	if n, ok := stateNames[s.State]; ok {
		if s.Domain() == "binary_sensor" && (s.State == "on" || s.State == "off") {
			return binaryText(s)
		}
		return n
	}
	if f, err := strconv.ParseFloat(s.State, 64); err == nil {
		v := tidy(f)
		if u := s.attr("unit_of_measurement"); u != "" {
			v += " " + u
		}
		return v
	}
	return clip(s.State, 40)
}

// binaryText reads a binary sensor by its device class: a door is 开着 / 关着, motion 有人 / 没人.
func binaryText(s State) string {
	on := s.State == "on"
	pick := func(a, b string) string {
		if on {
			return a
		}
		return b
	}
	switch s.attr("device_class") {
	case "door", "window", "opening", "garage_door":
		return pick("开着", "关着")
	case "motion", "occupancy", "presence":
		return pick("有人", "没人")
	case "moisture":
		return pick("有水", "干")
	case "battery":
		return pick("电量低", "正常")
	case "connectivity":
		return pick("在线", "离线")
	case "lock":
		return pick("没锁", "已上锁")
	case "smoke", "gas", "carbon_monoxide", "problem", "safety":
		return pick("报警", "正常")
	}
	return pick("开", "关")
}

func tidy(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	s := strconv.FormatFloat(f, 'f', 2, 64)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

// stateTone colors a state: on / open / playing positive, alarms danger, offline caution.
func stateTone(s State) string {
	switch {
	case s.Unavailable():
		return "caution"
	case s.State == "triggered" || s.State == "jammed" || s.State == "error":
		return "danger"
	case s.Domain() == "binary_sensor" && s.State == "on" && slices.Contains([]string{"smoke", "gas", "carbon_monoxide", "moisture", "problem", "safety"}, s.attr("device_class")):
		return "danger"
	case s.Domain() == "lock" && s.State == "unlocked":
		return "caution"
	case s.State == "on" || s.State == "open" || s.State == "playing" || s.State == "cleaning" || s.State == "home" ||
		slices.Contains([]string{"heat", "cool", "heat_cool", "auto", "dry", "fan_only"}, s.State):
		return "positive"
	}
	return "neutral"
}

// detail is a short line of what else matters about an entity: brightness, target temperature, position, volume.
func detail(s State) string {
	var parts []string
	switch s.Domain() {
	case "light":
		if b, ok := s.num("brightness"); ok && s.State == "on" {
			parts = append(parts, fmt.Sprintf("亮度 %d%%", int(math.Round(b/255*100))))
		}
	case "climate":
		if t, ok := s.num("current_temperature"); ok {
			parts = append(parts, "室温 "+tidy(t)+"°")
		}
		if t, ok := s.num("temperature"); ok && s.State != "off" {
			parts = append(parts, "设定 "+tidy(t)+"°")
		}
	case "cover":
		if p, ok := s.num("current_position"); ok {
			parts = append(parts, "开度 "+tidy(p)+"%")
		}
	case "media_player":
		if t := s.attr("media_title"); t != "" {
			parts = append(parts, clip(t, 30))
		}
		if v, ok := s.num("volume_level"); ok && s.State != "off" {
			parts = append(parts, fmt.Sprintf("音量 %d%%", int(math.Round(v*100))))
		}
	case "vacuum", "humidifier", "fan":
		if b, ok := s.num("battery_level"); ok {
			parts = append(parts, "电量 "+tidy(b)+"%")
		}
		if p, ok := s.num("percentage"); ok && s.State == "on" {
			parts = append(parts, "风速 "+tidy(p)+"%")
		}
	}
	return strings.Join(parts, " · ")
}

// control is one button (or a choice) the page offers for an entity: the service it calls with what data.
type control struct {
	Title   string
	Service string
	Data    map[string]any
	Role    string // destructive for the ones that open or disarm
}

// controls are the page's buttons for s, by domain and state. Empty: nothing to press (sensors, or a domain not
// writable).
func controls(s State) []control {
	on := s.State == "on"
	toggle := func() []control {
		if on {
			return []control{{Title: "关", Service: "turn_off"}}
		}
		return []control{{Title: "开", Service: "turn_on"}}
	}
	switch s.Domain() {
	case "light":
		cs := toggle()
		modes := s.list("supported_color_modes")
		if len(modes) > 0 && !(len(modes) == 1 && modes[0] == "onoff") {
			for _, pct := range []int{20, 50, 100} {
				cs = append(cs, control{Title: fmt.Sprintf("亮度 %d%%", pct), Service: "turn_on", Data: map[string]any{"brightness_pct": pct}})
			}
		}
		return cs
	case "switch", "fan", "humidifier", "input_boolean", "siren", "remote", "automation":
		return toggle()
	case "cover", "valve":
		verb := "cover"
		if s.Domain() == "valve" {
			verb = "valve"
		}
		return []control{{Title: "打开", Service: "open_" + verb}, {Title: "关上", Service: "close_" + verb}, {Title: "停", Service: "stop_" + verb}}
	case "climate":
		var cs []control
		if s.State == "off" {
			cs = append(cs, control{Title: "开", Service: "turn_on"})
		} else {
			cs = append(cs, control{Title: "关", Service: "turn_off"})
		}
		if t, ok := s.num("temperature"); ok {
			step := 1.0
			if st, ok := s.num("target_temp_step"); ok && st > 0 {
				step = st
			}
			lo, hi := s.attrNum("min_temp", 7), s.attrNum("max_temp", 35)
			if t-step >= lo {
				cs = append(cs, control{Title: "调到 " + tidy(t-step) + "°", Service: "set_temperature", Data: map[string]any{"temperature": t - step}})
			}
			if t+step <= hi {
				cs = append(cs, control{Title: "调到 " + tidy(t+step) + "°", Service: "set_temperature", Data: map[string]any{"temperature": t + step}})
			}
		}
		return cs
	case "media_player":
		if s.State == "off" {
			return []control{{Title: "开", Service: "turn_on"}}
		}
		return []control{{Title: "播放 / 暂停", Service: "media_play_pause"}, {Title: "音量 +", Service: "volume_up"},
			{Title: "音量 −", Service: "volume_down"}, {Title: "关", Service: "turn_off"}}
	case "vacuum":
		return []control{{Title: "开始清扫", Service: "start"}, {Title: "暂停", Service: "pause"}, {Title: "回充", Service: "return_to_base"}}
	case "lawn_mower":
		return []control{{Title: "开始", Service: "start_mowing"}, {Title: "暂停", Service: "pause"}, {Title: "回去", Service: "dock"}}
	case "lock":
		if s.State == "locked" {
			return []control{{Title: "开锁", Service: "unlock", Role: "destructive"}}
		}
		return []control{{Title: "上锁", Service: "lock"}}
	case "alarm_control_panel":
		if s.State == "disarmed" {
			return []control{{Title: "在家警戒", Service: "alarm_arm_home"}, {Title: "离家警戒", Service: "alarm_arm_away"}}
		}
		return []control{{Title: "撤防", Service: "alarm_disarm", Role: "destructive"}}
	case "scene":
		return []control{{Title: "执行", Service: "turn_on"}}
	case "script":
		if on {
			return []control{{Title: "停下", Service: "turn_off"}}
		}
		return []control{{Title: "运行", Service: "turn_on"}}
	case "button", "input_button":
		return []control{{Title: "按一下", Service: "press"}}
	case "water_heater":
		return toggle()
	}
	return nil
}

func (s State) attrNum(k string, def float64) float64 {
	if v, ok := s.num(k); ok {
		return v
	}
	return def
}

// options are the choices of a select / input_select (set with select_option), or nil.
func options(s State) []string {
	if s.Domain() == "select" || s.Domain() == "input_select" {
		return s.list("options")
	}
	return nil
}

// numberRange is a number / input_number's min, max and step, and whether it is one.
func numberRange(s State) (lo, hi, step float64, ok bool) {
	if s.Domain() != "number" && s.Domain() != "input_number" {
		return 0, 0, 0, false
	}
	return s.attrNum("min", 0), s.attrNum("max", 100), s.attrNum("step", 1), true
}

// hvacModes are a climate's modes when it has a few (the page offers them as a choice).
func hvacModes(s State) []string {
	if s.Domain() != "climate" {
		return nil
	}
	return s.list("hvac_modes")
}
