package e2e

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
)

var (
	testBot   = tgclient.Peer{ID: 500, RawID: 500, Name: "Test Bot", Type: "bot", Username: "my_test_bot", Kind: tgclient.KindUser}
	otherBot  = tgclient.Peer{ID: 501, RawID: 501, Name: "Other Bot", Type: "bot", Username: "other_bot", Kind: tgclient.KindUser}
	aPerson   = tgclient.Peer{ID: 600, RawID: 600, Name: "Ann", Type: "user", Username: "ann", Kind: tgclient.KindUser}
	aGroupBot = tgclient.Peer{ID: -700, RawID: 700, Name: "G", Type: "group", Username: "g", Kind: tgclient.KindChat}
)

func code(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func TestTarget(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		allowed  []string
		chat     string
		wantID   int64
		wantCode string
		resolves int // Resolve calls allowed
	}{
		{"empty list", nil, "", 0, output.CodeE2ERefused, 0},
		{"only entry by default", []string{"@my_test_bot"}, "", 500, "", 1},
		{"several need --chat", []string{"@my_test_bot", "@other_bot"}, "", 0, output.CodeUsage, 0},
		{"@ form", []string{"@my_test_bot"}, "@my_test_bot", 500, "", 1},
		{"bare, case-insensitive", []string{"@my_test_bot"}, "MY_TEST_BOT", 500, "", 1},
		{"t.me link", []string{"@my_test_bot"}, "https://t.me/my_test_bot", 500, "", 1},
		{"pick among several", []string{"@my_test_bot", "@other_bot"}, "other_bot", 501, "", 1},
		{"other name refused offline", []string{"@my_test_bot"}, "@other_bot", 0, output.CodeE2ERefused, 0},
		{"dialog name refused offline", []string{"@my_test_bot"}, "Saved Messages", 0, output.CodeE2ERefused, 0},
		{"id of the allowed bot", []string{"@my_test_bot"}, "500", 500, "", 1},
		{"id of someone else", []string{"@my_test_bot"}, "600", 0, output.CodeE2ERefused, 1},
		{"listed person refused", []string{"@ann"}, "", 0, output.CodeE2ERefused, 1},
		{"listed group refused", []string{"@g"}, "", 0, output.CodeE2ERefused, 1},
		{"unresolvable entry", []string{"@gone_bot"}, "", 0, output.CodeNotFound, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := tgfake.NewE2E(testBot, otherBot, aPerson, aGroupBot)
			p, err := Target(ctx, f, c.allowed, c.chat)
			if c.wantCode != "" {
				if code(err) != c.wantCode {
					t.Fatalf("err = %v, want code %s", err, c.wantCode)
				}
			} else if err != nil || p.ID != c.wantID {
				t.Fatalf("got %+v, %v; want id %d", p, err, c.wantID)
			}
			if len(f.Resolved) > c.resolves {
				t.Errorf("resolved %v, at most %d allowed", f.Resolved, c.resolves)
			}
			for _, r := range f.Resolved {
				if c.wantCode == "" {
					break
				}
				if r == c.chat {
					t.Errorf("resolved the refused chat %q", r)
				}
			}
		})
	}
}

func TestPrecheckIsOffline(t *testing.T) {
	if err := Precheck([]string{"@my_test_bot"}, "me"); code(err) != output.CodeE2ERefused {
		t.Fatalf("Precheck(me) = %v", err)
	}
	if err := Precheck([]string{"@my_test_bot"}, ""); err != nil {
		t.Fatal(err)
	}
	if !SameRef("@Bot", "t.me/bot") || SameRef("@bot", "@bot2") {
		t.Error("SameRef")
	}
}

func kb(placeholder string, rows ...[]tgclient.Button) *tgclient.ReplyMarkup {
	return &tgclient.ReplyMarkup{Type: "keyboard", Rows: rows, Persistent: true, Placeholder: placeholder}
}

