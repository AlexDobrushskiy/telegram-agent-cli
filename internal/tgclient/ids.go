package tgclient

import "github.com/gotd/td/tg"

// Kind is the MTProto peer kind.
type Kind string

const (
	KindUser    Kind = "user"
	KindChat    Kind = "chat"
	KindChannel Kind = "channel"
)

// channelOffset marks channel ids: -100… in Telethon / Bot API style.
const channelOffset = 1_000_000_000_000

// MarkedID turns a raw id into the marked form: users +id, basic groups -id, channels -100id.
func MarkedID(k Kind, raw int64) int64 {
	switch k {
	case KindChat:
		return -raw
	case KindChannel:
		return -(channelOffset + raw)
	}
	return raw
}

// SplitMarked is the inverse of MarkedID.
func SplitMarked(id int64) (Kind, int64) {
	switch {
	case id >= 0:
		return KindUser, id
	case id <= -channelOffset:
		return KindChannel, -id - channelOffset
	}
	return KindChat, -id
}

// PeerMarkedID returns the marked id of a tg.PeerClass (0 if nil).
func PeerMarkedID(p tg.PeerClass) int64 {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID
	case *tg.PeerChat:
		return MarkedID(KindChat, v.ChatID)
	case *tg.PeerChannel:
		return MarkedID(KindChannel, v.ChannelID)
	}
	return 0
}
