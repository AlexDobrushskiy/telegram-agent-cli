package cli

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
)

var (
	e2eBot    = tgclient.Peer{ID: 500, RawID: 500, Name: "Test Bot", Type: "bot", Username: "my_test_bot", Kind: tgclient.KindUser}
	e2ePerson = tgclient.Peer{ID: 600, RawID: 600, Name: "Ann", Type: "user", Username: "ann", Kind: tgclient.KindUser}
)

type e2eHarness struct {
	*harness
	bot      *tgfake.E2E
	connects int
	clock    time.Time
}

// newE2EHarness has config.json with e2e.allowed_chats = allowed and a fake bot chat.
func newE2EHarness(t *testing.T, allowed ...string) *e2eHarness {
	h := &e2eHarness{harness: newHarness(t), bot: tgfake.NewE2E(e2eBot, e2ePerson)}
	cfg := config.Config{APIID: 1, APIHash: "0123456789abcdef0123456789abcdef"}
	if allowed != nil {
		cfg.E2E = &config.E2E{AllowedChats: allowed}
	}
	if err := h.app.Paths.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	h.app.ConnectE2E = func(ctx context.Context, _ tgclient.Options, fn func(context.Context, tgclient.E2E) error) error {
		h.connects++
		return tgclient.MapError(fn(ctx, h.bot))
	}
	h.clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.app.Now = func() time.Time { return h.clock }
	h.app.Sleep = func(_ context.Context, d time.Duration) error {
		h.clock = h.clock.Add(d)
		return nil
	}
	return h
}

// granted checks that every write reached the fake under a grant for the bot only.
func (h *e2eHarness) granted() {
	h.t.Helper()
	for _, w := range h.bot.Writes {
		if w.Grant != e2eBot.RawID {
			h.t.Errorf("write %+v ran with grant %d, want %d", w, w.Grant, e2eBot.RawID)
		}
	}
}

func TestE2ERefusedBeforeConnecting(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	for _, chat := range []string{"me", "@ann", "Saved Messages", "@other_bot"} {
		e := h.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "send", "text", "hi", "--chat", chat)
		if e.Details["chat"] != chat {
			t.Errorf("details = %v", e.Details)
		}
	}
	if h.connects != 0 {
		t.Errorf("connected %d times for refused chats", h.connects)
	}
	// a numeric id is checked against the allowed entries' ids, not resolved itself
	h.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "send", "text", "hi", "--chat", "600")
	if slices.Contains(h.bot.Resolved, "600") {
		t.Error("resolved the refused id")
	}
	if len(h.bot.Writes) != 0 {
		t.Fatalf("writes = %+v", h.bot.Writes)
	}

	none := newE2EHarness(t)
	none.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "send", "text", "hi")

	person := newE2EHarness(t, "@ann")
	person.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "send", "text", "hi")
	if len(person.bot.Writes) != 0 {
		t.Error("wrote to a person on the list")
	}
}

func TestE2ESendTextAndWait(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	h.bot.Add(tgclient.BotMessage{ID: 5, From: "bot", Text: "hello"})
	h.bot.OnHistory = func(n int) {
		if n == 2 {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "pong", ReplyTo: 1001})
		}
	}
	d := h.ok("e2e", "send", "text", "ping 1", "--reply-to", "5", "--wait", "reply", "--timeout", "10")
	sent := d["sent"].(map[string]any)
	if sent["text"] != "ping 1" || sent["from"] != "me" || sent["reply_to"] != float64(5) {
		t.Errorf("sent = %v", sent)
	}
	wait := d["wait"].(map[string]any)
	if wait["message"].(map[string]any)["text"] != "pong" {
		t.Errorf("wait = %v", wait)
	}
	w := h.bot.Writes[0]
	if w.Kind != "text" || w.Text != "ping 1" || w.ReplyTo != 5 {
		t.Errorf("write = %+v", w)
	}
	// --message works too; both at once is a usage error
	h.ok("e2e", "send", "text", "--message", "ping 2", "--chat", "MY_TEST_BOT")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "text", "a", "--message", "b")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "text", "  ")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "text", "x", "--wait", "nope")
	h.granted()
}

