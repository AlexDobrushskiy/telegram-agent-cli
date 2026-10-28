package watch

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// Refusal rule ids, reported as error.details.rule.
const (
	RulePausedAll       = "paused_all"
	RuleNotRegistered   = "not_registered"
	RuleInvalid         = "invalid_registration"
	RulePaused          = "paused"
	RuleState           = "state"
	RuleNotActive       = "not_active_watcher"
	RuleTextLength      = "text_length"
	RuleRateWatcher     = "rate_watcher"
	RuleRateGlobal      = "rate_global"
	RuleChatSpacing     = "chat_spacing"
	RuleChatType        = "chat_type"
	RuleScope           = "scope"
	RuleEmptyChat       = "empty_chat"
	RuleLatestFromMe    = "latest_from_me"
	RuleOwnerBackoff    = "owner_backoff"
	RuleUnverifiable    = "unverifiable"
	RuleReplyToRequired = "reply_to_required"
	RuleTargetMissing   = "target_missing"
	RuleTargetFromMe    = "target_from_me"
	RuleAlreadyAnswered = "already_answered"
	RuleAlreadyReplied  = "already_replied"
	RuleNotMentioned    = "not_mentioned"
)

const (
	historyWindow = 50  // 1:1: messages read for the latest/backoff checks
	groupWindow   = 200 // groups: messages read for backoff and existing replies
	firstMessages = 5   // dm-first: oldest messages read to see who wrote first
)

// Refuse builds a watcher refusal (exit 7) with a stable rule id.
func Refuse(rule, format string, a ...any) *output.Error {
	return output.WatcherRefused(format, a...).WithDetail("rule", rule)
}

// Gate runs the checks for one watcher's send.
type Gate struct {
	Paths         config.Paths
	Registry      *Registry
	W             *Watcher
	GlobalPerHour int
	Now           func() time.Time
}

// Open checks PAUSE-ALL and the registration, in that order.
func Open(p config.Paths, name string, globalPerHour int, now func() time.Time) (*Gate, error) {
	if exists(p.PauseAll()) {
		return nil, Refuse(RulePausedAll, "PAUSE-ALL is present in the data dir: every watcher is paused")
	}
	reg, err := LoadRegistry(p.Watchers())
	if err != nil {
		return nil, Refuse(RuleInvalid, "%v", err)
	}
	if !reg.Has(name) {
		return nil, Refuse(RuleNotRegistered, "watcher %q is not registered in watchers.json (the user registers watchers)", name)
	}
	w, problems := reg.Watcher(name)
	if len(problems) > 0 {
		return nil, Refuse(RuleInvalid, "watcher %q registration is invalid: %s", name, strings.Join(problems, "; "))
	}
	return &Gate{Paths: p, Registry: reg, W: w, GlobalPerHour: globalPerHour, Now: now}, nil
}

// Paused refuses when PAUSE-ALL or the watcher's PAUSE exists.
func (g *Gate) Paused() error {
	if exists(g.Paths.PauseAll()) {
		return Refuse(RulePausedAll, "PAUSE-ALL is present in the data dir: every watcher is paused")
	}
	if exists(g.W.PausePath()) {
		return Refuse(RulePaused, "PAUSE file in %s: watcher %q is paused", g.W.Dir, g.W.Name)
	}
	return nil
}

// Offline runs the checks that need neither the chat nor the logs.
// It returns the watcher state for the scope check.
func (g *Gate) Offline(caller, text string) (*State, error) {
	if err := g.Paused(); err != nil {
		return nil, err
	}
	st, err := LoadState(g.W.StatePath())
	if err != nil {
		return nil, Refuse(RuleState, "cannot read %s: %v", g.W.StatePath(), err)
	}
	if st.ActiveWatcher == "" {
		return nil, Refuse(RuleNotActive, "state.json has no active_watcher")
	}
	if caller == "" || caller != st.ActiveWatcher {
		shown := caller
		if shown == "" {
			shown = "(unset)"
		}
		return nil, Refuse(RuleNotActive, "caller %s is not the active watcher %s", shown, st.ActiveWatcher)
	}
	if n := utf8.RuneCountInString(text); n < 1 || n > g.W.MaxChars {
		return nil, Refuse(RuleTextLength, "text length %d not in 1..%d", n, g.W.MaxChars)
	}
	return st, nil
}

