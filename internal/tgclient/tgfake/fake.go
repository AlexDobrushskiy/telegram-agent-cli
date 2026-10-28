// Package tgfake is an in-memory tgclient.Telegram for tests: no network.
package tgfake

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// Sent records one send.
type Sent struct {
	ChatID  int64
	Text    string
	Path    string
	ReplyTo int
}

// Fake holds dialogs and per-chat messages. Messages are kept in any order.
type Fake struct {
	mu       sync.Mutex
	Me       tgclient.User
	Dialogs  []tgclient.Dialog
	Messages map[int64][]tgclient.Message
	Sent     []Sent
	// Calls counts method calls by name.
	Calls map[string]int
	// Now is the clock for sent messages (default time.Now).
	Now func() time.Time
}

var _ tgclient.Telegram = (*Fake)(nil)

// New returns an empty fake logged in as me.
func New(me tgclient.User) *Fake {
	return &Fake{Me: me, Messages: map[int64][]tgclient.Message{}, Calls: map[string]int{}}
}

// AddDialog adds a chat; peer name/type/username come from d.
func (f *Fake) AddDialog(d tgclient.Dialog) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind, raw := tgclient.SplitMarked(d.ID)
	d.Peer = tgclient.Peer{ID: d.ID, Name: d.Name, Type: d.Type, Kind: kind, RawID: raw}
	if d.Username != nil {
		d.Peer.Username = *d.Username
	}
	f.Dialogs = append(f.Dialogs, d)
}

// AddMessage stores m (ChatID must be set). Time defaults from Date.
func (f *Fake) AddMessage(m tgclient.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.Time.IsZero() && m.Date != "" {
		m.Time, _ = time.Parse(time.RFC3339, m.Date)
	}
	if m.Date == "" && !m.Time.IsZero() {
		m.Date = tgclient.ISO(m.Time)
	}
	f.Messages[m.ChatID] = append(f.Messages[m.ChatID], m)
}

func (f *Fake) call(name string) {
	f.mu.Lock()
	f.Calls[name]++
	f.mu.Unlock()
}

func (f *Fake) Self(context.Context) (*tgclient.User, error) {
	f.call("Self")
	u := f.Me
	return &u, nil
}

func (f *Fake) IterDialogs(_ context.Context, folder tgclient.Folder, fn func(tgclient.Dialog) bool) error {
	f.call("IterDialogs")
	for _, d := range f.Dialogs {
		if (folder == tgclient.FolderMain && d.Archived) || (folder == tgclient.FolderArchive && !d.Archived) {
			continue
		}
		if !fn(d) {
			return nil
		}
	}
	return nil
}

func (f *Fake) Resolve(_ context.Context, ref string) (*tgclient.Peer, error) {
	f.call("Resolve")
	r, err := tgclient.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	for _, d := range f.Dialogs {
		p := d.Peer
		switch {
		case r.Kind == tgclient.RefID && d.ID == r.ID,
			r.Kind == tgclient.RefUsername && strings.EqualFold(p.Username, r.Value):
			return &p, nil
		}
	}
	if r.Kind == tgclient.RefName {
		return tgclient.PickByName(r.Value, f.Dialogs)
	}
	return nil, output.NotFound("cannot resolve %s", ref)
}

func (f *Fake) PeerInfo(_ context.Context, p *tgclient.Peer) (*tgclient.ChatInfo, error) {
	f.call("PeerInfo")
	about := ""
	return &tgclient.ChatInfo{ID: p.ID, Name: p.Name, Type: p.Type, About: &about}, nil
}

func (f *Fake) sorted(chat int64) []tgclient.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := append([]tgclient.Message(nil), f.Messages[chat]...)
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	return msgs
}

// History applies HistoryQuery semantics directly (newest first unless Reverse).
func (f *Fake) History(_ context.Context, p *tgclient.Peer, q tgclient.HistoryQuery) ([]tgclient.Message, error) {
	f.call("History")
	var out []tgclient.Message
	msgs := f.sorted(p.ID)
	keep := func(m tgclient.Message) bool {
		if (q.MinID > 0 && m.ID <= q.MinID) || (q.MaxID > 0 && m.ID >= q.MaxID) {
			return false
		}
		if q.Reverse {
			return (q.OffsetID == 0 || m.ID > q.OffsetID) && (q.OffsetDate.IsZero() || m.Time.After(q.OffsetDate))
		}
		return (q.OffsetID == 0 || m.ID < q.OffsetID) && (q.OffsetDate.IsZero() || m.Time.Before(q.OffsetDate))
	}
	if q.Reverse {
		for _, m := range msgs {
			if keep(m) && len(out) < q.Limit {
				out = append(out, m)
			}
		}
		return out, nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if keep(msgs[i]) && len(out) < q.Limit {
			out = append(out, msgs[i])
		}
	}
	return out, nil
}

