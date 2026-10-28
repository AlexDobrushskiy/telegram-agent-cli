package cli

import (
	"strings"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseDate accepts ISO dates/times like Python's fromisoformat; no zone means UTC.
func parseDate(flag, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	norm := strings.Replace(s, " ", "T", 1)
	if strings.HasSuffix(norm, "z") {
		norm = norm[:len(norm)-1] + "Z"
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, norm); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, output.Usage("--%s: not an ISO date/time: %q", flag, s)
}
