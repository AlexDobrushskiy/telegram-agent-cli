package tgclient

import (
	"context"

	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// Resolve finds a peer: marked id (cache, then dialogs), @username / t.me link,
// +phone (cache, contacts, then contacts.resolvePhone), or dialog name.
func (s *Session) Resolve(ctx context.Context, ref string) (*Peer, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case RefID:
		return s.resolveID(ctx, r.ID)
	case RefUsername:
		res, err := s.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: r.Value})
		if isRPC(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID") {
			return nil, output.NotFound("cannot resolve @%s", r.Value)
		}
		if err != nil {
			return nil, err
		}
		return s.peerFrom(res.Peer, res.Users, res.Chats)
	case RefInvite:
		return s.resolveInvite(ctx, r.Value)
	case RefPhone:
		return s.resolvePhone(ctx, r.Value)
	}
	var dialogs []Dialog
	if err := s.IterDialogs(ctx, FolderAll, func(d Dialog) bool {
		dialogs = append(dialogs, d)
		return true
	}); err != nil {
		return nil, err
	}
	return PickByName(r.Value, dialogs)
}

func (s *Session) peerFrom(pc tg.PeerClass, users []tg.UserClass, chats []tg.ChatClass) (*Peer, error) {
	ents := s.absorb(users, chats)
	id := PeerMarkedID(pc)
	p, ok := ents.peers[id]
	if !ok {
		return nil, output.NotFound("peer %d missing from response", id)
	}
	return &p, nil
}

func (s *Session) resolveID(ctx context.Context, id int64) (*Peer, error) {
	if p, ok := s.cache.Get(id); ok {
		if p.Type != "" {
			return &p, nil
		}
		if fresh, err := s.refresh(ctx, p); err == nil {
			return fresh, nil
		}
	}
	// Walk the dialogs (fills the cache). Like the prototype, a positive id also
	// matches a group/channel by its raw (unmarked) id.
	var exact, raw *Peer
	err := s.IterDialogs(ctx, FolderAll, func(d Dialog) bool {
		p := d.Peer
		if d.ID == id {
			exact = &p
			return false
		}
		if id > 0 && raw == nil && p.RawID == id {
			raw = &p
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if exact != nil {
		return exact, nil
	}
	if raw != nil {
		return raw, nil
	}
	return nil, output.NotFound("no chat with id %d", id)
}

// refresh refetches a cached peer (fills name/type for peers imported without them).
func (s *Session) refresh(ctx context.Context, p Peer) (*Peer, error) {
	switch p.Kind {
	case KindUser:
		users, err := s.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: p.RawID, AccessHash: p.AccessHash}})
		if err != nil {
			return nil, err
		}
		return s.peerFrom(&tg.PeerUser{UserID: p.RawID}, users, nil)
	case KindChannel:
		r, err := s.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: p.RawID, AccessHash: p.AccessHash}})
		if err != nil {
			return nil, err
		}
		return s.peerFrom(&tg.PeerChannel{ChannelID: p.RawID}, nil, r.GetChats())
	}
	r, err := s.api.MessagesGetChats(ctx, []int64{p.RawID})
	if err != nil {
		return nil, err
	}
	return s.peerFrom(&tg.PeerChat{ChatID: p.RawID}, nil, r.GetChats())
}

func (s *Session) resolveInvite(ctx context.Context, hash string) (*Peer, error) {
	r, err := s.api.MessagesCheckChatInvite(ctx, hash)
	if isRPC(err, "INVITE_HASH_EXPIRED", "INVITE_HASH_INVALID", "INVITE_HASH_EMPTY") {
		return nil, output.NotFound("invite link is invalid or expired")
	}
	if err != nil {
		return nil, err
	}
	var chat tg.ChatClass
	switch v := r.(type) {
	case *tg.ChatInviteAlready:
		chat = v.Chat
	case *tg.ChatInvitePeek:
		chat = v.Chat
	default:
		return nil, output.NotFound("not a member of the chat behind this invite link")
	}
	p, ok := chatPeer(chat)
	if !ok {
		return nil, output.NotFound("unexpected chat in invite")
	}
	s.absorb(nil, []tg.ChatClass{chat})
	return &p, nil
}

func (s *Session) resolvePhone(ctx context.Context, digits string) (*Peer, error) {
	if p, ok := s.cache.FindPhone(digits); ok && p.Type != "" {
		return &p, nil
	}
	if r, err := s.api.ContactsGetContacts(ctx, 0); err == nil {
		if c, ok := r.(*tg.ContactsContacts); ok {
			ents := s.absorb(c.Users, nil)
			for id, u := range ents.users {
				if u.Phone == digits {
					p := ents.peers[id]
					return &p, nil
				}
			}
		}
	} else {
		return nil, err
	}
	res, err := s.api.ContactsResolvePhone(ctx, digits)
	if isRPC(err, "PHONE_NOT_OCCUPIED", "PHONE_NUMBER_INVALID") {
		return nil, output.NotFound("no Telegram user with phone +%s visible to this account", digits)
	}
	if err != nil {
		return nil, err
	}
	return s.peerFrom(res.Peer, res.Users, res.Chats)
}
