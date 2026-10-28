package tgclient

import (
	"context"
	"sort"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// Session implements Telegram over a connected gotd client.
type Session struct {
	api   *tg.Client
	cache *PeerCache
	self  *tg.User
	now   func() time.Time
}

var _ Telegram = (*Session)(nil)

// NewSession wraps an authorised client. self is the logged-in user.
func NewSession(api *tg.Client, cache *PeerCache, self *tg.User) *Session {
	s := &Session{api: api, cache: cache, self: self, now: time.Now}
	if self != nil {
		s.cache.Put(userPeer(self), self.Phone, false)
	}
	return s
}

func (s *Session) selfID() int64 {
	if s.self == nil {
		return 0
	}
	return s.self.ID
}

// absorb indexes a response's entities and feeds the peer cache.
func (s *Session) absorb(users []tg.UserClass, chats []tg.ChatClass) *entities {
	e := newEntities(users, chats)
	for id, p := range e.peers {
		phone := ""
		if u, ok := e.users[id]; ok {
			phone = u.Phone
		}
		s.cache.Put(p, phone, isMin(e.users, chats, id))
	}
	return e
}

func (s *Session) Self(context.Context) (*User, error) {
	if s.self == nil {
		return nil, output.Auth("not logged in")
	}
	u := UserJSON(s.self)
	return &u, nil
}

type dialogKey struct {
	chat int64
	msg  int
}

func (s *Session) IterDialogs(ctx context.Context, folder Folder, fn func(Dialog) bool) error {
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: chunkSize}
	switch folder {
	case FolderMain:
		req.SetFolderID(0)
	case FolderArchive:
		req.SetFolderID(1)
	}
	seen := map[int64]bool{}
	for {
		r, err := s.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return err
		}
		var dialogs []tg.DialogClass
		var msgs []tg.MessageClass
		var users []tg.UserClass
		var chats []tg.ChatClass
		more := false
		switch v := r.(type) {
		case *tg.MessagesDialogs:
			dialogs, msgs, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
		case *tg.MessagesDialogsSlice:
			dialogs, msgs, users, chats, more = v.Dialogs, v.Messages, v.Users, v.Chats, true
		default:
			return nil
		}
		ents := s.absorb(users, chats)
		byKey := map[dialogKey]tg.MessageClass{}
		for _, m := range msgs {
			if nm, ok := m.AsNotEmpty(); ok {
				byKey[dialogKey{PeerMarkedID(nm.GetPeerID()), nm.GetID()}] = m
			}
		}
		var lastPeer tg.InputPeerClass
		var lastMsg tg.MessageClass
		newOnes := 0
		for _, dc := range dialogs {
			d, ok := dc.(*tg.Dialog)
			if !ok {
				continue // archive folder entry
			}
			id := PeerMarkedID(d.Peer)
			p, known := ents.peers[id]
			if !known {
				continue
			}
			lastPeer = p.InputPeer()
			if m, ok := byKey[dialogKey{id, d.TopMessage}]; ok {
				lastMsg = m
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			newOnes++
			if !fn(s.dialog(d, p, ents, byKey[dialogKey{id, d.TopMessage}])) {
				return nil
			}
		}
		if !more || len(dialogs) < req.Limit || newOnes == 0 || lastPeer == nil {
			return nil
		}
		req.ExcludePinned = true
		req.OffsetPeer = lastPeer
		req.OffsetID, req.OffsetDate = msgIDDate(lastMsg)
	}
}

func (s *Session) dialog(d *tg.Dialog, p Peer, ents *entities, top tg.MessageClass) Dialog {
	out := Dialog{
		ID: p.ID, Name: p.Name, Type: p.Type, Username: strPtr(p.Username),
		Unread: d.UnreadCount, Mentions: d.UnreadMentionsCount, Pinned: d.Pinned,
		Archived: d.FolderID == 1,
		Muted:    d.NotifySettings.MuteUntil > int(s.now().Unix()),
		Peer:     p, TopMessage: d.TopMessage, ReadInboxMaxID: d.ReadInboxMaxID,
	}
	if top != nil {
		if m, ok := ents.toMessage(top, s.selfID()); ok {
			last := &LastMessage{ID: m.ID, Date: m.Date, FromMe: m.FromMe, Text: truncateRunes(m.Text, 200)}
			if m.Media != nil {
				last.Media = &m.Media.Type
			}
			out.Last = last
		}
	}
	return out
}

