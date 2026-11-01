package e2e

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// Wait kinds.
const (
	ForReply    = "reply"    // a new bot message after --since
	ForEdit     = "edit"     // a bot message's text, markup or media changes
	ForReaction = "reaction" // reactions on a message change (either side)
	ForPin      = "pin"      // the pinned message changes or is edited
	ForKeyboard = "keyboard" // the reply keyboard in effect changes
	ForDraft    = "draft"    // a live draft (sendMessageDraft) arrives
)

// Kinds lists the wait kinds.
var Kinds = []string{ForReply, ForEdit, ForReaction, ForPin, ForKeyboard, ForDraft}

// WaitSpec says what to wait for.
type WaitSpec struct {
	For      string
	Since    int // reply/draft: only messages with a larger id count (0 = newest at start)
	Msg      int // edit/reaction: watch this message only (0 = the recent ones)
	Timeout  time.Duration
	Interval time.Duration
	// All (draft): keep collecting drafts until the final message lands.
	All bool
	// IgnoreMine (reaction): only the other side's reactions count.
	IgnoreMine bool
}

// Result is what a wait saw.
type Result struct {
	For      string                `json:"for"`
	Elapsed  float64               `json:"elapsed_s"`
	Message  *tgclient.BotMessage  `json:"message,omitempty"`  // the reply / edited / reacted / pinned message
	Messages []tgclient.BotMessage `json:"messages,omitempty"` // reply: every new bot message, oldest first
	Before   any                   `json:"before,omitempty"`   // edit/reaction/pin/keyboard: the state at start
	Keyboard *Keyboard             `json:"keyboard,omitempty"`
	Drafts   []tgclient.Draft      `json:"drafts,omitempty"`
}