func TestE2EWaitTimeoutStillReportsWrite(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	e := h.fail(output.ExitTimeout, output.CodeTimeout, "e2e", "send", "text", "anyone?", "--wait", "reply", "--timeout", "3")
	if e.Details["write"] == nil {
		t.Errorf("timeout hides the write: %v", e.Details)
	}
	if len(h.bot.Writes) != 1 {
		t.Errorf("writes = %+v", h.bot.Writes)
	}
}

func writeOgg(t *testing.T, seconds int) string {
	t.Helper()
	page := func(g int64, body string) []byte {
		h := make([]byte, 27)
		copy(h, "OggS")
		binary.LittleEndian.PutUint64(h[6:], uint64(g))
		return append(h, body...)
	}
	b := append(page(0, "OpusHead\x01\x01\x00\x00\x80\xbb\x00\x00\x00\x00\x00"), page(int64(48000*seconds), "x")...)
	p := filepath.Join(t.TempDir(), "q.ogg")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestE2ESendMedia(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	ogg := writeOgg(t, 4)
	h.ok("e2e", "send", "voice", "--path", ogg)
	if w := h.bot.Writes[0]; w.Media.Kind != "voice" || w.Media.Duration != 4 {
		t.Errorf("voice write = %+v", w)
	}
	jpg := filepath.Join(t.TempDir(), "p.jpg")
	_ = os.WriteFile(jpg, []byte("\xff\xd8\xff"), 0o600)
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "voice", "--path", jpg) // not ogg/opus
	h.ok("e2e", "send", "photo", "--path", jpg, "--caption", "describe this")
	if w := h.bot.Writes[1]; w.Media.Kind != "photo" || w.Text != "describe this" {
		t.Errorf("photo write = %+v", w)
	}
	h.ok("e2e", "send", "file", "--path", jpg)
	h.ok("e2e", "send", "location", "--lat", "41.55", "--long", "-8.42")
	if w := h.bot.Writes[3]; w.Media.Kind != "location" || w.Media.Lat != 41.55 {
		t.Errorf("location write = %+v", w)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "location", "--lat", "91", "--long", "0")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "send", "photo", "--path", "/nonexistent.jpg")
	h.granted()
}

func TestE2EReactPressStop(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	h.bot.Add(tgclient.BotMessage{ID: 7, From: "bot", Text: "pick", Inline: [][]tgclient.Button{{
		{Text: "A", Type: "callback", Data: "cb_A"},
		{Text: "Site", Type: "url", URL: "https://example.com"},
	}}})
	h.bot.Answer = &tgclient.CallbackAnswer{Answered: true, Message: "got A"}

	d := h.ok("e2e", "react", "--msg", "7", "--emoji", "❤️")
	if h.bot.Writes[0].Text != "❤" || d["message"].(map[string]any)["reactions"] == nil {
		t.Errorf("react = %+v / %v", h.bot.Writes[0], d)
	}
	h.ok("e2e", "react", "--msg", "7", "--remove")
	if h.bot.Writes[1].Text != "" {
		t.Errorf("remove = %+v", h.bot.Writes[1])
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "react", "--msg", "7")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "react", "--msg", "7", "--emoji", "👍", "--remove")

	d = h.ok("e2e", "press", "--msg", "7", "--button", "A")
	if w := h.bot.Writes[2]; w.Kind != "press" || string(w.Data) != "cb_A" || w.MsgID != 7 {
		t.Errorf("press = %+v", w)
	}
	if d["answer"].(map[string]any)["message"] != "got A" {
		t.Errorf("answer = %v", d["answer"])
	}
	h.ok("e2e", "press", "--msg", "7", "--button", "0")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "press", "--msg", "7", "--button", "Site")
	h.fail(output.ExitNotFound, output.CodeNotFound, "e2e", "press", "--msg", "7", "--button", "Z")
	h.fail(output.ExitNotFound, output.CodeNotFound, "e2e", "press", "--msg", "99", "--button", "A")
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "press", "--button", "A")

	h.ok("e2e", "stop", "--draft", "888")
	if w := h.bot.Writes[len(h.bot.Writes)-1]; w.Kind != "stop" || w.DraftID != 888 {
		t.Errorf("stop = %+v", w)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "stop")
	h.granted()
}