// Ledger is answered.jsonl, read once per send.
type Ledger struct {
	Answers []Answer
}

// LoadLedger reads the shared ledger.
func (g *Gate) LoadLedger() (*Ledger, error) {
	a, _, err := ReadJSONL[Answer](g.Paths.Answered())
	if err != nil {
		return nil, err
	}
	return &Ledger{Answers: a}, nil
}

// Answered reports whether (chat, msg) was answered by any watcher.
func (l *Ledger) Answered(chat int64, msg int) bool {
	for _, a := range l.Answers {
		if a.ChatID == chat && a.MsgID == msg {
			return true
		}
	}
	return false
}

// SentByWatcher reports whether (chat, msg) is a reply some watcher sent.
func (l *Ledger) SentByWatcher(chat int64, msg int) bool {
	for _, a := range l.Answers {
		if a.ChatID == chat && a.SentID == msg {
			return true
		}
	}
	return false
}

// Limits checks the per-watcher and global hourly limits and per-chat spacing.
// Every logged attempt counts, failed ones too.
func (g *Gate) Limits(chat int64, l *Ledger) error {
	now := g.Now()
	hourAgo := now.Add(-time.Hour)
	own, _, err := ReadJSONL[Send](g.W.LogPath())
	if err != nil {
		return err
	}
	if n := countSince(own, hourAgo); n >= g.W.PerHour {
		return Refuse(RuleRateWatcher, "watcher %q sent %d in the last hour (limit %d)", g.W.Name, n, g.W.PerHour)
	}
	// Global: across every registered watcher's log, or the ledger if that is larger
	// (it also covers watchers since removed from the registry).
	global := 0
	for _, name := range g.Registry.Names() {
		w, _ := g.Registry.Watcher(name)
		if w == nil || w.Dir == "" {
			continue
		}
		sends, _, err := ReadJSONL[Send](w.LogPath())
		if err != nil {
			continue
		}
		global += countSince(sends, hourAgo)
	}
	ledger := 0
	for _, a := range l.Answers {
		if a.Time().After(hourAgo) {
			ledger++
		}
	}
	if n := max(global, ledger); n >= g.GlobalPerHour {
		return Refuse(RuleRateGlobal, "all watchers sent %d in the last hour (global_per_hour %d)", n, g.GlobalPerHour)
	}
	spacing := time.Duration(g.W.PerChatSeconds) * time.Second
	last := time.Time{}
	for _, s := range own {
		if s.ChatID == chat && s.Time().After(last) {
			last = s.Time()
		}
	}
	for _, a := range l.Answers {
		if a.ChatID == chat && a.Time().After(last) {
			last = a.Time()
		}
	}
	if !last.IsZero() && now.Sub(last) < spacing {
		return Refuse(RuleChatSpacing, "a watcher replied in this chat %.0fs ago (per_chat_seconds %d)",
			now.Sub(last).Seconds(), g.W.PerChatSeconds)
	}
	return nil
}

func countSince(sends []Send, since time.Time) int {
	n := 0
	for _, s := range sends {
		if s.Time().After(since) {
			n++
		}
	}
	return n
}

// Plan is what a passing check would send.
type Plan struct {
	Chat    tgclient.ChatRef `json:"chat"`
	ReplyTo int              `json:"reply_to,omitempty"`
	Answers int              `json:"answers"` // the incoming message being answered
	Group   bool             `json:"group"`
	Text    string           `json:"text"`
	Watcher string           `json:"watcher"`
}

