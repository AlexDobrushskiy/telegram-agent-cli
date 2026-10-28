package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/watch"
)

// Chats of the watcher fixture.
const (
	cLead      = int64(101) // listed 1:1, they wrote last
	cMutual    = int64(102) // mutual contact, not listed, they wrote first
	cStranger  = int64(103) // not listed, not a contact, they wrote first
	cIWrote    = int64(104) // not listed, mutual contact, I wrote first
	cBot       = int64(105)
	cEmpty     = int64(106) // listed, no messages
	cGroup     = int64(-201)
	cUnlisted  = int64(-1000000000202) // supergroup, not listed
	cChannel   = int64(-1000000000301)
	meID       = int64(1)
	otherUser  = int64(55)
	sessA      = "session-a"
	sessB      = "session-b"
	envSession = config.DefaultWatcherSessionEnv
)

// grantFake checks that SendText runs under a one-send grant and nothing else.
type grantFake struct {
	*tgfake.Fake
	t        *testing.T
	failSend error
}

type nopInvoker struct{}

func (nopInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error { return nil }

func (g *grantFake) SendText(ctx context.Context, p *tgclient.Peer, text string, replyTo int) (*tgclient.Message, error) {
	inv := guard.Middleware(config.ReadOnly, false).Handle(nopInvoker{})
	if !config.ReadOnly() {
		g.t.Error("watch reply test is not running read-only")
	}
	if err := inv.Invoke(ctx, &tg.MessagesReadHistoryRequest{}, nil); err == nil {
		g.t.Error("the send grant let messages.readHistory through")
	}
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{}, nil); err != nil {
		return nil, err
	}
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{}, nil); err == nil {
		g.t.Error("the send grant allowed a second messages.sendMessage")
	}
	if g.failSend != nil {
		return nil, g.failSend
	}
	return g.Fake.SendText(ctx, p, text, replyTo)
}

type watchHarness struct {
	*harness
	mu    sync.Mutex
	g     *grantFake
	clock time.Time
	env   map[string]string
	root  string
	reg   map[string]map[string]any
}

func newWatchHarness(t *testing.T) *watchHarness {
	h := &watchHarness{harness: newHarness(t), clock: t0.Add(24 * time.Hour), env: map[string]string{envSession: sessA}}
	h.fake = tgfake.New(tgclient.User{ID: meID, Name: "Owner", Type: "user"})
	h.fake.Now = func() time.Time { return h.clock }
	h.fake.Mutual = map[int64]bool{cMutual: true, cIWrote: true}
	for _, d := range []tgclient.Dialog{
		{ID: meID, Name: "Saved Messages", Type: "user"},
		{ID: cLead, Name: "Lead", Type: "user"},
		{ID: cMutual, Name: "Mutual", Type: "user"},
		{ID: cStranger, Name: "Stranger", Type: "user"},
		{ID: cIWrote, Name: "Friend", Type: "user"},
		{ID: cBot, Name: "Some Bot", Type: "bot"},
		{ID: cEmpty, Name: "Empty", Type: "user"},
		{ID: cGroup, Name: "Group", Type: "group"},
		{ID: cUnlisted, Name: "Big Group", Type: "supergroup"},
		{ID: cChannel, Name: "Channel", Type: "channel"},
	} {
		h.fake.AddDialog(d)
	}
	h.g = &grantFake{Fake: h.fake, t: t}
	h.app.Connect = func(ctx context.Context, _ tgclient.Options, fn func(context.Context, tgclient.Telegram) error) error {
		h.mu.Lock()
		h.connects++
		h.mu.Unlock()
		return tgclient.MapError(fn(ctx, h.g))
	}
	h.app.Now = func() time.Time { return h.clock }
	h.app.Getenv = func(k string) string { return h.env[k] }

	h.add(cLead, 1, true, "hi, it's for sale", -2*time.Hour)
	h.add(cLead, 2, false, "is it still available?", -5*time.Minute)
	for _, c := range []int64{cMutual, cStranger, meID, cBot} {
		h.add(c, 1, false, "hello", -time.Hour)
	}
	h.add(cIWrote, 1, true, "hey", -3*time.Hour)
	h.add(cIWrote, 2, false, "hey back", -time.Hour)
	h.add(cGroup, 1, true, "selling a thing", -2*time.Hour)
	h.add(cGroup, 2, false, "@owner how much?", -3*time.Minute, mentioned)
	h.add(cGroup, 3, false, "unrelated chatter", -2*time.Minute)
	h.add(cGroup, 4, false, "and the colour?", -time.Minute, replyTo(1))
	h.add(cUnlisted, 1, false, "@owner hi", -time.Minute, mentioned)
	h.add(cChannel, 1, false, "post", -time.Minute)

	h.root = t.TempDir()
	h.reg = map[string]map[string]any{}
	h.addWatcher("sales", sessA, map[string]any{"groups": true}, cLead, cEmpty, cGroup)
	return h
}

