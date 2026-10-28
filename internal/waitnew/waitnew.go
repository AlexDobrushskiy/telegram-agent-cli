// Package waitnew finds incoming messages newer than a per-chat watermark.
// One Poll is one connection's worth of work; the caller loops, sleeps and
// releases the Telegram lock between polls.
package waitnew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"sort"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/watch"
)

const (
	// PerChat caps messages returned per chat per poll (the newest ones).
	PerChat = 100
	// unknownChatScan is how many recent messages are read from a chat the watermark has never seen.
	unknownChatScan = 20
	// slack absorbs out-of-order server dates when stopping the dialog scan early.
	slack        = 2 * time.Minute
	stateVersion = 1
)

// State is the watermark file.
type State struct {
	Version int           `json:"version"`
	Chats   map[int64]int `json:"chats"` // chat id → highest message id seen
	// Newest is the newest top-message date seen (server clock); dialogs older
	// than it (minus slack) cannot hold new messages, so the scan stops there.
	Newest  time.Time `json:"newest,omitzero"`
	Updated time.Time `json:"updated,omitzero"`
}

func (s *State) clone() *State {
	c := *s
	c.Chats = maps.Clone(s.Chats)
	if c.Chats == nil {
		c.Chats = map[int64]int{}
	}
	return &c
}

// Load reads the state file; ok is false when it does not exist.
func Load(path string) (st *State, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	st = &State{}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, false, fmt.Errorf("state file %s is not valid (delete it to start over): %w", path, err)
	}
	if st.Version != stateVersion {
		return nil, false, fmt.Errorf("state file %s has version %d, want %d (delete it to start over)", path, st.Version, stateVersion)
	}
	if st.Chats == nil {
		st.Chats = map[int64]int{}
	}
	return st, true, nil
}

// Save writes the state file atomically (0600).
func Save(path string, st *State, now time.Time) error {
	commit, err := Prepare(path, st, now)
	if err != nil {
		return err
	}
	return commit()
}

// Prepare writes the state to a temp file next to path; commit renames it into
// place. Callers print their output between the two, so a crash re-delivers
// messages rather than losing them.
func Prepare(path string, st *State, now time.Time) (commit func() error, err error) {
	st.Updated = now.UTC()
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return nil, err
	}
	tmp := path + ".next"
	if err := config.WriteFileAtomic(tmp, append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return func() error { return os.Rename(tmp, path) }, nil
}

// Filter selects chats and messages.
type Filter struct {
	Chats    map[int64]bool // empty: every chat
	DMs      bool           // 1:1 chats (people and bots)
	Groups   bool           // groups, supergroups, forums
	Mentions bool           // group messages only when they mention or reply to you
}

func isDM(t string) bool { return t == "user" || t == "bot" }

func (f Filter) chat(d tgclient.Dialog) bool {
	if len(f.Chats) > 0 && !f.Chats[d.ID] {
		return false
	}
	if !f.DMs && !f.Groups && !f.Mentions {
		return true
	}
	switch {
	case isDM(d.Type):
		return f.DMs
	case d.Peer.IsGroup():
		return f.Groups || f.Mentions
	}
	return false
}

// keep picks the incoming, non-service messages the filter wants.
func (f Filter) keep(ctx context.Context, t tgclient.Telegram, d tgclient.Dialog, msgs []tgclient.Message) ([]tgclient.Message, error) {
	var out []tgclient.Message
	for i := range msgs {
		m := &msgs[i]
		if m.FromMe || m.Service != "" {
			continue
		}
		if f.Mentions && d.Peer.IsGroup() {
			ok, err := watch.AddressedToMe(ctx, t, &d.Peer, msgs, m)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		out = append(out, *m)
	}
	return out, nil
}

// Result is one poll's (or the first run's) output.
type Result struct {
	Messages  []tgclient.Message
	Truncated []int64 // chats with more than PerChat new messages
}

func lastDate(d tgclient.Dialog) (time.Time, bool) {
	if d.Last == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, d.Last.Date)
	return t, err == nil
}