func TestE2EKeyboardAndMiniApp(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	h.ok("e2e", "read", "keyboard")
	h.fail(output.ExitNotFound, output.CodeNotFound, "e2e", "press", "--keyboard", "--button", "x")
	h.bot.Add(tgclient.BotMessage{ID: 10, From: "bot", Text: "🎯 Now talking to vault", Markup: &tgclient.ReplyMarkup{
		Type: "keyboard", Persistent: true, Placeholder: "→ vault · black",
		Rows: [][]tgclient.Button{{
			{Text: "🎯 vault@black ▾", Type: "web_app", URL: "https://picker.example/"},
			{Text: "ev-test@green", Type: "text"},
		}},
	}})
	d := h.ok("e2e", "read", "keyboard")
	kb := d["keyboard"].(map[string]any)
	if kb["message_id"] != float64(10) || kb["keyboard"].(map[string]any)["placeholder"] != "→ vault · black" {
		t.Errorf("keyboard = %v", kb)
	}

	h.ok("e2e", "press", "--keyboard", "--button", "ev-test@green")
	if w := h.bot.Writes[0]; w.Kind != "text" || w.Text != "ev-test@green" {
		t.Errorf("keyboard press = %+v", w)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "press", "--keyboard", "--button", "0") // web_app

	d = h.ok("e2e", "web-app-data", "--button", "0", "--data", `{"target":"ev-test@green"}`)
	if w := h.bot.Writes[1]; w.Kind != "webappdata" || w.Text != "🎯 vault@black ▾" || string(w.Data) != `{"target":"ev-test@green"}` {
		t.Errorf("web app data = %+v", w)
	}
	if d["sent"] == nil {
		t.Errorf("no service message in %v", d)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "web-app-data", "--button", "ev-test@green", "--data", "x")

	d = h.ok("e2e", "webapp-url", "--button", "0")
	if d["entry"] != "keyboard" || d["url"] != "https://picker.example/#tgWebAppData=fake" {
		t.Errorf("webapp-url = %v", d)
	}
	h.fail(output.ExitNotFound, output.CodeNotFound, "e2e", "webapp-url", "--menu")
	h.bot.Menu = &tgclient.MenuButton{Text: "Agents", URL: "https://board.example/"}
	if d = h.ok("e2e", "webapp-url", "--menu"); d["entry"] != "menu" || d["button"] != "Agents" {
		t.Errorf("menu = %v", d)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "webapp-url", "--menu", "--main")
	h.granted()
}

func TestE2EReadMessagesAndPinned(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	h.bot.Add(tgclient.BotMessage{ID: 3, From: "me", Text: "q"}, tgclient.BotMessage{ID: 4, From: "bot", Text: "a"},
		tgclient.BotMessage{ID: 5, From: "bot", Text: "b"})
	d := h.ok("e2e", "read", "messages", "--since", "3")
	if ids := idsOf(d["messages"]); !slices.Equal(ids, []int{4, 5}) {
		t.Errorf("ids = %v (want oldest first, after --since)", ids)
	}
	if ids := idsOf(h.ok("e2e", "read", "messages", "--from", "me")["messages"]); !slices.Equal(ids, []int{3}) {
		t.Errorf("--from me = %v", ids)
	}
	h.fail(output.ExitUsage, output.CodeUsage, "e2e", "read", "messages", "--from", "x")
	if h.ok("e2e", "read", "pinned")["pinned"] != nil {
		t.Error("pinned without a pin")
	}
	h.bot.Pinned = 4
	if p := h.ok("e2e", "read", "pinned")["pinned"].(map[string]any); p["text"] != "a" {
		t.Errorf("pinned = %v", p)
	}
	if len(h.bot.Writes) != 0 {
		t.Error("reads wrote")
	}
}