func mentioned(m *tgclient.Message) { m.Mentioned = true }
func replyTo(id int) func(*tgclient.Message) {
	return func(m *tgclient.Message) { m.ReplyTo = id }
}

func (h *watchHarness) add(chat int64, id int, fromMe bool, text string, ago time.Duration, opts ...func(*tgclient.Message)) {
	sender := chat
	if fromMe {
		sender = meID
	} else if chat < 0 {
		sender = otherUser
	}
	m := tgclient.Message{ID: id, ChatID: chat, FromMe: fromMe, SenderID: &sender, Text: text, Time: h.clock.Add(ago)}
	for _, o := range opts {
		o(&m)
	}
	h.fake.AddMessage(m)
}

func (h *watchHarness) dir(name string) string { return filepath.Join(h.root, name) }

// addWatcher registers name with a state.json whose active watcher is session.
func (h *watchHarness) addWatcher(name, session string, fields map[string]any, chats ...int64) {
	if err := os.MkdirAll(h.dir(name), 0o700); err != nil {
		h.t.Fatal(err)
	}
	e := map[string]any{"dir": h.dir(name), "scope": "listed", "approved": "test fixture"}
	for k, v := range fields {
		e[k] = v
	}
	h.reg[name] = e
	h.writeRegistry()
	h.writeState(name, session, chats...)
}

func (h *watchHarness) set(name, key string, v any) {
	h.reg[name][key] = v
	h.writeRegistry()
}

func (h *watchHarness) writeRegistry() {
	b, _ := json.Marshal(h.reg)
	h.write(h.app.Paths.Watchers(), string(b))
}

func (h *watchHarness) writeState(name, session string, chats ...int64) {
	m := map[string]string{}
	for _, c := range chats {
		m[strconv.FormatInt(c, 10)] = "note"
	}
	b, _ := json.Marshal(map[string]any{"active_watcher": session, "chats": m})
	h.write(filepath.Join(h.dir(name), watch.StateFile), string(b))
}

func (h *watchHarness) write(path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *watchHarness) appendLine(path string, v any) {
	if err := watch.AppendJSONL(path, v); err != nil {
		h.t.Fatal(err)
	}
}

func (h *watchHarness) send(ago time.Duration, chat int64) watch.Send {
	ts := float64(h.clock.Add(ago).UnixNano()) / 1e9
	return watch.Send{T: ts, Watcher: "x", ChatID: chat, Text: "t", OK: true, MsgID: 900}
}

func (h *watchHarness) answer(ago time.Duration, chat int64, msg, sent int) watch.Answer {
	ts := float64(h.clock.Add(ago).UnixNano()) / 1e9
	return watch.Answer{T: ts, Watcher: "x", ChatID: chat, MsgID: msg, SentID: sent}
}

func (h *watchHarness) reply(chat int64, text string, extra ...string) []string {
	return append([]string{"watch", "reply", "--watcher", "sales", "--chat", strconv.FormatInt(chat, 10), "--text", text}, extra...)
}

