package tgclient

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

// entities indexes the users/chats that came with one response.
type entities struct {
	peers map[int64]Peer
	users map[int64]*tg.User
}

func newEntities(users []tg.UserClass, chats []tg.ChatClass) *entities {
	e := &entities{peers: map[int64]Peer{}, users: map[int64]*tg.User{}}
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok {
			e.users[u.ID] = u
			e.peers[u.ID] = userPeer(u)
		}
	}
	for _, cc := range chats {
		if p, ok := chatPeer(cc); ok {
			e.peers[p.ID] = p
		}
	}
	return e
}

func (e *entities) name(id int64) *string {
	if p, ok := e.peers[id]; ok {
		n := p.Name
		return &n
	}
	return nil
}

// DisplayName follows Telethon's get_display_name.
func DisplayName(first, last string) string {
	switch {
	case first != "" && last != "":
		return first + " " + last
	case first != "":
		return first
	}
	return last
}

func userType(u *tg.User) string {
	if u.Bot {
		return "bot"
	}
	return "user"
}

func userUsername(u *tg.User) string {
	if u.Username != "" {
		return u.Username
	}
	for _, n := range u.Usernames {
		if n.Active {
			return n.Username
		}
	}
	return ""
}

func channelUsername(c *tg.Channel) string {
	if c.Username != "" {
		return c.Username
	}
	for _, n := range c.Usernames {
		if n.Active {
			return n.Username
		}
	}
	return ""
}

func channelType(c *tg.Channel) string {
	switch {
	case c.Forum:
		return "forum"
	case c.Megagroup:
		return "supergroup"
	}
	return "channel"
}

func userPeer(u *tg.User) Peer {
	return Peer{
		ID: u.ID, Kind: KindUser, RawID: u.ID, AccessHash: u.AccessHash,
		Name: DisplayName(u.FirstName, u.LastName), Type: userType(u), Username: userUsername(u),
	}
}

func chatPeer(cc tg.ChatClass) (Peer, bool) {
	switch c := cc.(type) {
	case *tg.Chat:
		return Peer{ID: MarkedID(KindChat, c.ID), Kind: KindChat, RawID: c.ID, Name: c.Title, Type: "group"}, true
	case *tg.ChatForbidden:
		return Peer{ID: MarkedID(KindChat, c.ID), Kind: KindChat, RawID: c.ID, Name: c.Title, Type: "group"}, true
	case *tg.Channel:
		return Peer{
			ID: MarkedID(KindChannel, c.ID), Kind: KindChannel, RawID: c.ID, AccessHash: c.AccessHash,
			Name: c.Title, Type: channelType(c), Username: channelUsername(c),
		}, true
	case *tg.ChannelForbidden:
		t := "channel"
		if c.Megagroup {
			t = "supergroup"
		}
		return Peer{
			ID: MarkedID(KindChannel, c.ID), Kind: KindChannel, RawID: c.ID, AccessHash: c.AccessHash,
			Name: c.Title, Type: t,
		}, true
	}
	return Peer{}, false
}

// isMin reports whether a user/channel object carries a "min" (unusable) access hash.
func isMin(users map[int64]*tg.User, chats []tg.ChatClass, id int64) bool {
	if u, ok := users[id]; ok {
		return u.Min
	}
	for _, cc := range chats {
		if c, ok := cc.(*tg.Channel); ok && MarkedID(KindChannel, c.ID) == id {
			return c.Min
		}
	}
	return false
}

// UserJSON is the prototype's user_json.
func UserJSON(u *tg.User) User {
	return User{
		ID: u.ID, Name: DisplayName(u.FirstName, u.LastName), Type: userType(u),
		Username: strPtr(userUsername(u)), Phone: strPtr(u.Phone),
		Contact: u.Contact, MutualContact: u.MutualContact,
	}
}

// PeerUserJSON is user_json for a non-user peer (contact search results).
func PeerUserJSON(p Peer) User {
	return User{ID: p.ID, Name: p.Name, Type: p.Type, Username: strPtr(p.Username)}
}

func unix(t int) time.Time { return time.Unix(int64(t), 0).UTC() }