// Waiter polls one bot chat. Now and Sleep are injectable for tests.
type Waiter struct {
	T     tgclient.E2E
	Peer  *tgclient.Peer
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// DefaultInterval is the polling period.
const DefaultInterval = time.Second

// recentWindow is how many recent messages edit/reaction waits watch.
const recentWindow = 20

// historyPage is how many messages one history read asks for (Telegram's maximum).
const historyPage = 100

// ReadKeyboard returns the reply keyboard the chat shows now (nil = none). It
// reads back until a bot message sets or removes one, or the chat starts.
func ReadKeyboard(ctx context.Context, t tgclient.E2E, p *tgclient.Peer) (*Keyboard, error) {
	before := 0
	for {
		msgs, err := t.BotHistoryBefore(ctx, p, before, historyPage)
		if err != nil || len(msgs) == 0 {
			return nil, err
		}
		if kb, decided := keyboardIn(msgs); decided {
			return kb, nil
		}
		before = msgs[len(msgs)-1].ID
	}
}

// ReadPinned returns the pinned message (nil = none).
func ReadPinned(ctx context.Context, t tgclient.E2E, p *tgclient.Peer) (*tgclient.BotMessage, error) {
	id, err := t.PinnedID(ctx, p)
	if err != nil || id == 0 {
		return nil, err
	}
	msgs, err := t.BotMessagesByID(ctx, p, []int{id})
	if err != nil || len(msgs) == 0 {
		return nil, err
	}
	return &msgs[0], nil
}

// NewestID is the newest message id in the chat (0 = empty).
func NewestID(ctx context.Context, t tgclient.E2E, p *tgclient.Peer) (int, error) {
	msgs, err := t.BotHistory(ctx, p, 0, 1)
	if err != nil || len(msgs) == 0 {
		return 0, err
	}
	return msgs[0].ID, nil
}

func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// editState is what an edit changes. edit_date is left out: Telegram also bumps
// it when a reaction changes.
func editState(m tgclient.BotMessage) any {
	return struct {
		Text     string              `json:"text"`
		Entities []tgclient.Entity   `json:"entities,omitempty"`
		Inline   [][]tgclient.Button `json:"inline_keyboard,omitempty"`
		Media    *tgclient.Media     `json:"media,omitempty"`
		MediaID  int64               `json:"media_id,omitempty"`
	}{m.Text, m.Entities, m.Inline, m.Media, m.MediaID}
}

func reactionState(m tgclient.BotMessage) any { return m.Reactions }

// pinState is what a pin change changes: which message, and its content.
func pinState(m *tgclient.BotMessage) any {
	if m == nil {
		return nil
	}
	return []any{m.ID, editState(*m)}
}

// othersReactions is reactionState without our own reaction.
func othersReactions(m tgclient.BotMessage) any {
	var out []tgclient.Reaction
	for _, r := range m.Reactions {
		if r.Mine {
			r.Count--
			r.Mine = false
		}
		r.By = nil
		if r.Count > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Watch is a wait whose starting state is already taken. Begin it before a
// write: whatever the write causes counts, however fast it lands.
type Watch struct {
	w     *Waiter
	s     WaitSpec
	res   *Result
	check func(ctx context.Context) (bool, error)
	only  int // edit/reaction: set by Only
}

// Only narrows an edit or reaction wait to message id, typically the one the
// write just created.
func (x *Watch) Only(id int) { x.only = id }

// Wait polls until the event happens or the timeout passes (exit 9).
func (w *Waiter) Wait(ctx context.Context, s WaitSpec) (*Result, error) {
	x, err := w.Begin(ctx, s)
	if err != nil {
		return nil, err
	}
	return x.Wait(ctx)
}

// Begin takes the starting state the wait compares against.
func (w *Waiter) Begin(ctx context.Context, s WaitSpec) (*Watch, error) {
	if s.Interval <= 0 {
		s.Interval = DefaultInterval
	}
	x := &Watch{w: w, s: s, res: &Result{For: s.For}}
	res := x.res
	if !slices.Contains(Kinds, s.For) {
		return nil, output.Usage("--for must be one of %v", Kinds)
	}
	since := s.Since
	if since <= 0 && (s.For == ForReply || s.For == ForDraft) {
		var err error
		if since, err = NewestID(ctx, w.T, w.Peer); err != nil {
			return nil, err
		}
	}

	switch s.For {
	case ForReply:
		x.check = func(ctx context.Context) (bool, error) {
			msgs, err := w.newBotMessages(ctx, since)
			if err != nil || len(msgs) == 0 {
				return false, err
			}
			res.Messages, res.Message = msgs, &msgs[0]
			return true, nil
		}
	case ForEdit, ForReaction:
		edit := s.For == ForEdit
		state := editState
		if !edit {
			state = reactionState
			if s.IgnoreMine {
				state = othersReactions
			}
		}
		before, newest, err := w.snapshot(ctx, s.Msg, edit)
		if err != nil {
			return nil, err
		}
		x.check = func(ctx context.Context) (bool, error) {
			msg := s.Msg
			if x.only > 0 {
				msg = x.only
			}
			now, _, err := w.snapshot(ctx, msg, edit)
			if err != nil {
				return false, err
			}
			for _, m := range now {
				old, seen := before[m.ID]
				if !seen {
					if m.ID <= newest {
						continue // older than the start, outside the window then
					}
					// A message that arrived after Begin started with no reactions and,
					// unless it is already marked edited, as it is now.
					old = m
					old.Reactions = nil
					if edit && (m.Edited == "" || len(m.Reactions) > 0) {
						before[m.ID] = m
						continue
					}
					if edit {
						old.Text, old.Entities, old.Inline, old.Media, old.MediaID = "", nil, nil, nil, 0
					}
				}
				if fingerprint(state(old)) != fingerprint(state(m)) {
					mm := m
					res.Message = &mm
					if seen || !edit {
						res.Before = state(old)
					}
					return true, nil
				}
			}
			return false, nil
		}
	case ForPin:
		before, err := ReadPinned(ctx, w.T, w.Peer)
		if err != nil {
			return nil, err
		}
		x.check = func(ctx context.Context) (bool, error) {
			now, err := ReadPinned(ctx, w.T, w.Peer)
			if err != nil {
				return false, err
			}
			if fingerprint(pinState(now)) != fingerprint(pinState(before)) {
				res.Message, res.Before = now, before
				return true, nil
			}
			return false, nil
		}
	case ForKeyboard:
		newest, err := NewestID(ctx, w.T, w.Peer)
		if err != nil {
			return nil, err
		}
		before, err := ReadKeyboard(ctx, w.T, w.Peer)
		if err != nil {
			return nil, err
		}
		// Only messages after Begin can change it; the rest is read once.
		x.check = func(ctx context.Context) (bool, error) {
			msgs, err := w.T.BotHistory(ctx, w.Peer, newest, historyPage)
			if err != nil {
				return false, err
			}
			now, decided := keyboardIn(msgs)
			if !decided {
				now = before
			}
			if fingerprint(now) != fingerprint(before) {
				res.Keyboard, res.Before = now, before
				return true, nil
			}
			return false, nil
		}
	case ForDraft:
		x.check = func(ctx context.Context) (bool, error) {
			for {
				select {
				case d := <-w.T.Drafts():
					if d.UserID == w.Peer.RawID {
						res.Drafts = append(res.Drafts, d)
					}
					continue
				default:
				}
				break
			}
			if !s.All {
				return len(res.Drafts) > 0, nil
			}
			msgs, err := w.newBotMessages(ctx, since)
			if err != nil || len(msgs) == 0 {
				return false, err
			}
			res.Messages, res.Message = msgs, &msgs[len(msgs)-1]
			return true, nil
		}
	}
	return x, nil
}

// Wait polls until the event happens or the timeout passes (exit 9). The
// timeout counts from here, not from Begin.
func (x *Watch) Wait(ctx context.Context) (*Result, error) {
	w, s, res := x.w, x.s, x.res
	start := w.Now()
	deadline := start.Add(s.Timeout)
	timeout := func() error {
		e := output.Timeout("no %s within %s", s.For, s.Timeout).WithDetail("for", s.For).
			WithDetail("timeout_s", s.Timeout.Seconds())
		if len(res.Drafts) > 0 {
			e = e.WithDetail("drafts", res.Drafts)
		}
		return e
	}
	// The polls share the timeout: one stuck in a FLOOD_WAIT ends with it.
	pctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	for {
		ok, err := x.check(pctx)
		if err != nil {
			if ctx.Err() == nil && pctx.Err() != nil {
				return nil, timeout()
			}
			return nil, err
		}
		if ok {
			res.Elapsed = float64(w.Now().Sub(start).Milliseconds()) / 1000
			return res, nil
		}
		if !w.Now().Before(deadline) {
			return nil, timeout()
		}
		if err := w.Sleep(ctx, min(s.Interval, deadline.Sub(w.Now()))); err != nil {
			return nil, err
		}
	}
}

// newBotMessages returns the bot's messages after since, oldest first.
func (w *Waiter) newBotMessages(ctx context.Context, since int) ([]tgclient.BotMessage, error) {
	msgs, err := w.T.BotHistory(ctx, w.Peer, since, 100)
	if err != nil {
		return nil, err
	}
	var out []tgclient.BotMessage
	for _, m := range msgs {
		if m.From == "bot" {
			out = append(out, m)
		}
	}
	slices.Reverse(out)
	return out, nil
}

// snapshot maps id → message for the watched message or the recent ones
// (only the bot's when botOnly), and gives the newest id it saw.
func (w *Waiter) snapshot(ctx context.Context, msg int, botOnly bool) (map[int]tgclient.BotMessage, int, error) {
	var msgs []tgclient.BotMessage
	var err error
	if msg > 0 {
		msgs, err = w.T.BotMessagesByID(ctx, w.Peer, []int{msg})
		if err == nil && len(msgs) == 0 {
			return nil, 0, output.NotFound("message %d not found in this chat", msg)
		}
	} else {
		msgs, err = w.T.BotHistory(ctx, w.Peer, 0, recentWindow)
	}
	if err != nil {
		return nil, 0, err
	}
	out := map[int]tgclient.BotMessage{}
	newest := 0
	for _, m := range msgs {
		newest = max(newest, m.ID)
		if botOnly && msg == 0 && m.From != "bot" {
			continue
		}
		out[m.ID] = m
	}
	return out, newest, nil
}