func (h *watchHarness) refused(rule string, args ...string) {
	h.t.Helper()
	e := h.fail(output.ExitWatcher, output.CodeWatcherRefused, args...)
	if e.Details["rule"] != rule {
		h.t.Fatalf("%v: rule %v (%s), want %s", args, e.Details["rule"], e.Message, rule)
	}
	if n := len(h.fake.Sent); n != 0 {
		h.t.Fatalf("%v: refused but %d sent", args, n)
	}
}

func readLines[T any](t *testing.T, path string) []T {
	t.Helper()
	v, bad, err := watch.ReadJSONL[T](path)
	if err != nil || bad != 0 {
		t.Fatalf("%s: %v (%d bad lines)", path, err, bad)
	}
	return v
}

func TestWatchReplyRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *watchHarness)
		chat  int64
		extra []string
		text  string
		rule  string
	}{
		{"pause all", func(h *watchHarness) { h.write(h.app.Paths.PauseAll(), "") }, cLead, nil, "", watch.RulePausedAll},
		{"not registered", func(h *watchHarness) { delete(h.reg, "sales"); h.writeRegistry() }, cLead, nil, "", watch.RuleNotRegistered},
		{"no registry", func(h *watchHarness) { os.Remove(h.app.Paths.Watchers()) }, cLead, nil, "", watch.RuleNotRegistered},
		{"registry not json", func(h *watchHarness) { h.write(h.app.Paths.Watchers(), "[1,2]") }, cLead, nil, "", watch.RuleInvalid},
		{"unknown field", func(h *watchHarness) { h.set("sales", "group", true) }, cLead, nil, "", watch.RuleInvalid},
		{"bad scope", func(h *watchHarness) { h.set("sales", "scope", "everyone") }, cLead, nil, "", watch.RuleInvalid},
		{"zero per_hour", func(h *watchHarness) { h.set("sales", "per_hour", 0) }, cLead, nil, "", watch.RuleInvalid},
		{"relative dir", func(h *watchHarness) { h.set("sales", "dir", "watchers/sales") }, cLead, nil, "", watch.RuleInvalid},
		{"missing dir", func(h *watchHarness) { h.set("sales", "dir", filepath.Join(h.root, "nope")) }, cLead, nil, "", watch.RuleInvalid},
		{"pause", func(h *watchHarness) { h.write(filepath.Join(h.dir("sales"), watch.PauseFile), "") }, cLead, nil, "", watch.RulePaused},
		{"no state", func(h *watchHarness) { os.Remove(filepath.Join(h.dir("sales"), watch.StateFile)) }, cLead, nil, "", watch.RuleState},
		{"bad state", func(h *watchHarness) { h.write(filepath.Join(h.dir("sales"), watch.StateFile), "{") }, cLead, nil, "", watch.RuleState},
		{"caller unset", func(h *watchHarness) { delete(h.env, envSession) }, cLead, nil, "", watch.RuleNotActive},
		{"caller differs", func(h *watchHarness) { h.env[envSession] = sessB }, cLead, nil, "", watch.RuleNotActive},
		{"no active watcher", func(h *watchHarness) { h.writeState("sales", "", cLead) }, cLead, nil, "", watch.RuleNotActive},
		{"empty text", nil, cLead, nil, "   ", watch.RuleTextLength},
		{"long text", func(h *watchHarness) { h.set("sales", "max_chars", 10) }, cLead, nil, "eleven chars", watch.RuleTextLength},
		{"rate per watcher", func(h *watchHarness) {
			h.set("sales", "per_hour", 2)
			log := filepath.Join(h.dir("sales"), watch.LogFile)
			h.appendLine(log, h.send(-2*time.Hour, cMutual)) // too old to count
			h.appendLine(log, h.send(-50*time.Minute, cMutual))
			failed := h.send(-40*time.Minute, cMutual)
			failed.OK = false // failed attempts count too
			h.appendLine(log, failed)
		}, cLead, nil, "", watch.RuleRateWatcher},
		{"rate global via logs", func(h *watchHarness) {
			if err := h.app.Paths.SaveConfig(config.Config{APIID: 1, GlobalPerHour: 3}); err != nil {
				h.t.Fatal(err)
			}
			h.addWatcher("other", sessB, nil)
			for i := 0; i < 3; i++ {
				h.appendLine(filepath.Join(h.dir("other"), watch.LogFile), h.send(-10*time.Minute, cMutual))
			}
		}, cLead, nil, "", watch.RuleRateGlobal},
		{"rate global via ledger", func(h *watchHarness) {
			if err := h.app.Paths.SaveConfig(config.Config{APIID: 1, GlobalPerHour: 2}); err != nil {
				h.t.Fatal(err)
			}
			for i := 0; i < 2; i++ { // a watcher since removed from the registry
				h.appendLine(h.app.Paths.Answered(), h.answer(-10*time.Minute, cMutual, 1, 50+i))
			}
		}, cLead, nil, "", watch.RuleRateGlobal},
		{"chat spacing", func(h *watchHarness) {
			h.appendLine(h.app.Paths.Answered(), h.answer(-10*time.Second, cLead, 1, 99))
		}, cLead, nil, "", watch.RuleChatSpacing},
		{"saved messages", func(h *watchHarness) { h.writeState("sales", sessA, meID) }, meID, nil, "", watch.RuleChatType},
		{"bot", func(h *watchHarness) { h.writeState("sales", sessA, cBot) }, cBot, nil, "", watch.RuleChatType},
		{"channel", func(h *watchHarness) { h.writeState("sales", sessA, cChannel) }, cChannel, []string{"--reply-to", "1"}, "", watch.RuleChatType},
		{"group without groups", func(h *watchHarness) { h.set("sales", "groups", false) }, cGroup, []string{"--reply-to", "2"}, "", watch.RuleChatType},
		{"listed: not listed", nil, cStranger, nil, "", watch.RuleScope},
		{"contacts: not mutual", func(h *watchHarness) { h.set("sales", "scope", "contacts") }, cStranger, nil, "", watch.RuleScope},
		{"dm-first: I wrote first", func(h *watchHarness) { h.set("sales", "scope", "dm-first") }, cIWrote, nil, "", watch.RuleScope},
		{"dm-first: listed does not bypass", func(h *watchHarness) {
			h.set("sales", "scope", "dm-first")
			h.writeState("sales", sessA, cIWrote)
		}, cIWrote, nil, "", watch.RuleScope},
		{"group not listed", nil, cUnlisted, []string{"--reply-to", "1"}, "", watch.RuleScope},
		{"group not listed, contacts scope", func(h *watchHarness) { h.set("sales", "scope", "contacts") }, cUnlisted, []string{"--reply-to", "1"}, "", watch.RuleScope},
		{"empty chat", nil, cEmpty, nil, "", watch.RuleEmptyChat},
		{"latest is mine", func(h *watchHarness) { h.add(cLead, 3, true, "yes it is", -time.Minute) }, cLead, nil, "", watch.RuleLatestFromMe},
		{"dm owner backoff", func(h *watchHarness) {
			h.add(cLead, 3, true, "let me check", -4*time.Minute)
			h.add(cLead, 4, false, "ok", -3*time.Minute)
		}, cLead, nil, "", watch.RuleOwnerBackoff},
		{"dm backoff unverifiable", func(h *watchHarness) {
			for i := 3; i < 60; i++ {
				h.add(cLead, i, false, "spam", -time.Duration(60-i)*time.Second)
			}
		}, cLead, nil, "", watch.RuleUnverifiable},
		{"dm reply-to mine", nil, cLead, []string{"--reply-to", "1"}, "", watch.RuleTargetFromMe},
		{"dm reply-to missing", nil, cLead, []string{"--reply-to", "77"}, "", watch.RuleTargetMissing},
		{"dm already answered", func(h *watchHarness) {
			h.appendLine(h.app.Paths.Answered(), h.answer(-time.Hour, cLead, 2, 98))
		}, cLead, nil, "", watch.RuleAlreadyAnswered},
		{"group reply-to required", nil, cGroup, nil, "", watch.RuleReplyToRequired},
		{"group target missing", nil, cGroup, []string{"--reply-to", "999"}, "", watch.RuleTargetMissing},
		{"group target mine", nil, cGroup, []string{"--reply-to", "1"}, "", watch.RuleTargetFromMe},
		{"group already answered", func(h *watchHarness) {
			h.appendLine(h.app.Paths.Answered(), h.answer(-time.Hour, cGroup, 2, 97))
		}, cGroup, []string{"--reply-to", "2"}, "", watch.RuleAlreadyAnswered},
		{"group already replied by me", func(h *watchHarness) {
			h.add(cGroup, 5, false, "@owner price?", -40*time.Minute, mentioned)
			h.add(cGroup, 6, true, "50", -30*time.Minute, replyTo(5))
		}, cGroup, []string{"--reply-to", "5"}, "", watch.RuleAlreadyReplied},
		{"group not mentioned", nil, cGroup, []string{"--reply-to", "3"}, "", watch.RuleNotMentioned},
		{"group owner backoff", func(h *watchHarness) {
			h.add(cGroup, 5, true, "brb", -30*time.Second)
		}, cGroup, []string{"--reply-to", "2"}, "", watch.RuleOwnerBackoff},
		{"group target unverifiable", func(h *watchHarness) {
			h.add(cGroup, 5, false, "@owner old question", -10*time.Hour, mentioned)
			for i := 6; i < 6+2*200; i++ {
				h.add(cGroup, i, false, "busy", -9*time.Hour)
			}
		}, cGroup, []string{"--reply-to", "5"}, "", watch.RuleUnverifiable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newWatchHarness(t)
			if c.setup != nil {
				c.setup(h)
			}
			text := c.text
			if text == "" {
				text = "Yes, still available."
			}
			h.refused(c.rule, h.reply(c.chat, text, c.extra...)...)
			// --dry-run refuses for the same reason.
			h.refused(c.rule, h.reply(c.chat, text, append(c.extra, "--dry-run")...)...)
		})
	}
}