func TestCurrentKeyboard(t *testing.T) {
	k1 := kb("one", []tgclient.Button{{Text: "a", Type: "text"}})
	k2 := kb("two", []tgclient.Button{{Text: "b", Type: "text"}})
	// newest first
	msgs := []tgclient.BotMessage{
		{ID: 9, From: "me", Markup: k1}, // the user's messages never set it
		{ID: 8, From: "bot", Inline: [][]tgclient.Button{{{Text: "x", Type: "callback"}}}},
		{ID: 7, From: "bot", Text: "plain"},
		{ID: 6, From: "bot", Markup: k2},
		{ID: 5, From: "bot", Markup: k1},
	}
	got := CurrentKeyboard(msgs)
	if got == nil || got.MessageID != 6 || got.Markup.Placeholder != "two" {
		t.Fatalf("CurrentKeyboard = %+v", got)
	}
	hide := append([]tgclient.BotMessage{{ID: 10, From: "bot", Markup: &tgclient.ReplyMarkup{Type: "hide"}}}, msgs...)
	if CurrentKeyboard(hide) != nil {
		t.Error("a later remove_keyboard leaves a keyboard")
	}
	force := append([]tgclient.BotMessage{{ID: 10, From: "bot", Markup: &tgclient.ReplyMarkup{Type: "force_reply"}}}, msgs...)
	if CurrentKeyboard(force) != nil {
		t.Error("force_reply counted as a keyboard")
	}
	if CurrentKeyboard(nil) != nil {
		t.Error("empty chat has a keyboard")
	}
}

func TestFindButton(t *testing.T) {
	rows := [][]tgclient.Button{
		{{Text: "A", Type: "callback"}, {Text: "B", Type: "callback"}},
		{{Text: "1", Type: "text"}, {Text: "dup"}, {Text: "dup"}},
	}
	for sel, want := range map[string]string{"A": "A", "B": "B", "0": "A", "3": "dup", "1": "1"} {
		b, err := FindButton(rows, sel)
		if sel == "3" {
			if err != nil || b.Text != "dup" {
				t.Errorf("FindButton(%q) = %+v, %v", sel, b, err)
			}
			continue
		}
		if err != nil || b.Text != want {
			t.Errorf("FindButton(%q) = %+v, %v; want %s", sel, b, err, want)
		}
	}
	if _, err := FindButton(rows, "dup"); code(err) != output.CodeAmbiguous {
		t.Errorf("duplicate label: %v", err)
	}
	if _, err := FindButton(rows, "C"); code(err) != output.CodeNotFound {
		t.Errorf("missing: %v", err)
	}
	if _, err := FindButton(rows, "9"); code(err) != output.CodeNotFound {
		t.Errorf("index out of range: %v", err)
	}
}

func TestNormalizeEmoji(t *testing.T) {
	if NormalizeEmoji(" ❤️ ") != "❤" || NormalizeEmoji("✍️") != "✍" || NormalizeEmoji("👍") != "👍" {
		t.Error("NormalizeEmoji")
	}
}

// ogg builds a minimal Ogg/Opus stream: an OpusHead page and a last page at granule.
func ogg(preSkip uint16, granule int64) []byte {
	page := func(g int64, body []byte) []byte {
		h := make([]byte, 27)
		copy(h, "OggS")
		binary.LittleEndian.PutUint64(h[6:], uint64(g))
		return append(h, body...)
	}
	head := []byte("OpusHead\x01\x01\x00\x00")
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	head = append(head, 0x80, 0xbb, 0, 0, 0, 0, 0)
	return append(page(0, head), page(granule, []byte("audio"))...)
}

func TestOpusDuration(t *testing.T) {
	cases := []struct {
		b    []byte
		want int
	}{
		{ogg(312, 48000*3+312), 3},
		{ogg(312, 48000*3/2+312), 2}, // 1.5 s rounds to 2
		{ogg(312, 100), 1},           // shorter than the pre-skip
		{ogg(0, 48000*61), 61},
	}
	for _, c := range cases {
		if d, err := OpusDuration(c.b); err != nil || d != c.want {
			t.Errorf("OpusDuration = %d, %v; want %d", d, err, c.want)
		}
	}
	for _, bad := range [][]byte{nil, []byte("RIFF....WAVE"), []byte("OggS but no head")} {
		if _, err := OpusDuration(bad); !errors.Is(err, ErrNotOpus) {
			t.Errorf("OpusDuration(%q) err = %v", bad, err)
		}
	}
}