func (s *Session) PeerInfo(ctx context.Context, p *Peer) (*ChatInfo, error) {
	out := &ChatInfo{ID: p.ID, Name: p.Name, Type: p.Type, Username: strPtr(p.Username)}
	switch p.Kind {
	case KindUser:
		full, err := s.api.UsersGetFullUser(ctx, &tg.InputUser{UserID: p.RawID, AccessHash: p.AccessHash})
		if err != nil {
			return nil, err
		}
		ents := s.absorb(full.Users, full.Chats)
		about := full.FullUser.About
		out.About = &about
		if u, ok := ents.users[p.RawID]; ok {
			out.Phone = strPtr(u.Phone)
			c, mc := u.Contact, u.MutualContact
			out.Contact, out.MutualContact = &c, &mc
		}
	case KindChannel:
		full, err := s.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: p.RawID, AccessHash: p.AccessHash})
		if err != nil {
			return nil, err
		}
		s.absorb(full.Users, full.Chats)
		if cf, ok := full.FullChat.(*tg.ChannelFull); ok {
			about, n := cf.About, cf.ParticipantsCount
			out.About, out.Members = &about, &n
		}
	default:
		full, err := s.api.MessagesGetFullChat(ctx, p.RawID)
		if err != nil {
			return nil, err
		}
		s.absorb(full.Users, full.Chats)
		if cf, ok := full.FullChat.(*tg.ChatFull); ok {
			about := cf.About
			n := 0
			if parts, ok := cf.Participants.(*tg.ChatParticipants); ok {
				n = len(parts.Participants)
			}
			out.About, out.Members = &about, &n
		}
	}
	return out, nil
}

func (s *Session) iter(kind iterKind, p *Peer, fetch func(context.Context, page) (pageResult, error)) iterSpec {
	return iterSpec{
		kind: kind, hasEntity: p != nil, fetch: fetch, selfID: s.selfID(),
		onPage: func(r pageResult) *entities { return s.absorb(r.users, r.chats) },
		inputPeer: func(chatID int64) tg.InputPeerClass {
			if c, ok := s.cache.Get(chatID); ok {
				return c.InputPeer()
			}
			return nil
		},
	}
}

func dateUnix(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	return int(t.Unix())
}

func (s *Session) History(ctx context.Context, p *Peer, q HistoryQuery) ([]Message, error) {
	in := p.InputPeer()
	fetch := func(ctx context.Context, pg page) (pageResult, error) {
		r, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: in, OffsetID: pg.offsetID, OffsetDate: pg.offsetDate, AddOffset: pg.addOffset, Limit: pg.limit,
		})
		if err != nil {
			return pageResult{}, err
		}
		return unpackMessages(r), nil
	}
	if q.Topic > 0 {
		fetch = func(ctx context.Context, pg page) (pageResult, error) {
			r, err := s.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
				Peer: in, MsgID: q.Topic, OffsetID: pg.offsetID, OffsetDate: pg.offsetDate, AddOffset: pg.addOffset, Limit: pg.limit,
			})
			if err != nil {
				return pageResult{}, err
			}
			return unpackMessages(r), nil
		}
	}
	it := s.iter(iterHistory, p, fetch)
	it.limit, it.reverse = q.Limit, q.Reverse
	it.offsetID, it.offsetDt, it.minID, it.maxID = q.OffsetID, dateUnix(q.OffsetDate), q.MinID, q.MaxID
	return it.run(ctx)
}

func (s *Session) Search(ctx context.Context, p *Peer, q SearchQuery) ([]Message, error) {
	return s.search(ctx, p, q, &tg.InputMessagesFilterEmpty{})
}

func (s *Session) search(ctx context.Context, p *Peer, q SearchQuery, filter tg.MessagesFilterClass) ([]Message, error) {
	minDate := dateUnix(q.After)
	if p == nil {
		if q.From != nil {
			return nil, output.Usage("--from needs --chat")
		}
		it := s.iter(iterGlobal, nil, func(ctx context.Context, pg page) (pageResult, error) {
			r, err := s.api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
				Q: q.Query, Filter: filter, MinDate: minDate, MaxDate: pg.maxDate,
				OffsetRate: pg.offsetRate, OffsetPeer: pg.offsetPeer, OffsetID: pg.offsetID, Limit: pg.limit,
			})
			if err != nil {
				return pageResult{}, err
			}
			return unpackMessages(r), nil
		})
		it.limit, it.offsetDt = q.Limit, dateUnix(q.Before)
		msgs, err := it.run(ctx)
		return cutAfter(msgs, q.After), err
	}
	in := p.InputPeer()
	req := tg.MessagesSearchRequest{Peer: in, Q: q.Query, Filter: filter, MinDate: minDate}
	var fromID int64
	if q.From != nil {
		// Telethon: in a 1:1 chat filter the sender locally, elsewhere ask the server.
		if p.Kind == KindUser {
			fromID = q.From.ID
		} else {
			req.SetFromID(q.From.InputPeer())
		}
	}
	it := s.iter(iterSearch, p, func(ctx context.Context, pg page) (pageResult, error) {
		r := req
		r.MaxDate, r.OffsetID, r.AddOffset, r.Limit = pg.maxDate, pg.offsetID, pg.addOffset, pg.limit
		res, err := s.api.MessagesSearch(ctx, &r)
		if err != nil {
			return pageResult{}, err
		}
		return unpackMessages(res), nil
	})
	it.limit, it.offsetDt, it.fromID = q.Limit, dateUnix(q.Before), fromID
	msgs, err := it.run(ctx)
	return cutAfter(msgs, q.After), err
}

