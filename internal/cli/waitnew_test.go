package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
)

const (
	wDM    = int64(101)
	wDM2   = int64(102)
	wGroup = int64(-201)
	wChan  = int64(-1000000000301)
)

type waitHarness struct {
	*harness
	clock   time.Time
	state   string
	onSleep []func() // run in order, one per sleep
	sleeps  int
	nextID  map[int64]int
}

func newWaitHarness(t *testing.T) *waitHarness {
	h := &waitHarness{harness: newHarness(t), clock: t0.Add(24 * time.Hour), nextID: map[int64]int{}}
	h.fake = tgfake.New(tgclient.User{ID: 1, Name: "Owner", Type: "user"})
	h.fake.Recency = true
	h.fake.AddDialog(tgclient.Dialog{ID: wDM, Name: "Dee Em", Type: "user"})
	h.fake.AddDialog(tgclient.Dialog{ID: wDM2, Name: "Second Person", Type: "user"})
	h.fake.AddDialog(tgclient.Dialog{ID: wGroup, Name: "Some Group", Type: "group"})
	h.fake.AddDialog(tgclient.Dialog{ID: wChan, Name: "Some Channel", Type: "channel"})
	for _, c := range []int64{wDM, wDM2, wGroup, wChan} {
		h.msg(c, false, "old", -time.Hour)
	}
	h.app.Now = func() time.Time { return h.clock }
	h.app.Sleep = func(_ context.Context, d time.Duration) error {
		h.clock = h.clock.Add(d)
		if h.sleeps < len(h.onSleep) {
			h.onSleep[h.sleeps]()
		}
		h.sleeps++
		return nil
	}
	h.state = filepath.Join(t.TempDir(), "wm.json")
	return h
}

// msg adds a message to chat, dated relative to the clock.
func (h *waitHarness) msg(chat int64, fromMe bool, text string, ago time.Duration, opts ...func(*tgclient.Message)) int {
	h.nextID[chat]++
	id := h.nextID[chat]
	sender := chat
	if fromMe {
		sender = 1
	} else if chat < 0 {
		sender = 55
	}
	m := tgclient.Message{ID: id, ChatID: chat, FromMe: fromMe, SenderID: &sender, Text: text, Time: h.clock.Add(ago)}
	for _, o := range opts {
		o(&m)
	}
	h.fake.AddMessage(m)
	return id
}

func (h *waitHarness) wait(args ...string) map[string]any {
	h.t.Helper()
	return h.ok(append([]string{"messages", "wait-new", "--state", h.state}, args...)...)
}