// clock is a fake clock; Sleep advances it.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

func waiter(f *tgfake.E2E) (*Waiter, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p := testBot
	return &Waiter{T: f, Peer: &p, Now: c.now, Sleep: c.sleep}, c
}

func spec(kind string) WaitSpec {
	return WaitSpec{For: kind, Timeout: 20 * time.Second, Interval: time.Second}
}

func TestWaitReply(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 10, From: "me", Text: "ping"})
	f.OnHistory = func(n int) {
		if n == 4 { // the newest-id read, then two empty polls
			f.Add(tgclient.BotMessage{ID: 11, From: "me", Text: "me again"},
				tgclient.BotMessage{ID: 12, From: "bot", Text: "pong"},
				tgclient.BotMessage{ID: 13, From: "bot", Text: "pong 2"})
		}
	}
	w, _ := waiter(f)
	r, err := w.Wait(context.Background(), spec(ForReply))
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.ID != 12 || len(r.Messages) != 2 || r.Messages[1].ID != 13 || r.Elapsed != 2 {
		t.Fatalf("result = %+v", r)
	}
}

func TestWaitReplySinceSeesEarlierReply(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 10, From: "me"}, tgclient.BotMessage{ID: 11, From: "bot", Text: "fast"})
	w, _ := waiter(f)
	s := spec(ForReply)
	s.Since = 10 // a reply that landed before the wait started still counts
	r, err := w.Wait(context.Background(), s)
	if err != nil || r.Message.Text != "fast" {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestWaitTimeout(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 10, From: "bot"})
	w, c := waiter(f)
	start := c.t
	_, err := w.Wait(context.Background(), WaitSpec{For: ForReply, Timeout: 5 * time.Second, Interval: 2 * time.Second})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitTimeout || oe.Code != output.CodeTimeout {
		t.Fatalf("err = %v", err)
	}
	if got := c.t.Sub(start); got != 5*time.Second {
		t.Errorf("waited %s, want exactly the timeout", got)
	}
}

func TestWaitEditIgnoresReactions(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	orig := tgclient.BotMessage{ID: 20, From: "bot", Text: "choose", Inline: [][]tgclient.Button{{{Text: "Drop", Type: "callback"}}}}
	f.Add(orig)
	f.OnHistory = func(n int) {
		switch n {
		case 2: // a reaction bumps edit_date but is not an edit
			m := orig
			m.Edited, m.Reactions = "2026-01-01T00:00:01Z", []tgclient.Reaction{{Emoji: "👍", Count: 1}}
			f.Edit(m)
		case 4:
			m := orig
			m.Edited, m.Text, m.Inline = "2026-01-01T00:00:03Z", "choose\n→ Drop ✓", nil
			f.Edit(m)
		}
	}
	w, _ := waiter(f)
	r, err := w.Wait(context.Background(), spec(ForEdit))
	if err != nil {
		t.Fatal(err)
	}
	// polls at 0 s (reaction only: no edit), 1 s, 2 s (edited)
	if r.Message.Text != "choose\n→ Drop ✓" || r.Elapsed != 2 {
		t.Fatalf("result = %+v", r)
	}
}

