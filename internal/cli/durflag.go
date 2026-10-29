package cli

import (
	"fmt"
	"strconv"
	"time"
)

// durFlag is a duration flag that also takes a bare number of seconds ("300" = "5m").
type durFlag struct{ d *time.Duration }

func newDurFlag(p *time.Duration, def time.Duration) *durFlag {
	*p = def
	return &durFlag{d: p}
}

func (f *durFlag) Set(s string) error {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f.d = time.Duration(n) * time.Second
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("want seconds (300) or a duration (5m): %q", s)
	}
	*f.d = d
	return nil
}

func (f *durFlag) String() string {
	if f.d == nil {
		return ""
	}
	return f.d.String()
}

func (f *durFlag) Type() string { return "duration" }