// cutAfter drops messages older than after (the prototype stops there too).
func cutAfter(msgs []Message, after time.Time) []Message {
	if after.IsZero() {
		return msgs
	}
	for i, m := range msgs {
		if m.Time.Before(after) {
			return msgs[:i]
		}
	}
	return msgs
}

func (s *Session) MessagesByID(ctx context.Context, p *Peer, ids []int) ([]Message, error) {
	input := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		input[i] = &tg.InputMessageID{ID: id}
	}
	var r tg.MessagesMessagesClass
	var err error
	if p.Kind == KindChannel {
		r, err = s.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.RawID, AccessHash: p.AccessHash}, ID: input,
		})
	} else {
		r, err = s.api.MessagesGetMessages(ctx, input)
	}
	if err != nil {
		return nil, err
	}
	pr := unpackMessages(r)
	ents := s.absorb(pr.users, pr.chats)
	var out []Message
	for _, mc := range pr.messages {
		if m, ok := ents.toMessage(mc, s.selfID()); ok {
			if p.Kind != KindChannel && m.ChatID != p.ID {
				continue // messages.getMessages is account-wide for users and basic groups
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// DefaultMentionScan is how many recent groups an account-wide mention search visits.
const DefaultMentionScan = 50

// Mentions never marks anything read. Without a chat it visits dialogs one by one:
// messages.searchGlobal rejects the my-mentions filter (INPUT_FILTER_INVALID).
func (s *Session) Mentions(ctx context.Context, p *Peer, q MentionsQuery) ([]Message, error) {
	if p != nil {
		if q.Unread {
			return s.unreadMentions(ctx, p, q.Limit)
		}
		return s.search(ctx, p, SearchQuery{Limit: q.Limit}, &tg.InputMessagesFilterMyMentions{})
	}
	scan := q.Scan
	if scan <= 0 {
		scan = DefaultMentionScan
	}
	// Unread: every dialog with unread mentions. Otherwise: the most recent groups.
	var peers []Peer
	err := s.IterDialogs(ctx, FolderAll, func(d Dialog) bool {
		switch {
		case q.Unread && d.Mentions > 0:
			peers = append(peers, d.Peer)
		case !q.Unread && d.Peer.IsGroup():
			peers = append(peers, d.Peer)
		}
		return q.Unread || len(peers) < scan
	})
	if err != nil {
		return nil, err
	}
	var out []Message
	for i := range peers {
		var msgs []Message
		if q.Unread {
			msgs, err = s.unreadMentions(ctx, &peers[i], q.Limit)
		} else {
			msgs, err = s.search(ctx, &peers[i], SearchQuery{Limit: q.Limit}, &tg.InputMessagesFilterMyMentions{})
		}
		if tgerr.IsCode(err, 400, 403) {
			continue // chat became unreadable (left, private, banned): skip it
		}
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// unreadMentions pages messages.getUnreadMentions; it does not mark anything read.
func (s *Session) unreadMentions(ctx context.Context, p *Peer, limit int) ([]Message, error) {
	in := p.InputPeer()
	var out []Message
	offsetID := 0
	for len(out) < limit {
		n := min(limit-len(out), chunkSize)
		r, err := s.api.MessagesGetUnreadMentions(ctx, &tg.MessagesGetUnreadMentionsRequest{
			Peer: in, OffsetID: offsetID, Limit: n,
		})
		if err != nil {
			return out, err
		}
		pr := unpackMessages(r)
		ents := s.absorb(pr.users, pr.chats)
		got := 0
		for _, mc := range pr.messages {
			if m, ok := ents.toMessage(mc, s.selfID()); ok {
				out = append(out, m)
				offsetID = m.ID
				got++
			}
		}
		if got < n || !pr.more {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Session) SearchContacts(ctx context.Context, query string, limit int) (*ContactsResult, error) {
	r, err := s.api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: query, Limit: limit})
	if err != nil {
		return nil, err
	}
	ents := s.absorb(r.Users, r.Chats)
	pick := func(peers []tg.PeerClass) []User {
		out := []User{}
		for _, pc := range peers {
			id := PeerMarkedID(pc)
			if u, ok := ents.users[id]; ok && id > 0 {
				out = append(out, UserJSON(u))
			} else if p, ok := ents.peers[id]; ok {
				out = append(out, PeerUserJSON(p))
			}
		}
		return out
	}
	return &ContactsResult{Mine: pick(r.MyResults), Public: pick(r.Results)}, nil
}

// isRPC reports whether err is a Telegram RPC error of one of the given types.
func isRPC(err error, types ...string) bool { return tgerr.Is(err, types...) }