// Init builds the watermark from every dialog. With since set it also returns
// the wanted messages newer than since.
func Init(ctx context.Context, t tgclient.Telegram, f Filter, since time.Time) (*State, *Result, error) {
	st := &State{Version: stateVersion, Chats: map[int64]int{}}
	var fetch []tgclient.Dialog
	err := t.IterDialogs(ctx, tgclient.FolderAll, func(d tgclient.Dialog) bool {
		st.Chats[d.ID] = d.TopMessage
		at, ok := lastDate(d)
		if ok && at.After(st.Newest) {
			st.Newest = at
		}
		if !since.IsZero() && f.chat(d) && (!ok || !at.Before(since)) {
			fetch = append(fetch, d)
		}
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	res := &Result{}
	for _, d := range fetch {
		msgs, err := t.History(ctx, &d.Peer, tgclient.HistoryQuery{Limit: PerChat})
		if err != nil {
			return nil, nil, err
		}
		if len(msgs) == PerChat && !msgs[len(msgs)-1].Time.Before(since) {
			res.Truncated = append(res.Truncated, d.ID)
		}
		msgs = newerThan(msgs, since)
		kept, err := f.keep(ctx, t, d, msgs)
		if err != nil {
			return nil, nil, err
		}
		res.Messages = append(res.Messages, kept...)
		st.Chats[d.ID] = max(st.Chats[d.ID], maxID(msgs))
	}
	sortMessages(res.Messages)
	return st, res, nil
}

// Poll returns the wanted incoming messages past the watermark and the advanced state.
// Only chats whose top message moved are fetched; st is not modified.
func Poll(ctx context.Context, t tgclient.Telegram, st *State, f Filter) (*State, *Result, error) {
	next := st.clone()
	stopBefore := time.Time{}
	if !st.Newest.IsZero() {
		stopBefore = st.Newest.Add(-slack)
	}
	type change struct {
		d     tgclient.Dialog
		wm    int
		known bool
	}
	var changed []change
	err := t.IterDialogs(ctx, tgclient.FolderAll, func(d tgclient.Dialog) bool {
		at, ok := lastDate(d)
		// Unpinned dialogs come newest first: past this one nothing is new.
		if !d.Pinned && ok && !stopBefore.IsZero() && at.Before(stopBefore) {
			return false
		}
		if ok && at.After(next.Newest) {
			next.Newest = at
		}
		wm, known := st.Chats[d.ID]
		if known && d.TopMessage <= wm {
			return true
		}
		if !f.chat(d) {
			next.Chats[d.ID] = max(wm, d.TopMessage)
			return true
		}
		changed = append(changed, change{d, wm, known})
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	res := &Result{}
	for _, c := range changed {
		var msgs []tgclient.Message
		if c.known {
			msgs, err = t.History(ctx, &c.d.Peer, tgclient.HistoryQuery{MinID: c.wm, Limit: PerChat})
			if err == nil && len(msgs) == PerChat {
				res.Truncated = append(res.Truncated, c.d.ID)
			}
		} else {
			// A chat the watermark never saw (new since the first run): recent messages only.
			msgs, err = t.History(ctx, &c.d.Peer, tgclient.HistoryQuery{Limit: unknownChatScan})
			if err == nil && !stopBefore.IsZero() {
				msgs = newerThan(msgs, stopBefore)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		kept, err := f.keep(ctx, t, c.d, msgs)
		if err != nil {
			return nil, nil, err
		}
		res.Messages = append(res.Messages, kept...)
		next.Chats[c.d.ID] = max(c.wm, c.d.TopMessage, maxID(msgs))
	}
	sortMessages(res.Messages)
	return next, res, nil
}

func newerThan(msgs []tgclient.Message, since time.Time) []tgclient.Message {
	var out []tgclient.Message
	for _, m := range msgs {
		if !m.Time.Before(since) {
			out = append(out, m)
		}
	}
	return out
}

func maxID(msgs []tgclient.Message) int {
	n := 0
	for _, m := range msgs {
		n = max(n, m.ID)
	}
	return n
}

// sortMessages orders oldest first, then by chat and id.
func sortMessages(msgs []tgclient.Message) {
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i], msgs[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		if a.ChatID != b.ChatID {
			return a.ChatID < b.ChatID
		}
		return a.ID < b.ID
	})
}
