//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

type waitResult struct {
	Initialized bool         `json:"initialized"`
	TimedOut    bool         `json:"timed_out"`
	Polls       int          `json:"polls"`
	Messages    []rawMessage `json:"messages"`
}

// rawMessage keeps a message opaque: only a few fields are checked.
type rawMessage = map[string]any

func TestWaitNew(t *testing.T) {
	state := filepath.Join(t.TempDir(), "wm.json")
	var first waitResult
	ok(t, &first, "messages", "wait-new", "--state", state)
	if !first.Initialized || len(first.Messages) != 0 {
		t.Fatalf("first run: %+v", first)
	}
	if st, err := os.Stat(state); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", st, err)
	}
	// Two polls with the lock released in between; new messages may legitimately arrive.
	var w waitResult
	ok(t, &w, "messages", "wait-new", "--state", state, "--timeout", "6", "--interval", "5")
	if w.Initialized || w.Polls < 1 || (len(w.Messages) == 0 && !w.TimedOut) {
		t.Fatalf("wait: %+v", w)
	}
	for _, m := range w.Messages {
		if m["from_me"] == true {
			t.Fatalf("wait-new returned an own message: %v", m)
		}
	}
}

type watchList struct {
	Watchers []struct {
		Name         string `json:"name"`
		SendsLastDay int    `json:"sends_last_day"`
	} `json:"watchers"`
}

func sendsLastDay(t *testing.T, name string) int {
	var l watchList
	ok(t, &l, "watch", "list")
	for _, w := range l.Watchers {
		if w.Name == name {
			return w.SendsLastDay
		}
	}
	return -1
}

// TestWatchReplyDryRun needs a watcher in the registry: TGAC_E2E_WATCHER (name),
// TGAC_E2E_WATCHER_CHAT (chat id), optional TGAC_E2E_REPLY_TO. For a full pass the
// session env must match the entry's session binding.
func TestWatchReplyDryRun(t *testing.T) {
	ok(t, nil, "watch", "list")
	name, chat := os.Getenv("TGAC_E2E_WATCHER"), os.Getenv("TGAC_E2E_WATCHER_CHAT")
	if name == "" || chat == "" {
		t.Skip("set TGAC_E2E_WATCHER and TGAC_E2E_WATCHER_CHAT to dry-run a reply")
	}
	var check struct {
		Valid   bool `json:"valid"`
		Watcher struct {
			SessionBound  bool `json:"session_bound"`
			CallerMatches bool `json:"caller_matches"`
		} `json:"watcher"`
	}
	ok(t, &check, "watch", "check", "--watcher", name)
	if !check.Valid || !check.Watcher.SessionBound {
		t.Fatalf("registry entry %q is not valid: %+v", name, check)
	}
	before := sendsLastDay(t, name)
	args := []string{"watch", "reply", "--watcher", name, "--chat", chat, "--text", "e2e dry run", "--dry-run"}
	if r := os.Getenv("TGAC_E2E_REPLY_TO"); r != "" {
		args = append(args, "--reply-to", r)
	}
	code, env := run(t, args...)
	switch {
	case code == 0 && !check.Watcher.CallerMatches:
		t.Fatalf("dry run passed although the caller does not match the session binding")
	case code == 0:
		t.Logf("would send: %s", env.Data)
	case code == 7 && env.Error.Code == "watcher_refused":
		t.Logf("refused (a valid outcome): %s", env.Error.Message)
	default:
		t.Fatalf("dry run: exit %d %+v", code, env.Error)
	}
	if after := sendsLastDay(t, name); after != before {
		t.Fatalf("dry run logged a send (%d → %d)", before, after)
	}
}