func TestWatchReplyOfflineRefusalsDoNotConnect(t *testing.T) {
	h := newWatchHarness(t)
	h.write(h.app.Paths.PauseAll(), "")
	h.refused(watch.RulePausedAll, h.reply(cLead, "hi")...)
	h.env[envSession] = sessB
	os.Remove(h.app.Paths.PauseAll())
	h.refused(watch.RuleNotActive, h.reply(cLead, "hi")...)
	if h.connects != 0 {
		t.Fatalf("connected %d times for offline refusals", h.connects)
	}
}

func TestWatchReplyDM(t *testing.T) {
	h := newWatchHarness(t)
	d := h.ok(h.reply(cLead, "  Yes, still available.  ")...)
	if len(h.fake.Sent) != 1 {
		t.Fatalf("sent %v", h.fake.Sent)
	}
	s := h.fake.Sent[0]
	if s.ChatID != cLead || s.Text != "Yes, still available." || s.ReplyTo != 0 {
		t.Fatalf("sent %+v", s)
	}
	sent := d["sent"].(map[string]any)
	if d["answers"].(float64) != 2 || sent["id"].(float64) != 3 || d["watcher"] != "sales" {
		t.Fatalf("output %v", d)
	}
	logs := readLines[watch.Send](t, filepath.Join(h.dir("sales"), watch.LogFile))
	if len(logs) != 1 || !logs[0].OK || logs[0].MsgID != 3 || logs[0].Answered != 2 || logs[0].By != sessA ||
		logs[0].ChatID != cLead || logs[0].Text != "Yes, still available." {
		t.Fatalf("sends.log %+v", logs)
	}
	ledger := readLines[watch.Answer](t, h.app.Paths.Answered())
	if len(ledger) != 1 || ledger[0].ChatID != cLead || ledger[0].MsgID != 2 || ledger[0].SentID != 3 || ledger[0].Watcher != "sales" {
		t.Fatalf("ledger %+v", ledger)
	}
	for _, p := range []string{filepath.Join(h.dir("sales"), watch.LogFile), h.app.Paths.Answered()} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, st.Mode())
		}
	}
	// No double reply: the latest message is now ours.
	h.clock = h.clock.Add(time.Minute)
	h.fake.Sent = nil
	h.refused(watch.RuleLatestFromMe, h.reply(cLead, "Anything else?")...)
}