func (h *waitHarness) watermark() map[string]int {
	h.t.Helper()
	b, err := os.ReadFile(h.state)
	if err != nil {
		h.t.Fatal(err)
	}
	var st struct {
		Chats map[string]int `json:"chats"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		h.t.Fatal(err)
	}
	return st.Chats
}

func texts(v any) []string {
	var out []string
	for _, m := range list(v) {
		out = append(out, m["text"].(string))
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return eq(a, b)
}

func TestWaitNewFirstRunInitialises(t *testing.T) {
	h := newWaitHarness(t)
	d := h.wait()
	if d["initialized"] != true || len(list(d["messages"])) != 0 || h.connects != 1 || h.sleeps != 0 {
		t.Fatalf("first run = %v (connects %d sleeps %d)", d, h.connects, h.sleeps)
	}
	wm := h.watermark()
	for _, c := range []int64{wDM, wDM2, wGroup, wChan} {
		if wm[strconv.FormatInt(c, 10)] != 1 {
			t.Fatalf("watermark %v", wm)
		}
	}
	if st, _ := os.Stat(h.state); st.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v", st.Mode())
	}
}

func TestWaitNewFirstRunSince(t *testing.T) {
	h := newWaitHarness(t)
	h.msg(wDM, false, "recent", -10*time.Minute)
	h.msg(wDM, true, "mine", -9*time.Minute)
	d := h.wait("--since", tgclient.ISO(h.clock.Add(-30*time.Minute)))
	if got := texts(d["messages"]); !eq(got, []string{"recent"}) || d["initialized"] != true {
		t.Fatalf("since = %v", d)
	}
	if h.watermark()[strconv.FormatInt(wDM, 10)] != 3 {
		t.Fatalf("watermark %v", h.watermark())
	}
}

func TestWaitNewArrivesOnSecondPoll(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.connects = 0
	h.onSleep = []func(){func() { h.msg(wDM, false, "hello", 0) }}
	d := h.wait("--timeout", "600", "--interval", "30")
	if got := texts(d["messages"]); !eq(got, []string{"hello"}) {
		t.Fatalf("messages = %v", d)
	}
	if d["polls"].(float64) != 2 || h.connects != 2 || d["timed_out"] != false {
		t.Fatalf("polls %v connects %d (one connection, so one lock, per poll)", d["polls"], h.connects)
	}
	if h.watermark()[strconv.FormatInt(wDM, 10)] != 2 {
		t.Fatalf("watermark not advanced: %v", h.watermark())
	}
	// Returned once: the next wait sees nothing.
	d = h.wait("--timeout", "0")
	if len(list(d["messages"])) != 0 || d["timed_out"] != true {
		t.Fatalf("second wait = %v", d)
	}
	if h.fake.Calls["SendText"] != 0 {
		t.Fatal("wait-new sent something")
	}
}

func TestWaitNewIgnoresOwnAndServiceMessages(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.msg(wDM, true, "mine", 0)
	h.msg(wGroup, false, "", 0, func(m *tgclient.Message) { m.Service = "chat_add_user" })
	d := h.wait("--timeout", "60", "--interval", "30")
	if len(list(d["messages"])) != 0 || d["timed_out"] != true || d["polls"].(float64) != 3 {
		t.Fatalf("own/service = %v", d)
	}
	wm := h.watermark()
	if wm[strconv.FormatInt(wDM, 10)] != 2 || wm[strconv.FormatInt(wGroup, 10)] != 2 {
		t.Fatalf("watermark must absorb own messages: %v", wm)
	}
}

func TestWaitNewTimeout(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	start := h.clock
	d := h.wait("--timeout", "100", "--interval", "30")
	// Polls at 0, 30, 60, 90 and the deadline (100).
	if d["timed_out"] != true || d["polls"].(float64) != 5 || h.clock.Sub(start) != 100*time.Second {
		t.Fatalf("timeout = %v after %v", d, h.clock.Sub(start))
	}
}

func TestWaitNewFilters(t *testing.T) {
	mention := func(m *tgclient.Message) { m.Mentioned = true }
	setup := func(t *testing.T) *waitHarness {
		h := newWaitHarness(t)
		h.wait()
		mine := h.msg(wGroup, true, "my group post", -time.Minute)
		h.msg(wDM, false, "dm", 0)
		h.msg(wGroup, false, "group plain", 0)
		h.msg(wGroup, false, "group mention", 0, mention)
		h.msg(wGroup, false, "group reply to me", 0, func(m *tgclient.Message) { m.ReplyTo = mine })
		h.msg(wChan, false, "channel post", 0)
		return h
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"all", nil, []string{"dm", "group plain", "group mention", "group reply to me", "channel post"}},
		{"dms", []string{"--dms"}, []string{"dm"}},
		{"groups", []string{"--groups"}, []string{"group plain", "group mention", "group reply to me"}},
		{"mentions", []string{"--mentions"}, []string{"group mention", "group reply to me"}},
		{"mentions+dms", []string{"--mentions", "--dms"}, []string{"dm", "group mention", "group reply to me"}},
		{"mentions+groups", []string{"--mentions", "--groups"}, []string{"group mention", "group reply to me"}},
		{"chats", []string{"--chats", strconv.FormatInt(wChan, 10) + "," + strconv.FormatInt(wDM, 10)}, []string{"dm", "channel post"}},
		{"chats+dms", []string{"--chats", strconv.FormatInt(wChan, 10), "--dms"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := setup(t)
			d := h.wait(append([]string{"--timeout", "0"}, c.args...)...)
			if got := texts(d["messages"]); !sameSet(got, c.want) {
				t.Fatalf("%v: got %v want %v", c.args, got, c.want)
			}
		})
	}
}

func TestWaitNewFetchesOnlyChangedChats(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.msg(wDM2, false, "only here", 0)
	before := h.fake.Calls["History"]
	d := h.wait("--timeout", "0")
	if got := texts(d["messages"]); !eq(got, []string{"only here"}) || h.fake.Calls["History"]-before != 1 {
		t.Fatalf("got %v with %d history calls", got, h.fake.Calls["History"]-before)
	}
}

func TestWaitNewStopsScanAtOldDialogs(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	// A dialog older than the newest date already seen cannot be new: the scan stops before it.
	old := int64(103)
	h.fake.AddDialog(tgclient.Dialog{ID: old, Name: "Ancient", Type: "user"})
	h.msg(old, false, "ancient", -48*time.Hour)
	h.msg(wDM, false, "fresh", 0)
	d := h.wait("--timeout", "0")
	if got := texts(d["messages"]); !eq(got, []string{"fresh"}) {
		t.Fatalf("got %v", got)
	}
	if _, seen := h.watermark()[strconv.FormatInt(old, 10)]; seen {
		t.Fatal("scan did not stop at dialogs older than the watermark's newest date")
	}
}

func TestWaitNewUnknownChat(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.fake.AddDialog(tgclient.Dialog{ID: 104, Name: "New Person", Type: "user"})
	h.msg(104, false, "first contact", 0)
	d := h.wait("--timeout", "0")
	if got := texts(d["messages"]); !eq(got, []string{"first contact"}) {
		t.Fatalf("got %v", got)
	}
}

func TestWaitNewTruncated(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	for i := 0; i < 120; i++ {
		h.msg(wDM, false, "flood", 0)
	}
	d := h.wait("--timeout", "0")
	if len(list(d["messages"])) != 100 || len(d["truncated_chats"].([]any)) != 1 {
		t.Fatalf("truncation: %d msgs, %v", len(list(d["messages"])), d["truncated_chats"])
	}
}

func TestWaitNewSkipsBusyLock(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.msg(wDM, false, "after busy", 0)
	inner := h.app.Connect
	busy := 1
	h.app.Connect = func(ctx context.Context, o tgclient.Options, fn func(context.Context, tgclient.Telegram) error) error {
		if busy > 0 {
			busy--
			return output.LockBusy("held")
		}
		return inner(ctx, o, fn)
	}
	d := h.wait("--timeout", "60", "--interval", "30")
	if got := texts(d["messages"]); !eq(got, []string{"after busy"}) || d["polls"].(float64) != 2 {
		t.Fatalf("busy lock: %v", d)
	}
}

func TestWaitNewUsage(t *testing.T) {
	h := newWaitHarness(t)
	h.fail(output.ExitUsage, output.CodeUsage, "messages", "wait-new")
	h.fail(output.ExitUsage, output.CodeUsage, "messages", "wait-new", "--state", h.state, "--interval", "1")
	h.fail(output.ExitUsage, output.CodeUsage, "messages", "wait-new", "--state", h.state, "--chats", "abc")
	if err := os.WriteFile(h.state, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "messages", "wait-new", "--state", h.state)
}

func TestWaitNewOldestFirst(t *testing.T) {
	h := newWaitHarness(t)
	h.wait()
	h.msg(wDM, false, "second", -1*time.Second)
	h.msg(wDM2, false, "first", -2*time.Second)
	h.msg(wDM, false, "third", 0)
	d := h.wait("--timeout", "0")
	if got := texts(d["messages"]); !eq(got, []string{"first", "second", "third"}) {
		t.Fatalf("order %v", got)
	}
}
