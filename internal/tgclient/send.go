package tgclient

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/tg"
)

func randomID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}

func replyTo(id int) tg.InputReplyToClass {
	if id <= 0 {
		return nil
	}
	return &tg.InputReplyToMessage{ReplyToMsgID: id}
}

func (s *Session) SendText(ctx context.Context, p *Peer, text string, reply int) (*Message, error) {
	rid, err := randomID()
	if err != nil {
		return nil, err
	}
	upd, err := s.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: p.InputPeer(), Message: text, RandomID: rid, NoWebpage: true, ReplyTo: replyTo(reply),
	})
	if err != nil {
		return nil, err
	}
	return s.sentMessage(upd, rid, p, text)
}

func (s *Session) SendFile(ctx context.Context, p *Peer, path, caption string, reply int) (*Message, error) {
	media, err := uploadMedia(ctx, s.api, path)
	if err != nil {
		return nil, err
	}
	rid, err := randomID()
	if err != nil {
		return nil, err
	}
	upd, err := s.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: p.InputPeer(), Media: media, Message: caption, RandomID: rid, ReplyTo: replyTo(reply),
	})
	if err != nil {
		return nil, err
	}
	return s.sentMessage(upd, rid, p, caption)
}

// sentMessage extracts the message we just sent from the updates Telegram returns.
func (s *Session) sentMessage(upd tg.UpdatesClass, rid int64, p *Peer, text string) (*Message, error) {
	var updates []tg.UpdateClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch v := upd.(type) {
	case *tg.UpdateShortSentMessage:
		m := &tg.Message{ID: v.ID, Date: v.Date, Out: true, Message: text, Media: v.Media, PeerID: peerOf(p)}
		if s.self != nil {
			m.FromID = &tg.PeerUser{UserID: s.self.ID}
		}
		ents := newEntities(s.selfUsers(), nil)
		ents.peers[p.ID] = *p
		out, _ := ents.toMessage(m, s.selfID())
		return &out, nil
	case *tg.Updates:
		updates, users, chats = v.Updates, v.Users, v.Chats
	case *tg.UpdatesCombined:
		updates, users, chats = v.Updates, v.Users, v.Chats
	default:
		return nil, fmt.Errorf("unexpected send result %T", upd)
	}
	ents := s.absorb(append(users, s.selfUsers()...), chats)
	if _, ok := ents.peers[p.ID]; !ok {
		ents.peers[p.ID] = *p
	}
	id := 0
	for _, u := range updates {
		if m, ok := u.(*tg.UpdateMessageID); ok && m.RandomID == rid {
			id = m.ID
		}
	}
	for _, u := range updates {
		var mc tg.MessageClass
		switch v := u.(type) {
		case *tg.UpdateNewMessage:
			mc = v.Message
		case *tg.UpdateNewChannelMessage:
			mc = v.Message
		default:
			continue
		}
		if out, ok := ents.toMessage(mc, s.selfID()); ok && out.FromMe && (id == 0 || out.ID == id) && out.ChatID == p.ID {
			return &out, nil
		}
	}
	if id == 0 {
		return nil, fmt.Errorf("sent, but Telegram returned no message id")
	}
	name := p.Name
	return &Message{ID: id, ChatID: p.ID, ChatName: &name, FromMe: true, Text: text}, nil
}

func (s *Session) selfUsers() []tg.UserClass {
	if s.self == nil {
		return nil
	}
	return []tg.UserClass{s.self}
}

func peerOf(p *Peer) tg.PeerClass {
	switch p.Kind {
	case KindChat:
		return &tg.PeerChat{ChatID: p.RawID}
	case KindChannel:
		return &tg.PeerChannel{ChannelID: p.RawID}
	}
	return &tg.PeerUser{UserID: p.RawID}
}