func TestWatchReplyDMIgnoresOwnWatcherSendsInBackoff(t *testing.T) {
	h := newWatchHarness(t)
	h.ok(h.reply(cLead, "Yes.")...)
	h.clock = h.clock.Add(2 * time.Minute)
	h.add(cLead, 4, false, "great, where are you?", -30*time.Second)
	h.ok(h.reply(cLead, "Downtown.")...)
	if len(h.fake.Sent) != 2 {
		t.Fatalf("sent %v", h.fake.Sent)
	}
}

func TestWatchReplyDMReplyTo(t *testing.T) {
	h := newWatchHarness(t)
	d := h.ok(h.reply(cLead, "Yes.", "--reply-to", "2")...)
	if h.fake.Sent[0].ReplyTo != 2 || d["answers"].(float64) != 2 {
		t.Fatalf("sent %+v out %v", h.fake.Sent, d)
	}
}

func TestWatchReplyScopes(t *testing.T) {
	cases := []struct {
		scope string
		chat  int64
	}{
		{"contacts", cMutual},
		{"contacts", cLead}, // listed counts for contacts
		{"dm-first", cStranger},
		{"dm-first", cMutual},
	}
	for _, c := range cases {
		t.Run(c.scope+"/"+strconv.FormatInt(c.chat, 10), func(t *testing.T) {
			h := newWatchHarness(t)
			h.set("sales", "scope", c.scope)
			h.ok(h.reply(c.chat, "Hello!")...)
			if len(h.fake.Sent) != 1 || h.fake.Sent[0].ChatID != c.chat {
				t.Fatalf("sent %v", h.fake.Sent)
			}
		})
	}
}