func TestWaitReactionOnOneMessage(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 30, From: "me", Text: "hi"}, tgclient.BotMessage{ID: 31, From: "bot"})
	f.OnHistory = func(int) {}
	calls := 0
	w, c := waiter(f)
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		calls++
		if calls == 2 {
			f.Edit(tgclient.BotMessage{ID: 31, From: "bot", Reactions: []tgclient.Reaction{{Emoji: "👀", Count: 1}}}) // other message
		}
		if calls == 3 {
			f.Edit(tgclient.BotMessage{ID: 30, From: "me", Text: "hi", Reactions: []tgclient.Reaction{{Emoji: "✍", Count: 1, By: []int64{500}}}})
		}
		return c.sleep(ctx, d)
	}
	s := spec(ForReaction)
	s.Msg = 30
	r, err := w.Wait(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.ID != 30 || r.Message.Reactions[0].Emoji != "✍" {
		t.Fatalf("result = %+v", r)
	}
	s.Msg = 99
	if _, err := w.Wait(context.Background(), s); code(err) != output.CodeNotFound {
		t.Errorf("missing message: %v", err)
	}
}

func TestWaitPinAndKeyboard(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 40, From: "bot", Text: "🎯 vault"})
	f.Pinned = 40
	f.OnHistory = func(n int) {
		if n == 2 {
			f.Add(tgclient.BotMessage{ID: 41, From: "bot", Text: "🎯 Now talking to ev", Markup: kb("→ ev")})
		}
	}
	w, c := waiter(f)
	r, err := w.Wait(context.Background(), spec(ForKeyboard))
	if err != nil || r.Keyboard == nil || r.Keyboard.Markup.Placeholder != "→ ev" || r.Before != (*Keyboard)(nil) {
		t.Fatalf("keyboard wait = %+v, %v", r, err)
	}

	calls := 0
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		if calls++; calls == 2 {
			f.Edit(tgclient.BotMessage{ID: 40, From: "bot", Text: "🎯 ev", Edited: "x"})
		}
		return c.sleep(ctx, d)
	}
	r, err = w.Wait(context.Background(), spec(ForPin))
	if err != nil || r.Message.Text != "🎯 ev" {
		t.Fatalf("pin wait = %+v, %v", r, err)
	}
}

func TestWaitDraft(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 50, From: "me", Text: "q"})
	f.DraftsCh <- tgclient.Draft{UserID: 999, Text: "someone else"}
	f.DraftsCh <- tgclient.Draft{UserID: 500, RandomID: 7, Text: "Thinking"}
	w, c := waiter(f)
	r, err := w.Wait(context.Background(), spec(ForDraft))
	if err != nil || len(r.Drafts) != 1 || r.Drafts[0].Text != "Thinking" {
		t.Fatalf("first draft = %+v, %v", r, err)
	}

	calls := 0
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		switch calls++; calls {
		case 1:
			f.DraftsCh <- tgclient.Draft{UserID: 500, RandomID: 7, Text: "Thinking… 4"}
		case 2:
			f.DraftsCh <- tgclient.Draft{UserID: 500, RandomID: 7, Text: "Thinking… 42"}
		case 3:
			f.Add(tgclient.BotMessage{ID: 51, From: "bot", Text: "42"})
		}
		return c.sleep(ctx, d)
	}
	s := spec(ForDraft)
	s.All = true
	r, err = w.Wait(context.Background(), s)
	if err != nil || len(r.Drafts) != 2 || r.Message.Text != "42" {
		t.Fatalf("all drafts = %+v, %v", r, err)
	}

	// a timeout with drafts but no final message reports them
	s.Timeout = 2 * time.Second
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case f.DraftsCh <- tgclient.Draft{UserID: 500, Text: "stuck"}:
		default:
		}
		return c.sleep(ctx, d)
	}
	_, err = w.Wait(context.Background(), s)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != output.CodeTimeout || oe.Details["drafts"] == nil {
		t.Fatalf("draft timeout = %v", err)
	}
}

func TestWaitUnknownKind(t *testing.T) {
	w, _ := waiter(tgfake.NewE2E(testBot))
	if _, err := w.Wait(context.Background(), spec("nope")); code(err) != output.CodeUsage {
		t.Fatal(err)
	}
}