// CheckChat runs the chat-type, scope and conversation rules against the live chat.
func (g *Gate) CheckChat(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, self *tgclient.User,
	st *State, l *Ledger, replyTo int, text string) (*Plan, error) {
	plan := &Plan{Chat: p.Ref(), ReplyTo: replyTo, Text: text, Watcher: g.W.Name}
	switch {
	case self != nil && p.ID == self.ID:
		return nil, Refuse(RuleChatType, "chat %d is Saved Messages", p.ID)
	case p.Type == "user":
	case p.IsGroup():
		if !g.W.Groups {
			return nil, Refuse(RuleChatType, "chat %d is a %s and watcher %q is 1:1 only (groups: false)", p.ID, p.Type, g.W.Name)
		}
		plan.Group = true
	default:
		return nil, Refuse(RuleChatType, "chat %d is a %s: watchers answer people in 1:1 chats or groups", p.ID, p.Type)
	}
	if err := g.checkScope(ctx, t, p, st, plan.Group); err != nil {
		return nil, err
	}
	var err error
	if plan.Group {
		err = g.checkGroup(ctx, t, p, l, plan)
	} else {
		err = g.checkDM(ctx, t, p, l, plan)
	}
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (g *Gate) checkScope(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, st *State, group bool) error {
	_, listed := st.Chats[p.ID]
	if group {
		if !listed {
			return Refuse(RuleScope, "group %d is not in state.json chats (groups must be listed)", p.ID)
		}
		return nil
	}
	switch g.W.Scope {
	case ScopeListed:
		if !listed {
			return Refuse(RuleScope, "chat %d is not in state.json chats (scope listed)", p.ID)
		}
	case ScopeContacts:
		if listed {
			return nil
		}
		info, err := t.PeerInfo(ctx, p)
		if err != nil {
			return err
		}
		if info.MutualContact == nil || !*info.MutualContact {
			return Refuse(RuleScope, "chat %d is neither a mutual contact nor in state.json chats (scope contacts)", p.ID)
		}
	case ScopeDMFirst:
		first, err := t.History(ctx, p, tgclient.HistoryQuery{Limit: firstMessages, Reverse: true})
		if err != nil {
			return err
		}
		for _, m := range first {
			if m.Service != "" {
				continue
			}
			if m.FromMe {
				return Refuse(RuleScope, "the owner wrote first in chat %d (scope dm-first)", p.ID)
			}
			return nil
		}
		return Refuse(RuleScope, "chat %d has no messages from them (scope dm-first)", p.ID)
	default:
		return Refuse(RuleInvalid, "unknown scope %q", g.W.Scope)
	}
	return nil
}

func (g *Gate) backoff() time.Duration { return time.Duration(g.W.BackoffMinutes) * time.Minute }

// ownerWroteRecently refuses if the owner wrote in recent (newest first) within backoff.
// Watcher replies (known via the ledger) do not count. full reports that recent
// holds the whole chat history.
func (g *Gate) ownerWroteRecently(p *tgclient.Peer, recent []tgclient.Message, full bool, l *Ledger) error {
	since := g.Now().Add(-g.backoff())
	for _, m := range recent {
		if m.Time.Before(since) {
			return nil
		}
		if m.FromMe && !l.SentByWatcher(p.ID, m.ID) {
			return Refuse(RuleOwnerBackoff, "the owner wrote in this chat %s ago (backoff_minutes %d): they are handling it",
				g.Now().Sub(m.Time).Round(time.Second), g.W.BackoffMinutes)
		}
	}
	if !full && len(recent) > 0 {
		return Refuse(RuleUnverifiable, "more than %d messages in the last %d min: cannot verify the backoff rule",
			len(recent), g.W.BackoffMinutes)
	}
	return nil
}

func (g *Gate) checkDM(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, l *Ledger, plan *Plan) error {
	recent, err := t.History(ctx, p, tgclient.HistoryQuery{Limit: historyWindow})
	if err != nil {
		return err
	}
	var latest *tgclient.Message
	for i := range recent {
		if recent[i].Service == "" {
			latest = &recent[i]
			break
		}
	}
	if latest == nil {
		return Refuse(RuleEmptyChat, "chat %d has no messages", p.ID)
	}
	if latest.FromMe {
		return Refuse(RuleLatestFromMe, "the latest message (%d) is from this account: no double replies", latest.ID)
	}
	if err := g.ownerWroteRecently(p, recent, len(recent) < historyWindow, l); err != nil {
		return err
	}
	target := latest.ID
	if plan.ReplyTo > 0 {
		m, err := g.message(ctx, t, p, recent, plan.ReplyTo)
		if err != nil {
			return err
		}
		if m.FromMe {
			return Refuse(RuleTargetFromMe, "message %d is from this account: reply to their message", plan.ReplyTo)
		}
		target = plan.ReplyTo
	}
	if l.Answered(p.ID, latest.ID) || l.Answered(p.ID, target) {
		return Refuse(RuleAlreadyAnswered, "a watcher already answered message %d in this chat", target)
	}
	plan.Answers = target
	return nil
}

func (g *Gate) checkGroup(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, l *Ledger, plan *Plan) error {
	if plan.ReplyTo <= 0 {
		return Refuse(RuleReplyToRequired, "in groups --reply-to is required (the message you answer)")
	}
	recent, err := t.History(ctx, p, tgclient.HistoryQuery{Limit: groupWindow})
	if err != nil {
		return err
	}
	full := len(recent) < groupWindow
	target, err := g.message(ctx, t, p, recent, plan.ReplyTo)
	if err != nil {
		return err
	}
	if target.Service != "" {
		return Refuse(RuleTargetMissing, "message %d is a service message", target.ID)
	}
	if target.FromMe {
		return Refuse(RuleTargetFromMe, "message %d is from this account", target.ID)
	}
	if l.Answered(p.ID, target.ID) {
		return Refuse(RuleAlreadyAnswered, "a watcher already answered message %d", target.ID)
	}
	// Replies to the target are newer than it; recent covers them all when it
	// holds the whole history or reaches back past the target.
	after := recent
	if !full && (len(recent) == 0 || recent[len(recent)-1].ID > target.ID) {
		after, err = t.History(ctx, p, tgclient.HistoryQuery{MinID: target.ID, Limit: groupWindow})
		if err != nil {
			return err
		}
		if len(after) >= groupWindow {
			return Refuse(RuleUnverifiable, "%d+ messages since %d: cannot verify nobody answered it", groupWindow, target.ID)
		}
	}
	for _, m := range after {
		if m.FromMe && m.ReplyTo == target.ID {
			return Refuse(RuleAlreadyReplied, "message %d from this account already replies to %d", m.ID, target.ID)
		}
	}
	if g.W.MentionsOnly {
		ok, err := AddressedToMe(ctx, t, p, recent, target)
		if err != nil {
			return err
		}
		if !ok {
			return Refuse(RuleNotMentioned, "message %d neither mentions the owner nor replies to them (mentions_only)", target.ID)
		}
	}
	if err := g.ownerWroteRecently(p, recent, full, l); err != nil {
		return err
	}
	plan.Answers = target.ID
	return nil
}

// message finds id in known, else fetches it.
func (g *Gate) message(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, known []tgclient.Message, id int) (*tgclient.Message, error) {
	m, err := findMessage(ctx, t, p, known, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, Refuse(RuleTargetMissing, "message %d not found in chat %d", id, p.ID)
	}
	return m, nil
}

func findMessage(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, known []tgclient.Message, id int) (*tgclient.Message, error) {
	for i := range known {
		if known[i].ID == id {
			return &known[i], nil
		}
	}
	got, err := t.MessagesByID(ctx, p, []int{id})
	if err != nil {
		return nil, err
	}
	for i := range got {
		if got[i].ID == id {
			return &got[i], nil
		}
	}
	return nil, nil
}

// AddressedToMe: m mentions the user (Telegram's flag) or replies to one of their messages.
// known saves a fetch when it holds the parent.
func AddressedToMe(ctx context.Context, t tgclient.Telegram, p *tgclient.Peer, known []tgclient.Message, m *tgclient.Message) (bool, error) {
	if m.Mentioned {
		return true, nil
	}
	if m.ReplyTo <= 0 {
		return false, nil
	}
	parent, err := findMessage(ctx, t, p, known, m.ReplyTo)
	if err != nil {
		return false, err
	}
	return parent != nil && parent.FromMe, nil
}

// Record appends the attempt to sends.log and, on success, the answer to the ledger.
// Callers hold the watch lock.
func (g *Gate) Record(caller string, plan *Plan, sent *tgclient.Message, sendErr error) error {
	ts, at := stamp(g.Now())
	s := Send{T: ts, At: at, Watcher: g.W.Name, By: caller, ChatID: plan.Chat.ID, ReplyTo: plan.ReplyTo,
		Answered: plan.Answers, Text: plan.Text, OK: sendErr == nil && sent != nil}
	if s.OK {
		s.MsgID = sent.ID
	} else if sendErr != nil {
		s.Err = sendErr.Error()
	}
	if err := AppendJSONL(g.W.LogPath(), s); err != nil {
		return fmt.Errorf("record send: %w", err)
	}
	if !s.OK {
		return nil
	}
	a := Answer{T: ts, At: at, Watcher: g.W.Name, ChatID: plan.Chat.ID, MsgID: plan.Answers, SentID: sent.ID}
	if err := AppendJSONL(g.Paths.Answered(), a); err != nil {
		return fmt.Errorf("record answer: %w", err)
	}
	return nil
}