func TestWatchReplyGroup(t *testing.T) {
	cases := []struct {
		name         string
		target       string
		mentionsOnly bool
	}{
		{"mention", "2", true},
		{"reply to my message", "4", true},
		{"plain with mentions_only off", "3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newWatchHarness(t)
			h.set("sales", "mentions_only", c.mentionsOnly)
			h.ok(h.reply(cGroup, "It's 50.", "--reply-to", c.target)...)
			want, _ := strconv.Atoi(c.target)
			if len(h.fake.Sent) != 1 || h.fake.Sent[0].ReplyTo != want {
				t.Fatalf("sent %+v", h.fake.Sent)
			}
			ledger := readLines[watch.Answer](t, h.app.Paths.Answered())
			if len(ledger) != 1 || ledger[0].MsgID != want || ledger[0].ChatID != cGroup {
				t.Fatalf("ledger %+v", ledger)
			}
			// The same target cannot be answered twice (ledger, and my reply in the chat).
			h.clock = h.clock.Add(time.Hour)
			h.fake.Sent = nil
			h.refused(watch.RuleAlreadyAnswered, h.reply(cGroup, "Again?", "--reply-to", c.target)...)
		})
	}
}

func TestWatchReplyLedgerBlocksWhenReplyIsGone(t *testing.T) {
	h := newWatchHarness(t)
	h.addWatcher("second", sessB, map[string]any{"groups": true}, cGroup)
	h.ok(h.reply(cGroup, "It's 50.", "--reply-to", "2")...)
	// The reply was deleted, so only the ledger knows message 2 was answered.
	h.fake.Messages[cGroup] = h.fake.Messages[cGroup][:4]
	h.clock = h.clock.Add(time.Hour)
	h.fake.Sent = nil
	h.env[envSession] = sessB
	h.refused(watch.RuleAlreadyAnswered, "watch", "reply", "--watcher", "second", "--chat", strconv.FormatInt(cGroup, 10),
		"--text", "50!", "--reply-to", "2")
}