// toMessage converts a message; ok is false for empty messages.
func (e *entities) toMessage(mc tg.MessageClass, selfID int64) (Message, bool) {
	switch m := mc.(type) {
	case *tg.Message:
		chatID := PeerMarkedID(m.PeerID)
		out := Message{
			ID: m.ID, ChatID: chatID, ChatName: e.name(chatID), Time: unix(m.Date), Date: ISO(unix(m.Date)),
			FromMe: m.Out, Text: m.Message, Mentioned: m.Mentioned,
		}
		e.setSender(&out, m.FromID, m.PeerID, m.Out, selfID)
		if m.EditDate > 0 {
			out.Edited = ISO(unix(m.EditDate))
		}
		if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID > 0 {
			out.ReplyTo = h.ReplyToMsgID
		}
		if f, ok := m.GetFwdFrom(); ok {
			switch {
			case f.FromName != "":
				out.ForwardedFrom = f.FromName
			case f.FromID != nil:
				out.ForwardedFrom = PeerMarkedID(f.FromID)
			}
		}
		out.Media = MediaInfo(m.Media)
		out.Album = m.GroupedID
		if v, ok := m.GetViews(); ok {
			out.Views = &v
		}
		return out, true
	case *tg.MessageService:
		chatID := PeerMarkedID(m.PeerID)
		out := Message{
			ID: m.ID, ChatID: chatID, ChatName: e.name(chatID), Time: unix(m.Date), Date: ISO(unix(m.Date)),
			FromMe: m.Out, Mentioned: m.Mentioned, Service: actionName(m.Action),
		}
		e.setSender(&out, m.FromID, m.PeerID, m.Out, selfID)
		if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID > 0 {
			out.ReplyTo = h.ReplyToMsgID
		}
		return out, true
	}
	return Message{}, false
}

// setSender follows Telethon: from_id, else the peer for incoming 1:1 and channel posts.
func (e *entities) setSender(out *Message, from, peer tg.PeerClass, isOut bool, selfID int64) {
	var id int64
	switch {
	case from != nil:
		id = PeerMarkedID(from)
	case isOut && selfID != 0:
		id = selfID
	default:
		if _, isChat := peer.(*tg.PeerChat); !isChat {
			id = PeerMarkedID(peer)
		}
	}
	if id == 0 {
		return
	}
	out.SenderID = &id
	out.SenderName = e.name(id)
}

func actionName(a tg.MessageActionClass) string {
	if a == nil {
		return ""
	}
	return strings.TrimPrefix(a.TypeName(), "messageAction")
}

// MediaInfo is the prototype's media_info.
func MediaInfo(mc tg.MessageMediaClass) *Media {
	if mc == nil {
		return nil
	}
	if _, empty := mc.(*tg.MessageMediaEmpty); empty {
		return nil
	}
	kind := strings.ToLower(strings.TrimPrefix(mc.TypeName(), "messageMedia"))
	out := &Media{Type: kind}
	switch m := mc.(type) {
	case *tg.MessageMediaPhoto:
		if p, ok := m.Photo.(*tg.Photo); ok {
			out.Mime = "image/jpeg"
			_, out.Size = largestPhotoSize(p)
		}
	case *tg.MessageMediaDocument:
		if d, ok := m.Document.(*tg.Document); ok {
			out.Type = documentKind(d, kind)
			out.Name = documentFilename(d)
			out.Mime = d.MimeType
			out.Size = d.Size
		}
	case *tg.MessageMediaWebPage:
		if w, ok := m.Webpage.(*tg.WebPage); ok {
			out.URL = w.URL
			out.Title = w.Title
		}
	case *tg.MessageMediaPoll:
		out.Question = m.Poll.Question.Text
	}
	return out
}

// documentKind refines "document" from its attributes (voice, video_note, sticker, gif, video, audio).
func documentKind(d *tg.Document, fallback string) string {
	var audio *tg.DocumentAttributeAudio
	var video *tg.DocumentAttributeVideo
	sticker, animated := false, false
	for _, a := range d.Attributes {
		switch v := a.(type) {
		case *tg.DocumentAttributeAudio:
			audio = v
		case *tg.DocumentAttributeVideo:
			video = v
		case *tg.DocumentAttributeSticker:
			sticker = true
		case *tg.DocumentAttributeAnimated:
			animated = true
		}
	}
	switch {
	case audio != nil && audio.Voice:
		return "voice"
	case video != nil && video.RoundMessage:
		return "video_note"
	case sticker:
		return "sticker"
	case animated:
		return "gif"
	case video != nil:
		return "video"
	case audio != nil:
		return "audio"
	}
	return fallback
}

func documentFilename(d *tg.Document) string {
	for _, a := range d.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			return f.FileName
		}
	}
	return ""
}

// largestPhotoSize returns the biggest downloadable size type and its byte size.
func largestPhotoSize(p *tg.Photo) (string, int64) {
	best, bestSize := "", int64(-1)
	for _, s := range p.Sizes {
		var t string
		var n int64
		switch v := s.(type) {
		case *tg.PhotoSize:
			t, n = v.Type, int64(v.Size)
		case *tg.PhotoSizeProgressive:
			t = v.Type
			for _, x := range v.Sizes {
				if int64(x) > n {
					n = int64(x)
				}
			}
		default:
			continue
		}
		if n > bestSize {
			best, bestSize = t, n
		}
	}
	if bestSize < 0 {
		bestSize = 0
	}
	return best, bestSize
}

// truncateRunes cuts s to n code points (Python's s[:n]).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