// A reaction on the pinned message is not a pin change.
func TestWaitPinIgnoresReactions(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 40, From: "bot", Text: "🎯 vault"})
	f.Pinned = 40
	w, c := waiter(f)
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		f.Edit(tgclient.BotMessage{ID: 40, From: "bot", Text: "🎯 vault", Edited: "x", Reactions: []tgclient.Reaction{{Emoji: "👍", Count: 1, Mine: true}}})
		return c.sleep(ctx, d)
	}
	s := spec(ForPin)
	s.Timeout = 3 * time.Second
	if _, err := w.Wait(context.Background(), s); code(err) != output.CodeTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
}

// Swapping the file or a custom emoji is an edit even when the rest is equal.
func TestWaitEditSeesMediaAndEmojiSwaps(t *testing.T) {
	for _, swap := range []func(m *tgclient.BotMessage){
		func(m *tgclient.BotMessage) { m.MediaID = 2 },
		func(m *tgclient.BotMessage) {
			m.Entities = []tgclient.Entity{{Type: "custom_emoji", Length: 2, CustomEmojiID: 2}}
		},
	} {
		f := tgfake.NewE2E(testBot)
		orig := tgclient.BotMessage{ID: 20, From: "bot", Text: "🙂", Media: &tgclient.Media{Type: "document", Name: "r.pdf", Size: 10},
			MediaID: 1, Entities: []tgclient.Entity{{Type: "custom_emoji", Length: 2, CustomEmojiID: 1}}}
		f.Add(orig)
		w, c := waiter(f)
		w.Sleep = func(ctx context.Context, d time.Duration) error {
			m := orig
			swap(&m)
			f.Edit(m)
			return c.sleep(ctx, d)
		}
		s := spec(ForEdit)
		s.Msg = 20
		if _, err := w.Wait(context.Background(), s); err != nil {
			t.Fatalf("edit not seen: %v", err)
		}
	}
}

// A persistent keyboard stays in effect however many messages follow it.
func TestReadKeyboardBeyondOnePage(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 10, From: "bot", Text: "🎯", Markup: kb("→ vault")})
	for i := 11; i < 400; i++ {
		f.Add(tgclient.BotMessage{ID: i, From: []string{"me", "bot"}[i%2], Text: "chat"})
	}
	got, err := ReadKeyboard(context.Background(), f, &testBot)
	if err != nil || got == nil || got.MessageID != 10 {
		t.Fatalf("keyboard = %+v, %v", got, err)
	}
	// and a keyboard wait doesn't page the whole chat on every poll
	f.OnHistory = nil
	w, c := waiter(f)
	polls := 0
	w.Sleep = func(ctx context.Context, d time.Duration) error {
		if polls++; polls == 3 {
			f.Add(tgclient.BotMessage{ID: 400, From: "bot", Text: "🎯 ev", Markup: kb("→ ev")})
		}
		return c.sleep(ctx, d)
	}
	f.HistoryCalls = 0
	r, err := w.Wait(context.Background(), spec(ForKeyboard))
	if err != nil || r.Keyboard.MessageID != 400 || r.Before.(*Keyboard).MessageID != 10 {
		t.Fatalf("wait = %+v, %v", r, err)
	}
	if f.HistoryCalls > 10 {
		t.Errorf("%d history calls for a 3-poll wait", f.HistoryCalls)
	}
}

// The timeout bounds the polls too: a read stuck in a long FLOOD_WAIT ends as
// a timeout, not after it.
func TestWaitTimeoutBoundsPolls(t *testing.T) {
	f := tgfake.NewE2E(testBot)
	f.Add(tgclient.BotMessage{ID: 10, From: "bot"})
	w, _ := waiter(f)
	x, err := w.Begin(context.Background(), WaitSpec{For: ForReply, Timeout: 20 * time.Millisecond, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.Stall = true
	done := make(chan error, 1)
	go func() { _, err := x.Wait(context.Background()); done <- err }()
	select {
	case err := <-done:
		if code(err) != output.CodeTimeout {
			t.Fatalf("err = %v, want timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck poll outlived the timeout")
	}
}