func TestE2EAllowDisallow(t *testing.T) {
	h := newE2EHarness(t)
	t.Setenv(config.EnvReadOnly, "0")
	if d := h.ok("e2e", "allow", "my_test_bot"); !slices.Equal(strs(d["allowed_chats"]), []string{"@my_test_bot"}) {
		t.Errorf("allow = %v", d)
	}
	h.ok("e2e", "allow", "@MY_TEST_BOT") // no duplicate
	h.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "allow", "@ann")
	cfg, _ := h.app.Paths.LoadConfig()
	if !slices.Equal(cfg.E2EAllowed(), []string{"@my_test_bot"}) || cfg.APIID != 1 {
		t.Errorf("config = %+v", cfg)
	}
	d := h.ok("e2e", "status")
	if rows := list(d["allowed_chats"]); len(rows) != 1 || rows[0]["ok"] != true {
		t.Errorf("status = %v", d)
	}
	h.ok("e2e", "disallow", "t.me/my_test_bot")
	h.fail(output.ExitNotFound, output.CodeNotFound, "e2e", "disallow", "@my_test_bot")
	if cfg, _ = h.app.Paths.LoadConfig(); len(cfg.E2EAllowed()) != 0 {
		t.Errorf("after disallow: %v", cfg.E2EAllowed())
	}
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// botReacts sets the bot's reaction on message id.
func botReacts(f *tgfake.E2E, id int, emoji string) {
	for i := range f.Msgs {
		if f.Msgs[i].ID == id {
			f.Msgs[i].Reactions = append(f.Msgs[i].Reactions, tgclient.Reaction{Emoji: emoji, Count: 1, By: []int64{e2eBot.RawID}})
		}
	}
}

// The bot answers a write at once, before the command reads anything: the
// wait must still see it (its baseline is the state before the write).
func TestE2EWaitSeesEventsRightAfterTheWrite(t *testing.T) {
	waitOf := func(d map[string]any) map[string]any { return d["wait"].(map[string]any) }

	t.Run("send text, reaction on the sent message", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 5, From: "bot", Text: "hello"})
		h.bot.OnWrite = func(w tgfake.E2EWrite) { botReacts(h.bot, h.bot.NextID, "👀") }
		d := h.ok("e2e", "send", "text", "hi", "--wait", "reaction", "--timeout", "3")
		m := waitOf(d)["message"].(map[string]any)
		if m["id"] != float64(h.bot.NextID) || m["reactions"].([]any)[0].(map[string]any)["emoji"] != "👀" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("send text, reply then edit", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "thinking"})
		}
		h.bot.OnHistory = func(n int) {
			if n == 4 {
				h.bot.Edit(tgclient.BotMessage{ID: 2000, From: "bot", Text: "done", Edited: "2026-01-01T00:00:02Z"})
			}
		}
		d := h.ok("e2e", "send", "text", "hi", "--wait", "edit", "--timeout", "5")
		if m := waitOf(d)["message"].(map[string]any); m["text"] != "done" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("send text, reply during the wait, then edit", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.OnHistory = func(n int) {
			switch n {
			case 3:
				h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "thinking"})
			case 5:
				h.bot.Edit(tgclient.BotMessage{ID: 2000, From: "bot", Text: "done", Edited: "2026-01-01T00:00:03Z"})
			}
		}
		d := h.ok("e2e", "send", "text", "hi", "--wait", "edit", "--timeout", "8")
		if m := waitOf(d)["message"].(map[string]any); m["text"] != "done" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("send text, reply already edited", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "done", Edited: "2026-01-01T00:00:01Z"})
		}
		d := h.ok("e2e", "send", "text", "hi", "--wait", "edit", "--timeout", "3")
		if m := waitOf(d)["message"].(map[string]any); m["text"] != "done" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("press inline, reply", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 7, From: "bot", Text: "pick", Inline: [][]tgclient.Button{{{Text: "A", Type: "callback", Data: "a"}}}})
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "you picked A"})
		}
		d := h.ok("e2e", "press", "--msg", "7", "--button", "A", "--wait", "reply", "--timeout", "3")
		if m := waitOf(d)["message"].(map[string]any); m["text"] != "you picked A" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("stop, final message", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 7, From: "me", Text: "q"})
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "stopped"})
		}
		d := h.ok("e2e", "stop", "--draft", "9", "--wait", "reply", "--timeout", "3")
		if m := waitOf(d)["message"].(map[string]any); m["text"] != "stopped" {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("keyboard press, new keyboard", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 10, From: "bot", Text: "menu", Markup: &tgclient.ReplyMarkup{
			Type: "keyboard", Rows: [][]tgclient.Button{{{Text: "next", Type: "text"}}}}})
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			h.bot.Add(tgclient.BotMessage{ID: 2000, From: "bot", Text: "page 2", Markup: &tgclient.ReplyMarkup{
				Type: "keyboard", Rows: [][]tgclient.Button{{{Text: "back", Type: "text"}}}}})
		}
		d := h.ok("e2e", "press", "--keyboard", "--button", "next", "--wait", "keyboard", "--timeout", "3")
		if kb := waitOf(d)["keyboard"].(map[string]any); kb["message_id"] != float64(2000) {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})

	t.Run("react: the bot's reaction counts, our own does not", func(t *testing.T) {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 7, From: "me", Text: "q"})
		h.bot.OnWrite = func(w tgfake.E2EWrite) {
			if w.Text == "👍" {
				botReacts(h.bot, 7, "👀")
			}
		}
		h.fail(output.ExitTimeout, output.CodeTimeout, "e2e", "react", "--msg", "7", "--emoji", "❤️", "--wait", "reaction", "--timeout", "3")
		d := h.ok("e2e", "react", "--msg", "7", "--emoji", "👍", "--wait", "reaction", "--timeout", "3")
		if m := waitOf(d)["message"].(map[string]any); len(m["reactions"].([]any)) != 2 {
			t.Fatalf("wait = %v", waitOf(d))
		}
	})
}