func TestWatchReplyDryRun(t *testing.T) {
	h := newWatchHarness(t)
	d := h.ok(h.reply(cGroup, "It's 50.", "--reply-to", "2", "--dry-run")...)
	w := d["would_send"].(map[string]any)
	if d["dry_run"] != true || w["text"] != "It's 50." || w["reply_to"].(float64) != 2 || w["answers"].(float64) != 2 ||
		w["group"] != true || w["chat"].(map[string]any)["id"].(float64) != float64(cGroup) {
		t.Fatalf("dry run %v", d)
	}
	if len(h.fake.Sent) != 0 || h.fake.Calls["History"] == 0 {
		t.Fatalf("dry run sent %v / read history %d times", h.fake.Sent, h.fake.Calls["History"])
	}
	for _, p := range []string{filepath.Join(h.dir("sales"), watch.LogFile), h.app.Paths.Answered()} {
		if fileExists(p) {
			t.Fatalf("dry run wrote %s", p)
		}
	}
}

func TestWatchReplySendFailureIsLoggedNotLedgered(t *testing.T) {
	h := newWatchHarness(t)
	h.g.failSend = errors.New("network down")
	h.fail(output.ExitInternal, output.CodeInternal, h.reply(cLead, "Yes.")...)
	logs := readLines[watch.Send](t, filepath.Join(h.dir("sales"), watch.LogFile))
	if len(logs) != 1 || logs[0].OK || !strings.Contains(logs[0].Err, "network down") {
		t.Fatalf("sends.log %+v", logs)
	}
	if fileExists(h.app.Paths.Answered()) {
		t.Fatal("failed send reached the ledger")
	}
}

func TestWatchReplyUsage(t *testing.T) {
	h := newWatchHarness(t)
	h.fail(output.ExitUsage, output.CodeUsage, "watch", "reply", "--watcher", "sales", "--chat", "@lead", "--text", "x")
	h.fail(output.ExitUsage, output.CodeUsage, "watch", "reply", "--watcher", "sales", "--chat", "101")
	h.fail(output.ExitUsage, output.CodeUsage, h.reply(cLead, "x", "--reply-to", "-1")...)
}

func TestWatchReplyRuneLength(t *testing.T) {
	h := newWatchHarness(t)
	h.set("sales", "max_chars", 10)
	h.ok(h.reply(cLead, "Да, продаю")...) // 10 characters, 19 bytes
}

// Two watchers racing to answer the same message: exactly one reply goes out.
func TestWatchReplyConcurrentWatchers(t *testing.T) {
	for _, c := range []struct {
		name  string
		chat  int64
		extra []string
		rules []string
	}{
		{"group", cGroup, []string{"--reply-to", "2"}, []string{watch.RuleAlreadyAnswered, watch.RuleAlreadyReplied}},
		{"dm", cLead, nil, []string{watch.RuleLatestFromMe}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newWatchHarness(t)
			h.addWatcher("second", sessB, map[string]any{"groups": true}, cLead, cGroup)
			// Each clock read is a minute later, so per-chat spacing never hides the rule under test.
			tick := func() time.Time {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.clock = h.clock.Add(time.Minute)
				return h.clock
			}
			h.app.Now, h.fake.Now = tick, tick
			apps := []*App{h.app, h.cloneApp(sessB)}
			names := []string{"sales", "second"}
			outs := make([]*bytes.Buffer, 2)
			codes := make([]int, 2)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range apps {
				outs[i] = &bytes.Buffer{}
				apps[i].Stdout = outs[i]
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					args := append([]string{"watch", "reply", "--watcher", names[i], "--chat", strconv.FormatInt(c.chat, 10),
						"--text", fmt.Sprintf("answer from %s", names[i])}, c.extra...)
					codes[i] = apps[i].Execute(context.Background(), args)
				}(i)
			}
			close(start)
			wg.Wait()
			if len(h.fake.Sent) != 1 {
				t.Fatalf("sent %d replies: %+v", len(h.fake.Sent), h.fake.Sent)
			}
			ok, refused := 0, 0
			for i, code := range codes {
				switch code {
				case 0:
					ok++
				case output.ExitWatcher:
					var env envelope
					_ = json.Unmarshal(outs[i].Bytes(), &env)
					rule, _ := env.Error.Details["rule"].(string)
					if !containsStr(c.rules, rule) {
						t.Fatalf("loser refused with %q: %s", rule, env.Error.Message)
					}
					refused++
				default:
					t.Fatalf("exit %d: %s", code, outs[i].String())
				}
			}
			if ok != 1 || refused != 1 {
				t.Fatalf("ok %d refused %d", ok, refused)
			}
			if n := len(readLines[watch.Answer](t, h.app.Paths.Answered())); n != 1 {
				t.Fatalf("ledger has %d answers", n)
			}
		})
	}
}

