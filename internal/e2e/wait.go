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

// keyboardScan is how far back the keyboard in effect is looked for.
const keyboardScan = 100

// ReadKeyboard returns the reply keyboard the chat shows now (nil = none).
func ReadKeyboard(ctx context.Context, t tgclient.E2E, p *tgclient.Peer) (*Keyboard, error) {
	msgs, err := t.BotHistory(ctx, p, 0, keyboardScan)
	if err != nil {
		return nil, err
	}
	return CurrentKeyboard(msgs), nil
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

// editState is what an edit changes.
func editState(m tgclient.BotMessage) any {
	return struct {
		Edited, Text string
		Entities     []tgclient.Entity
		Inline       [][]tgclient.Button
		Media        *tgclient.Media
	}{m.Edited, m.Text, m.Entities, m.Inline, m.Media}
}

func reactionState(m tgclient.BotMessage) any { return m.Reactions }

// Wait polls until the event happens or the timeout passes (exit 9).
func (w *Waiter) Wait(ctx context.Context, s WaitSpec) (*Result, error) {
	if s.Interval <= 0 {
		s.Interval = DefaultInterval
	}
	start := w.Now()
	deadline := start.Add(s.Timeout)
	res := &Result{For: s.For}
	done := func() (*Result, error) {
		res.Elapsed = float64(w.Now().Sub(start).Milliseconds()) / 1000
		return res, nil
	}

	var check func() (bool, error)
	switch s.For {
	case ForReply:
		since, err := w.since(ctx, s.Since)
		if err != nil {
			return nil, err
		}
		check = func() (bool, error) {
			msgs, err := w.newBotMessages(ctx, since)
			if err != nil || len(msgs) == 0 {
				return false, err
			}
			res.Messages, res.Message = msgs, &msgs[0]
			return true, nil
		}
	case ForEdit, ForReaction:
		state := editState
		if s.For == ForReaction {
			state = reactionState
		}
		before, err := w.snapshot(ctx, s.Msg, s.For == ForEdit)
		if err != nil {
			return nil, err
		}
		check = func() (bool, error) {
			now, err := w.snapshot(ctx, s.Msg, s.For == ForEdit)
			if err != nil {
				return false, err
			}
			for _, m := range now {
				old, seen := before[m.ID]
				if !seen {
					continue // new messages are not edits or reactions on watched ones
				}
				if fingerprint(state(old)) != fingerprint(state(m)) {
					mm := m
					res.Message, res.Before = &mm, state(old)
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
		check = func() (bool, error) {
			now, err := ReadPinned(ctx, w.T, w.Peer)
			if err != nil {
				return false, err
			}
			if fingerprint(now) != fingerprint(before) {
				res.Message, res.Before = now, before
				return true, nil
			}
			return false, nil
		}
	case ForKeyboard:
		before, err := ReadKeyboard(ctx, w.T, w.Peer)
		if err != nil {
			return nil, err
		}
		check = func() (bool, error) {
			now, err := ReadKeyboard(ctx, w.T, w.Peer)
			if err != nil {
				return false, err
			}
			if fingerprint(now) != fingerprint(before) {
				res.Keyboard, res.Before = now, before
				return true, nil
			}
			return false, nil
		}
	case ForDraft:
		since, err := w.since(ctx, s.Since)
		if err != nil {
			return nil, err
		}
		check = func() (bool, error) {
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
	default:
		return nil, output.Usage("--for must be one of %v", Kinds)
	}

	for {
		ok, err := check()
		if err != nil {
			return nil, err
		}
		if ok {
			return done()
		}
		if !w.Now().Before(deadline) {
			e := output.Timeout("no %s within %s", s.For, s.Timeout).WithDetail("for", s.For).
				WithDetail("timeout_s", s.Timeout.Seconds())
			if len(res.Drafts) > 0 {
				e = e.WithDetail("drafts", res.Drafts)
			}
			return nil, e
		}
		if err := w.Sleep(ctx, min(s.Interval, deadline.Sub(w.Now()))); err != nil {
			return nil, err
		}
	}
}

func (w *Waiter) since(ctx context.Context, since int) (int, error) {
	if since > 0 {
		return since, nil
	}
	return NewestID(ctx, w.T, w.Peer)
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
// (only the bot's when botOnly).
func (w *Waiter) snapshot(ctx context.Context, msg int, botOnly bool) (map[int]tgclient.BotMessage, error) {
	var msgs []tgclient.BotMessage
	var err error
	if msg > 0 {
		msgs, err = w.T.BotMessagesByID(ctx, w.Peer, []int{msg})
		if err == nil && len(msgs) == 0 {
			return nil, output.NotFound("message %d not found in this chat", msg)
		}
	} else {
		msgs, err = w.T.BotHistory(ctx, w.Peer, 0, recentWindow)
	}
	if err != nil {
		return nil, err
	}
	out := map[int]tgclient.BotMessage{}
	for _, m := range msgs {
		if botOnly && msg == 0 && m.From != "bot" {
			continue
		}
		out[m.ID] = m
	}
	return out, nil
}
