package tgclient

import (
	"context"
	"math"

	"github.com/gotd/td/tg"
)

// chunkSize is Telethon's _MAX_CHUNK_SIZE.
const chunkSize = 100

// page is one request's offsets.
type page struct {
	offsetID   int
	offsetDate int
	addOffset  int
	limit      int
	maxDate    int // messages.search / searchGlobal
	offsetRate int // searchGlobal
	offsetPeer tg.InputPeerClass
}

// pageResult is one response, unpacked.
type pageResult struct {
	messages []tg.MessageClass
	users    []tg.UserClass
	chats    []tg.ChatClass
	more     bool // a slice: more pages may exist
	nextRate int
}

type iterKind int

const (
	iterHistory iterKind = iota // messages.getHistory / getReplies
	iterSearch                  // messages.search
	iterGlobal                  // messages.searchGlobal
)

// iterSpec describes one message iteration; it ports Telethon's _MessagesIter.
type iterSpec struct {
	kind      iterKind
	hasEntity bool
	reverse   bool
	limit     int
	offsetID  int
	offsetDt  int
	minID     int
	maxID     int
	addOffset int
	fromID    int64 // client-side sender filter (0 = none)
	fetch     func(ctx context.Context, p page) (pageResult, error)
	// onPage receives each page's entities (for chat names and the peer cache).
	onPage func(r pageResult) *entities
	// inputPeer gives the input peer of a message's chat (searchGlobal paging).
	inputPeer func(chatID int64) tg.InputPeerClass
	selfID    int64
}

func unpackMessages(r tg.MessagesMessagesClass) pageResult {
	switch v := r.(type) {
	case *tg.MessagesMessages:
		return pageResult{messages: v.Messages, users: v.Users, chats: v.Chats}
	case *tg.MessagesMessagesSlice:
		return pageResult{messages: v.Messages, users: v.Users, chats: v.Chats, more: true, nextRate: v.NextRate}
	case *tg.MessagesChannelMessages:
		return pageResult{messages: v.Messages, users: v.Users, chats: v.Chats, more: true}
	}
	return pageResult{}
}

func msgIDDate(mc tg.MessageClass) (int, int) {
	switch m := mc.(type) {
	case *tg.Message:
		return m.ID, m.Date
	case *tg.MessageService:
		return m.ID, m.Date
	}
	return 0, 0
}

// run iterates like Telethon's iter_messages and returns up to limit messages.
func (it iterSpec) run(ctx context.Context) ([]Message, error) {
	if it.limit <= 0 {
		return nil, nil
	}
	offsetID, minID, maxID := it.offsetID, it.minID, it.maxID
	if it.reverse {
		offsetID = max(offsetID, minID)
		if offsetID > 0 && maxID > 0 && maxID-offsetID <= 1 {
			return nil, nil
		}
		if maxID == 0 {
			maxID = math.MaxInt
		}
		if offsetID > 0 {
			offsetID++
		} else if it.offsetDt == 0 {
			offsetID = 1
		}
	} else {
		offsetID = max(offsetID, maxID)
		if offsetID > 0 && minID > 0 && offsetID-minID <= 1 {
			return nil, nil
		}
	}

	p := page{offsetID: offsetID, offsetDate: it.offsetDt, addOffset: it.addOffset, maxDate: it.offsetDt,
		offsetPeer: &tg.InputPeerEmpty{}}
	hasAddOffset := it.kind != iterGlobal
	if it.reverse && hasAddOffset {
		p.addOffset -= chunkSize
	}
	lastID := math.MaxInt
	if it.reverse {
		lastID = 0
	}
	inRange := func(id int) bool {
		if !it.hasEntity {
			return true
		}
		if it.reverse {
			return id > lastID && id < maxID
		}
		return id < lastID && id > minID
	}

	var out []Message
	for len(out) < it.limit {
		p.limit = min(it.limit-len(out), chunkSize)
		if it.fromID != 0 {
			p.limit = chunkSize // most of a page may be filtered out
		}
		if it.reverse && hasAddOffset && p.limit != chunkSize {
			p.addOffset = it.addOffset - p.limit
		}
		r, err := it.fetch(ctx, p)
		if err != nil {
			return out, err
		}
		ents := it.onPage(r)
		msgs := r.messages
		if it.reverse {
			msgs = make([]tg.MessageClass, len(r.messages))
			for i, m := range r.messages {
				msgs[len(r.messages)-1-i] = m
			}
		}
		// lastRaw/lastChat track the last message seen (kept or filtered) for the next offset.
		var lastRaw tg.MessageClass
		var lastChat int64
		done := false
		for _, mc := range msgs {
			m, ok := ents.toMessage(mc, it.selfID)
			if !ok {
				continue
			}
			if !inRange(m.ID) {
				done = true
				break
			}
			lastRaw, lastChat = mc, m.ChatID
			if it.fromID != 0 && (m.SenderID == nil || *m.SenderID != it.fromID) {
				continue
			}
			lastID = m.ID
			out = append(out, m)
			if len(out) >= it.limit {
				break
			}
		}
		if done || !r.more || len(r.messages) == 0 || lastRaw == nil {
			break
		}
		if first, _ := msgIDDate(r.messages[0]); !it.reverse && first <= p.limit {
			break
		}
		id, date := msgIDDate(lastRaw)
		next := id
		if it.reverse {
			next++
		}
		if next == p.offsetID {
			break // no progress
		}
		p.offsetID = next
		switch it.kind {
		case iterSearch:
			p.maxDate = 0
		case iterGlobal:
			p.offsetRate = r.nextRate
			p.offsetPeer = &tg.InputPeerEmpty{}
			if ip := it.inputPeer(lastChat); ip != nil {
				p.offsetPeer = ip
			}
		default:
			p.offsetDate = date
		}
	}
	if len(out) > it.limit {
		out = out[:it.limit]
	}
	return out, nil
}