func containsStr(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

// cloneApp is a second process: same data dir and Telegram, its own caller id.
func (h *watchHarness) cloneApp(session string) *App {
	a := *h.app
	a.Stdout = io.Discard
	a.Getenv = func(k string) string {
		if k == envSession {
			return session
		}
		return h.env[k]
	}
	return &a
}

func TestWatchList(t *testing.T) {
	h := newWatchHarness(t)
	h.addWatcher("other", sessB, map[string]any{"scope": "bogus"})
	h.write(filepath.Join(h.dir("other"), watch.PauseFile), "")
	h.ok(h.reply(cLead, "Yes.")...)
	d := h.ok("watch", "list")
	rows := list(d["watchers"])
	if len(rows) != 2 || d["pause_all"] != false || d["global_sends_last_hour"].(float64) != 1 ||
		d["global_per_hour"].(float64) != config.DefaultGlobalPerHour {
		t.Fatalf("list %v", d)
	}
	other, sales := rows[0], rows[1]
	if other["name"] != "other" || other["valid"] != false || other["paused"] != true || len(other["problems"].([]any)) != 1 {
		t.Fatalf("other %v", other)
	}
	if sales["valid"] != true || sales["sends_last_hour"].(float64) != 1 || sales["listed_chats"].(float64) != 3 ||
		sales["caller_is_active"] != true || sales["last_send"] == nil || sales["active_watcher"] != sessA ||
		sales["max_chars"].(float64) != watch.DefaultMaxChars || sales["mentions_only"] != true {
		t.Fatalf("sales %v", sales)
	}
	if h.connects != 1 {
		t.Fatalf("watch list connected (%d)", h.connects)
	}
}

func TestWatchListEmpty(t *testing.T) {
	h := newWatchHarness(t)
	os.Remove(h.app.Paths.Watchers())
	d := h.ok("watch", "list")
	if d["registry_exists"] != false || len(d["watchers"].([]any)) != 0 {
		t.Fatalf("empty list %v", d)
	}
}

func TestWatchCheck(t *testing.T) {
	h := newWatchHarness(t)
	d := h.ok("watch", "check", "--watcher", "sales")
	if d["valid"] != true || len(d["chats"].([]any)) != 3 || len(d["warnings"].([]any)) != 0 {
		t.Fatalf("check %v", d)
	}
	h.set("sales", "approved", "")
	h.set("sales", "per_chat_seconds", -1)
	h.write(filepath.Join(h.dir("sales"), watch.StateFile), `{"active_watcher":"x","chats":{"abc":"n"}}`)
	d = h.ok("watch", "check", "--watcher", "sales")
	w := d["watcher"].(map[string]any)
	if d["valid"] != false || len(w["problems"].([]any)) != 2 || len(d["warnings"].([]any)) != 2 {
		t.Fatalf("check %v", d)
	}
	h.fail(output.ExitNotFound, output.CodeNotFound, "watch", "check", "--watcher", "nobody")
	if h.connects != 0 {
		t.Fatal("watch check connected")
	}
}

func TestWatchHelpDocumentsRegistry(t *testing.T) {
	h := newWatchHarness(t)
	var out bytes.Buffer
	h.app.Stdout = &out
	h.app.Execute(context.Background(), []string{"watch", "--help"})
	for _, want := range []string{"watchers.json", `"scope"`, "per_chat_seconds", "PAUSE-ALL", "answered.jsonl", "TGAC_WATCHER_SESSION"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("watch --help lacks %q", want)
		}
	}
}