// A send's reaction wait watches the message it sent: not the reply-to target,
// not other recent messages.
func TestE2ESendReactionWaitWatchesTheSentMessage(t *testing.T) {
	for _, args := range [][]string{{}, {"--reply-to", "5"}} {
		h := newE2EHarness(t, "@my_test_bot")
		h.bot.Add(tgclient.BotMessage{ID: 5, From: "me", Text: "earlier"})
		h.bot.OnWrite = func(tgfake.E2EWrite) { botReacts(h.bot, 5, "👍") } // unrelated
		sleeps, sleep := 0, h.app.Sleep
		h.app.Sleep = func(ctx context.Context, d time.Duration) error {
			if sleeps++; sleeps == 2 {
				botReacts(h.bot, h.bot.NextID, "👀")
			}
			return sleep(ctx, d)
		}
		cmd := append([]string{"e2e", "send", "text", "hi", "--wait", "reaction", "--timeout", "8"}, args...)
		d := h.ok(cmd...)
		m := d["wait"].(map[string]any)["message"].(map[string]any)
		if m["id"] != float64(h.bot.NextID) {
			t.Fatalf("%v: wait = %v", args, d["wait"])
		}
	}
}

// Reply and draft waits count only what comes after the write, whatever
// message the write acted on.
func TestE2EReplyWaitAfterReactIgnoresOlderBotMessages(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	h.bot.Add(tgclient.BotMessage{ID: 10, From: "me", Text: "q"}, tgclient.BotMessage{ID: 20, From: "bot", Text: "old answer"})
	h.fail(output.ExitTimeout, output.CodeTimeout, "e2e", "react", "--msg", "10", "--emoji", "👍", "--wait", "reply", "--timeout", "3")
	h.fail(output.ExitTimeout, output.CodeTimeout, "e2e", "react", "--msg", "10", "--emoji", "👍", "--wait", "draft", "--all", "--timeout", "3")
}

// The guard hook's model of cobra: a flag the root command doesn't define takes
// the next word as its value there, even when the subcommand declares it bool.
func TestE2EUnknownRootFlagEatsTheNextWord(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	// "me" is the text, not the command: this is e2e send text --all me
	e := h.fail(output.ExitE2E, output.CodeE2ERefused, "--all", "me", "e2e", "send", "text", "--chat", "@other_bot")
	if e.Details["chat"] != "@other_bot" {
		t.Errorf("details = %v", e.Details)
	}
}

// The allow-list is read again once the account lock is held: a bot disallowed
// while the command waited for the lock gets nothing.
func TestE2EDisallowWhileQueuedForTheLock(t *testing.T) {
	h := newE2EHarness(t, "@my_test_bot")
	connect := h.app.ConnectE2E
	h.app.ConnectE2E = func(ctx context.Context, o tgclient.Options, fn func(context.Context, tgclient.E2E) error) error {
		// what e2e disallow does while this command waits for the lock
		cfg, err := h.app.Paths.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		cfg.E2E.AllowedChats = nil
		if err := h.app.Paths.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		return connect(ctx, o, fn)
	}
	h.fail(output.ExitE2E, output.CodeE2ERefused, "e2e", "send", "text", "hi")
	if len(h.bot.Writes) != 0 {
		t.Fatalf("writes = %+v", h.bot.Writes)
	}
}