func (f *Fake) chats(p *tgclient.Peer) []int64 {
	if p != nil {
		return []int64{p.ID}
	}
	var ids []int64
	for _, d := range f.Dialogs {
		ids = append(ids, d.ID)
	}
	return ids
}

func (f *Fake) newestFirst(p *tgclient.Peer, match func(tgclient.Message) bool, limit int) []tgclient.Message {
	var out []tgclient.Message
	for _, id := range f.chats(p) {
		for _, m := range f.sorted(id) {
			if match(m) {
				out = append(out, m)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (f *Fake) Search(_ context.Context, p *tgclient.Peer, q tgclient.SearchQuery) ([]tgclient.Message, error) {
	f.call("Search")
	needle := strings.ToLower(q.Query)
	return f.newestFirst(p, func(m tgclient.Message) bool {
		if !strings.Contains(strings.ToLower(m.Text), needle) {
			return false
		}
		if q.From != nil && (m.SenderID == nil || *m.SenderID != q.From.ID) {
			return false
		}
		return (q.After.IsZero() || !m.Time.Before(q.After)) && (q.Before.IsZero() || m.Time.Before(q.Before))
	}, q.Limit), nil
}

func (f *Fake) MessagesByID(_ context.Context, p *tgclient.Peer, ids []int) ([]tgclient.Message, error) {
	f.call("MessagesByID")
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []tgclient.Message
	for _, m := range f.sorted(p.ID) {
		if want[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *Fake) Mentions(_ context.Context, p *tgclient.Peer, q tgclient.MentionsQuery) ([]tgclient.Message, error) {
	f.call("Mentions")
	scanned := map[int64]bool{}
	if p == nil {
		n := q.Scan
		if n <= 0 {
			n = tgclient.DefaultMentionScan
		}
		for _, d := range f.Dialogs {
			if (q.Unread && d.Mentions > 0) || (!q.Unread && d.Peer.IsGroup() && n > 0) {
				scanned[d.ID] = true
				if !q.Unread {
					n--
				}
			}
		}
	}
	return f.newestFirst(p, func(m tgclient.Message) bool {
		return m.Mentioned && (p != nil || scanned[m.ChatID])
	}, q.Limit), nil
}

func (f *Fake) SearchContacts(_ context.Context, query string, limit int) (*tgclient.ContactsResult, error) {
	f.call("SearchContacts")
	res := &tgclient.ContactsResult{Mine: []tgclient.User{}, Public: []tgclient.User{}}
	for _, d := range f.Dialogs {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(query)) && len(res.Mine) < limit {
			res.Mine = append(res.Mine, tgclient.PeerUserJSON(d.Peer))
		}
	}
	return res, nil
}

func (f *Fake) DownloadMedia(_ context.Context, p *tgclient.Peer, msgID int, dir string) (*tgclient.Download, error) {
	f.call("DownloadMedia")
	for _, m := range f.sorted(p.ID) {
		if m.ID != msgID {
			continue
		}
		if m.Media == nil {
			return nil, output.NotFound("message %d has no media", msgID)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, tgclient.MediaFilename(m.Media, m.Time))
		if err := os.WriteFile(path, []byte("fake media"), 0o600); err != nil {
			return nil, err
		}
		return &tgclient.Download{Path: path, Media: m.Media}, nil
	}
	return nil, output.NotFound("message %d not found", msgID)
}

func (f *Fake) send(p *tgclient.Peer, s Sent) *tgclient.Message {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	id := 1
	for _, m := range f.sorted(p.ID) {
		id = max(id, m.ID+1)
	}
	name, me := p.Name, f.Me.ID
	m := tgclient.Message{ID: id, ChatID: p.ID, ChatName: &name, FromMe: true, SenderID: &me, Text: s.Text,
		ReplyTo: s.ReplyTo, Time: now().UTC(), Date: tgclient.ISO(now())}
	f.AddMessage(m)
	f.mu.Lock()
	f.Sent = append(f.Sent, s)
	f.mu.Unlock()
	return &m
}

func (f *Fake) SendText(_ context.Context, p *tgclient.Peer, text string, replyTo int) (*tgclient.Message, error) {
	f.call("SendText")
	return f.send(p, Sent{ChatID: p.ID, Text: text, ReplyTo: replyTo}), nil
}

func (f *Fake) SendFile(_ context.Context, p *tgclient.Peer, path, caption string, replyTo int) (*tgclient.Message, error) {
	f.call("SendFile")
	return f.send(p, Sent{ChatID: p.ID, Text: caption, Path: path, ReplyTo: replyTo}), nil
}
