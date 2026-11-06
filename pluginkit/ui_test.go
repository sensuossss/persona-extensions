package pluginkit

import (
	"encoding/json"
	"math"
	"testing"
)

func TestRatio(t *testing.T) {
	var used, limit float64 // 0 of 0: an unused or unlimited quota window
	for _, c := range []struct {
		in, want float64
	}{{0.25, 0.25}, {-1, 0}, {2, 1}, {math.Inf(1), 1}, {math.Inf(-1), 0}, {used / limit, 0}} {
		if got := *Ratio(c.in); got != c.want {
			t.Errorf("Ratio(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	v := View{Sections: []Section{{Items: []Item{{Type: "progress", Ratio: Ratio(used / limit)}}}}}
	if _, err := json.Marshal(v); err != nil {
		t.Errorf("a progress item for 0 of 0 does not encode: %v", err)
	}
}
