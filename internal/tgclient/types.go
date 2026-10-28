// Package tgclient wraps gotd: connection, locking, session storage, peer resolution
// and the JSON shapes. Commands talk to the Telegram interface so they can be tested
// against a fake (see tgfake).
package tgclient

import (
	"context"
	"time"

	"github.com/gotd/td/tg"
)

// Telegram is everything the read/send commands need from an authorised session.
type Telegram interface {
	Self(ctx context.Context) (*User, error)

	// IterDialogs calls fn for each dialog (pinned first, then by recency) until fn returns false.
	IterDialogs(ctx context.Context, folder Folder, fn func(Dialog) bool) error
	// Resolve accepts a marked id, @username, t.me link, +phone, or a dialog name.
	Resolve(ctx context.Context, ref string) (*Peer, error)
	PeerInfo(ctx context.Context, p *Peer) (*ChatInfo, error)

	History(ctx context.Context, p *Peer, q HistoryQuery) ([]Message, error)
	// Search searches one chat, or the whole account when p is nil.
	Search(ctx context.Context, p *Peer, q SearchQuery) ([]Message, error)
	MessagesByID(ctx context.Context, p *Peer, ids []int) ([]Message, error)
	// Mentions lists messages that mention or reply to the user, in one chat or all (p nil).
	Mentions(ctx context.Context, p *Peer, q MentionsQuery) ([]Message, error)

	SearchContacts(ctx context.Context, query string, limit int) (*ContactsResult, error)
	DownloadMedia(ctx context.Context, p *Peer, msgID int, dir string) (*Download, error)

	SendText(ctx context.Context, p *Peer, text string, replyTo int) (*Message, error)
	SendFile(ctx context.Context, p *Peer, path, caption string, replyTo int) (*Message, error)
}

// Folder selects dialogs by folder.
type Folder int

const (
	FolderAll     Folder = iota // main list and archive
	FolderMain                  // main list only
	FolderArchive               // archive only
)

// HistoryQuery mirrors Telethon's iter_messages arguments.
type HistoryQuery struct {
	Limit      int
	OffsetID   int       // ids below this (or above it when Reverse)
	OffsetDate time.Time // messages before this (or after it when Reverse)
	MinID      int       // exclusive
	MaxID      int       // exclusive
	Reverse    bool      // oldest first
	Topic      int       // forum topic id (messages.getReplies)
}

// SearchQuery is a text search in one chat or account-wide.
type SearchQuery struct {
	Query  string
	From   *Peer // sender filter; needs a chat
	Limit  int
	After  time.Time
	Before time.Time
}

// MentionsQuery lists mentions/replies to the user.
type MentionsQuery struct {
	Unread bool // messages.getUnreadMentions (does not mark them read)
	Limit  int
	Scan   int // without a chat and Unread: how many recent groups to search (0 = default)
}

// Peer is a resolved chat or user. AccessHash never leaves the process.
type Peer struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Username   string `json:"username,omitempty"`
	Kind       Kind   `json:"-"`
	RawID      int64  `json:"-"`
	AccessHash int64  `json:"-"`
}

// ChatRef is the short chat object in command output.
type ChatRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Ref returns the short output form.
func (p *Peer) Ref() ChatRef { return ChatRef{ID: p.ID, Name: p.Name, Type: p.Type} }

// InputPeer builds the MTProto input peer.
func (p *Peer) InputPeer() tg.InputPeerClass {
	switch p.Kind {
	case KindChat:
		return &tg.InputPeerChat{ChatID: p.RawID}
	case KindChannel:
		return &tg.InputPeerChannel{ChannelID: p.RawID, AccessHash: p.AccessHash}
	}
	return &tg.InputPeerUser{UserID: p.RawID, AccessHash: p.AccessHash}
}

// IsGroup is true for basic groups, supergroups and forums.
func (p *Peer) IsGroup() bool {
	return p.Type == "group" || p.Type == "supergroup" || p.Type == "forum"
}

// Message is the JSON shape of a message (field names from the prototype).
type Message struct {
	ID            int     `json:"id"`
	ChatID        int64   `json:"chat_id"`
	ChatName      *string `json:"chat_name"`
	Date          string  `json:"date"`
	FromMe        bool    `json:"from_me"`
	SenderID      *int64  `json:"sender_id"`
	SenderName    *string `json:"sender_name"`
	Text          string  `json:"text"`
	Edited        string  `json:"edited,omitempty"`
	ReplyTo       int     `json:"reply_to,omitempty"`
	ForwardedFrom any     `json:"forwarded_from,omitempty"`
	Media         *Media  `json:"media,omitempty"`
	Album         int64   `json:"album,omitempty"`
	Service       string  `json:"service,omitempty"`
	Views         *int    `json:"views,omitempty"`
	Mentioned     bool    `json:"mentioned,omitempty"`

	Time time.Time `json:"-"`
}

// Media describes an attachment.
type Media struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
	Question string `json:"question,omitempty"`
}

// Dialog is one entry of the chat list.
type Dialog struct {
	ID       int64        `json:"id"`
	Name     string       `json:"name"`
	Type     string       `json:"type"`
	Username *string      `json:"username"`
	Unread   int          `json:"unread"`
	Mentions int          `json:"mentions"`
	Pinned   bool         `json:"pinned"`
	Archived bool         `json:"archived"`
	Muted    bool         `json:"muted"`
	Last     *LastMessage `json:"last,omitempty"`

	Peer           Peer `json:"-"`
	TopMessage     int  `json:"-"`
	ReadInboxMaxID int  `json:"-"`
}

// LastMessage is the dialog's top message preview.
type LastMessage struct {
	ID     int     `json:"id"`
	Date   string  `json:"date"`
	FromMe bool    `json:"from_me"`
	Text   string  `json:"text"`
	Media  *string `json:"media"`
}

// User is the JSON shape of a user (or a chat in contact search).
type User struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Username      *string `json:"username"`
	Phone         *string `json:"phone"`
	Contact       bool    `json:"contact"`
	MutualContact bool    `json:"-"`
}

// ChatInfo is `chats show` output.
type ChatInfo struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Username      *string `json:"username"`
	Phone         *string `json:"phone,omitempty"`
	Contact       *bool   `json:"contact,omitempty"`
	MutualContact *bool   `json:"mutual_contact,omitempty"`
	About         *string `json:"about,omitempty"`
	Members       *int    `json:"members,omitempty"`
}

// ContactsResult is `contacts search` output.
type ContactsResult struct {
	Mine   []User `json:"mine"`
	Public []User `json:"public"`
}

// Download is `media download` output.
type Download struct {
	Path  string `json:"path"`
	Media *Media `json:"media"`
}

// ISO formats a time as UTC "2006-01-02T15:04:05Z".
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
