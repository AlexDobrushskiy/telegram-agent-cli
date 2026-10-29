package cli

import (
	"testing"
	"time"
)

func TestDurFlag(t *testing.T) {
	var d time.Duration
	f := newDurFlag(&d, time.Minute)
	if d != time.Minute {
		t.Fatalf("default = %v", d)
	}
	for in, want := range map[string]time.Duration{"300": 5 * time.Minute, "0": 0, "5m": 5 * time.Minute, "90s": 90 * time.Second, "1h30m": 90 * time.Minute} {
		if err := f.Set(in); err != nil || d != want {
			t.Errorf("Set(%q) = %v, %v; want %v", in, d, err, want)
		}
	}
	for _, bad := range []string{"", "5 min", "abc"} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
	}
}
