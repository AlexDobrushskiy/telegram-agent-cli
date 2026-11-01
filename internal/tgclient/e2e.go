package tgclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// E2E is what the e2e harness needs from a session: a rich view of one private
// chat with a bot, and the user-side actions on it. Policy (which bot may be
// written to) lives in the caller; these methods only talk to Telegram.
type E2E interface {
	Self(ctx context.Context) (*User, error)
	Resolve(ctx context.Context, ref string) (*Peer, error)

	// BotHistory returns up to limit messages with id > minID, newest first.
	BotHistory(ctx context.Context, p *Peer, minID, limit int) ([]BotMessage, error)
	BotMessagesByID(ctx context.Context, p *Peer, ids []int) ([]BotMessage, error)
	// PinnedID is the chat's pinned message id (0 = none).
	PinnedID(ctx context.Context, p *Peer) (int, error)
	// MenuButton is the bot's menu button for this user (nil = commands list or default).
	MenuButton(ctx context.Context, p *Peer) (*MenuButton, error)

	BotSendText(ctx context.Context, p *Peer, text string, replyTo int) (int, error)
	BotSendMedia(ctx context.Context, p *Peer, m OutMedia, caption string, replyTo int) (int, error)
	// React sets the account's reaction on a message; "" removes it.
	React(ctx context.Context, p *Peer, msgID int, emoji string) error
	PressCallback(ctx context.Context, p *Peer, msgID int, data []byte) (*CallbackAnswer, error)
	SendWebAppData(ctx context.Context, p *Peer, buttonText, data string) error
	// StopDraft taps stop on a live draft (the bot gets stopped_message_generation).
	StopDraft(ctx context.Context, p *Peer, draftID int64) error
	// WebAppURL opens a Mini App the way a client does and returns its URL (with signed initData).
	WebAppURL(ctx context.Context, p *Peer, req WebAppRequest) (string, error)

	// Drafts streams live drafts (bot "typing" a message) seen on this connection.
	Drafts() <-chan Draft
}

// BotMessage is a message in a private chat with a bot, as the e2e harness shows it.
type BotMessage struct {
	ID        int          `json:"id"`
	Date      string       `json:"date"`
	Edited    string       `json:"edited,omitempty"`
	From      string       `json:"from"` // "bot" or "me"
	Text      string       `json:"text"`
	Entities  []Entity     `json:"entities,omitempty"`
	ReplyTo   int          `json:"reply_to,omitempty"`
	Media     *Media       `json:"media,omitempty"`
	Service   *Service     `json:"service,omitempty"`
	Reactions []Reaction   `json:"reactions,omitempty"`
	Inline    [][]Button   `json:"inline_keyboard,omitempty"`
	Markup    *ReplyMarkup `json:"reply_markup,omitempty"`
	Album     int64        `json:"album,omitempty"`
}

// Entity is a formatting entity; Text is the covered part of the message.
type Entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"` // UTF-16 code units, as Telegram counts
	Length   int    `json:"length"`
	Text     string `json:"text"`
	URL      string `json:"url,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	Language string `json:"language,omitempty"`
}

// Service describes a service message (pin, web_app_data, …).
type Service struct {
	Type       string `json:"type"`
	ButtonText string `json:"button_text,omitempty"`
	Data       string `json:"data,omitempty"`
}

// Reaction is one reaction on a message.
type Reaction struct {
	Emoji       string  `json:"emoji,omitempty"`
	CustomEmoji int64   `json:"custom_emoji_id,omitempty"`
	Paid        bool    `json:"paid,omitempty"`
	Count       int     `json:"count"`
	Mine        bool    `json:"mine"`
	By          []int64 `json:"by,omitempty"` // recent reactors (user ids), when Telegram lists them
}

// Button is an inline or reply-keyboard button.
type Button struct {
	Text string `json:"text"`
	// Type: inline: callback|url|web_app|switch_inline|game|buy|url_auth|user_profile|copy|disabled;
	// keyboard: text|request_phone|request_location|request_poll|request_peer|web_app.
	Type       string `json:"type"`
	Data       string `json:"data,omitempty"`        // callback data (UTF-8)
	DataBase64 string `json:"data_base64,omitempty"` // callback data that is not UTF-8
	URL        string `json:"url,omitempty"`
	Query      string `json:"query,omitempty"`
	Copy       string `json:"copy,omitempty"`
	Password   bool   `json:"requires_password,omitempty"`

	raw []byte
}

// CallbackData returns the raw callback bytes (from Data/DataBase64 when the
// button was not converted from Telegram's).
func (b Button) CallbackData() []byte {
	if b.raw != nil {
		return b.raw
	}
	if b.DataBase64 != "" {
		if d, err := base64.StdEncoding.DecodeString(b.DataBase64); err == nil {
			return d
		}
	}
	return []byte(b.Data)
}

// ReplyMarkup is a non-inline markup: a reply keyboard, its removal, or force-reply.
type ReplyMarkup struct {
	Type        string     `json:"type"` // keyboard|hide|force_reply
	Rows        [][]Button `json:"rows,omitempty"`
	Persistent  bool       `json:"persistent,omitempty"`
	Resize      bool       `json:"resize,omitempty"`
	OneTime     bool       `json:"one_time,omitempty"`
	Selective   bool       `json:"selective,omitempty"`
	Placeholder string     `json:"placeholder,omitempty"`
}

// OutMedia is a media message to send.
type OutMedia struct {
	Kind     string // photo|voice|file|location
	Path     string
	Duration int // voice seconds
	Lat      float64
	Long     float64
}

// CallbackAnswer is the bot's answerCallbackQuery as the client sees it.
type CallbackAnswer struct {
	Answered bool   `json:"answered"` // false: the bot did not answer in time
	Message  string `json:"message,omitempty"`
	Alert    bool   `json:"alert,omitempty"`
	URL      string `json:"url,omitempty"`
}

// WebAppRequest says which Mini App entry point to open.
type WebAppRequest struct {
	Kind string // keyboard (reply keyboard web_app button) | inline | menu | main
	URL  string // the button's or menu's URL (keyboard, inline, menu)
	// Platform is the client platform reported to Telegram and the Mini App (default "android").
	Platform string
}

// MenuButton is a bot's menu button that opens a Mini App.
type MenuButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Draft is one live-draft update (Bot API sendMessageDraft).
type Draft struct {
	At         time.Time `json:"-"`
	Date       string    `json:"date"`
	RandomID   int64     `json:"draft_id"`
	Text       string    `json:"text"`
	CanStop    bool      `json:"can_stop,omitempty"`
	KeepOnStop bool      `json:"keep_on_stop,omitempty"`
	UserID     int64     `json:"-"`
}

var _ E2E = (*E2ESession)(nil)

// E2ESession is a Session plus a draft stream fed by the connection's updates.
type E2ESession struct {
	*Session
	drafts chan Draft
}

func (s *E2ESession) Drafts() <-chan Draft { return s.drafts }

// draftHandler collects live-draft typing updates; it never blocks the client.
type draftHandler struct {
	ch  chan Draft
	now func() time.Time
}

func (h *draftHandler) Handle(_ context.Context, u tg.UpdatesClass) error {
	var list []tg.UpdateClass
	switch v := u.(type) {
	case *tg.UpdateShort:
		list = []tg.UpdateClass{v.Update}
	case *tg.Updates:
		list = v.Updates
	case *tg.UpdatesCombined:
		list = v.Updates
	}
	for _, x := range list {
		t, ok := x.(*tg.UpdateUserTyping)
		if !ok {
			continue
		}
		a, ok := t.Action.(*tg.SendMessageTextDraftAction)
		if !ok {
			continue
		}
		at := h.now()
		d := Draft{At: at, Date: ISO(at), RandomID: a.RandomID, Text: a.Text.Text, CanStop: a.CanStop, KeepOnStop: a.KeepOnStop, UserID: t.UserID}
		select {
		case h.ch <- d:
		default: // a full buffer drops the oldest view, never the connection
		}
	}
	return nil
}

var _ telegram.UpdateHandler = (*draftHandler)(nil)

// RunE2E is RunSession for the e2e harness: updates are on so live drafts arrive.
func RunE2E(ctx context.Context, opts Options, fn func(ctx context.Context, t E2E) error) error {
	opts.NeedAuth = true
	h := &draftHandler{ch: make(chan Draft, 512), now: time.Now}
	opts.UpdateHandler = h
	return Run(ctx, opts, func(ctx context.Context, c *Conn) error {
		return fn(ctx, &E2ESession{Session: c.Session(), drafts: h.ch})
	})
}

// BotHistory reads the chat without marking anything read.
func (s *Session) BotHistory(ctx context.Context, p *Peer, minID, limit int) ([]BotMessage, error) {
	r, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: p.InputPeer(), MinID: minID, Limit: limit})
	if err != nil {
		return nil, err
	}
	pr := unpackMessages(r)
	s.absorb(pr.users, pr.chats)
	var out []BotMessage
	for _, mc := range pr.messages {
		if m, ok := ToBotMessage(mc); ok && m.ID > minID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Session) BotMessagesByID(ctx context.Context, p *Peer, ids []int) ([]BotMessage, error) {
	input := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		input[i] = &tg.InputMessageID{ID: id}
	}
	r, err := s.api.MessagesGetMessages(ctx, input)
	if err != nil {
		return nil, err
	}
	pr := unpackMessages(r)
	s.absorb(pr.users, pr.chats)
	var out []BotMessage
	for _, mc := range pr.messages {
		nm, ok := mc.AsNotEmpty()
		if !ok || PeerMarkedID(nm.GetPeerID()) != p.ID {
			continue // messages.getMessages is account-wide for users
		}
		if m, ok := ToBotMessage(mc); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Session) fullUser(ctx context.Context, p *Peer) (*tg.UserFull, error) {
	if p.Kind != KindUser {
		return nil, output.Usage("%s is not a user", p.Name)
	}
	full, err := s.api.UsersGetFullUser(ctx, &tg.InputUser{UserID: p.RawID, AccessHash: p.AccessHash})
	if err != nil {
		return nil, err
	}
	s.absorb(full.Users, full.Chats)
	return &full.FullUser, nil
}

func (s *Session) PinnedID(ctx context.Context, p *Peer) (int, error) {
	f, err := s.fullUser(ctx, p)
	if err != nil {
		return 0, err
	}
	id, _ := f.GetPinnedMsgID()
	return id, nil
}

func (s *Session) MenuButton(ctx context.Context, p *Peer) (*MenuButton, error) {
	f, err := s.fullUser(ctx, p)
	if err != nil {
		return nil, err
	}
	info, ok := f.GetBotInfo()
	if !ok {
		return nil, nil
	}
	mb, ok := info.GetMenuButton()
	if !ok {
		return nil, nil
	}
	if b, ok := mb.(*tg.BotMenuButton); ok {
		return &MenuButton{Text: b.Text, URL: b.URL}, nil
	}
	return nil, nil
}

func (s *Session) BotSendText(ctx context.Context, p *Peer, text string, reply int) (int, error) {
	rid, err := randomID()
	if err != nil {
		return 0, err
	}
	upd, err := s.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: p.InputPeer(), Message: text, RandomID: rid, NoWebpage: true, ReplyTo: replyTo(reply),
	})
	if err != nil {
		return 0, err
	}
	return sentID(upd, rid)
}

func (s *Session) BotSendMedia(ctx context.Context, p *Peer, m OutMedia, caption string, reply int) (int, error) {
	media, err := s.outMedia(ctx, m)
	if err != nil {
		return 0, err
	}
	rid, err := randomID()
	if err != nil {
		return 0, err
	}
	upd, err := s.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: p.InputPeer(), Media: media, Message: caption, RandomID: rid, ReplyTo: replyTo(reply),
	})
	if err != nil {
		return 0, err
	}
	return sentID(upd, rid)
}

func (s *Session) outMedia(ctx context.Context, m OutMedia) (tg.InputMediaClass, error) {
	switch m.Kind {
	case "location":
		return &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: m.Lat, Long: m.Long}}, nil
	case "photo":
		f, err := uploader.NewUploader(s.api).FromPath(ctx, m.Path)
		if err != nil {
			return nil, err
		}
		return &tg.InputMediaUploadedPhoto{File: f}, nil
	case "voice":
		f, err := uploader.NewUploader(s.api).FromPath(ctx, m.Path)
		if err != nil {
			return nil, err
		}
		return &tg.InputMediaUploadedDocument{
			File: f, MimeType: "audio/ogg",
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true, Duration: m.Duration}},
		}, nil
	case "file":
		f, err := uploader.NewUploader(s.api).FromPath(ctx, m.Path)
		if err != nil {
			return nil, err
		}
		return &tg.InputMediaUploadedDocument{
			File: f, MimeType: mimeFor(m.Path), ForceFile: true,
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: baseName(m.Path)}},
		}, nil
	}
	return nil, output.Usage("unknown media kind %q", m.Kind)
}

func (s *Session) React(ctx context.Context, p *Peer, msgID int, emoji string) error {
	req := &tg.MessagesSendReactionRequest{Peer: p.InputPeer(), MsgID: msgID}
	if emoji != "" {
		req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}})
	}
	_, err := s.api.MessagesSendReaction(ctx, req)
	return err
}

func (s *Session) PressCallback(ctx context.Context, p *Peer, msgID int, data []byte) (*CallbackAnswer, error) {
	req := &tg.MessagesGetBotCallbackAnswerRequest{Peer: p.InputPeer(), MsgID: msgID}
	req.SetData(data)
	r, err := s.api.MessagesGetBotCallbackAnswer(ctx, req)
	if tgerr.Is(err, "BOT_RESPONSE_TIMEOUT") {
		return &CallbackAnswer{Answered: false}, nil // pressed; the bot never called answerCallbackQuery
	}
	if err != nil {
		return nil, err
	}
	return &CallbackAnswer{Answered: true, Message: r.Message, Alert: r.Alert, URL: r.URL}, nil
}

func (s *Session) SendWebAppData(ctx context.Context, p *Peer, buttonText, data string) error {
	rid, err := randomID()
	if err != nil {
		return err
	}
	_, err = s.api.MessagesSendWebViewData(ctx, &tg.MessagesSendWebViewDataRequest{
		Bot: &tg.InputUser{UserID: p.RawID, AccessHash: p.AccessHash}, RandomID: rid, ButtonText: buttonText, Data: data,
	})
	return err
}

// DefaultWebAppPlatform is the client platform the harness reports.
const DefaultWebAppPlatform = "android"

func (s *Session) WebAppURL(ctx context.Context, p *Peer, r WebAppRequest) (string, error) {
	bot := &tg.InputUser{UserID: p.RawID, AccessHash: p.AccessHash}
	webAppPlatform := r.Platform
	if webAppPlatform == "" {
		webAppPlatform = DefaultWebAppPlatform
	}
	var res tg.WebViewResultURL
	switch r.Kind {
	case "keyboard":
		req := &tg.MessagesRequestSimpleWebViewRequest{Bot: bot, Platform: webAppPlatform}
		req.SetURL(r.URL)
		v, err := s.api.MessagesRequestSimpleWebView(ctx, req)
		if err != nil {
			return "", err
		}
		res = *v
	case "inline", "menu":
		req := &tg.MessagesRequestWebViewRequest{Peer: p.InputPeer(), Bot: bot, Platform: webAppPlatform}
		req.SetURL(r.URL)
		if r.Kind == "menu" {
			req.FromBotMenu = true
		}
		v, err := s.api.MessagesRequestWebView(ctx, req)
		if err != nil {
			return "", err
		}
		res = *v
	case "main":
		v, err := s.api.MessagesRequestMainWebView(ctx, &tg.MessagesRequestMainWebViewRequest{Peer: p.InputPeer(), Bot: bot, Platform: webAppPlatform})
		if err != nil {
			return "", err
		}
		res = *v
	default:
		return "", output.Usage("unknown web app kind %q", r.Kind)
	}
	return res.URL, nil
}

// sentID finds the id of the message a send created.
func sentID(upd tg.UpdatesClass, rid int64) (int, error) {
	var updates []tg.UpdateClass
	switch v := upd.(type) {
	case *tg.UpdateShortSentMessage:
		return v.ID, nil
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdatesCombined:
		updates = v.Updates
	default:
		return 0, fmt.Errorf("unexpected send result %T", upd)
	}
	for _, u := range updates {
		if m, ok := u.(*tg.UpdateMessageID); ok && m.RandomID == rid {
			return m.ID, nil
		}
	}
	for _, u := range updates {
		if m, ok := u.(*tg.UpdateNewMessage); ok {
			if nm, ok := m.Message.(*tg.Message); ok && nm.Out {
				return nm.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("sent, but Telegram returned no message id")
}

// ToBotMessage converts a message of a private bot chat; ok is false for empty ones.
func ToBotMessage(mc tg.MessageClass) (BotMessage, bool) {
	from := func(out bool) string {
		if out {
			return "me"
		}
		return "bot"
	}
	switch m := mc.(type) {
	case *tg.Message:
		out := BotMessage{
			ID: m.ID, Date: ISO(unix(m.Date)), From: from(m.Out), Text: m.Message,
			Entities: entitiesJSON(m.Message, m.Entities), Media: MediaInfo(m.Media), Album: m.GroupedID,
		}
		if m.EditDate > 0 {
			out.Edited = ISO(unix(m.EditDate))
		}
		if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID > 0 {
			out.ReplyTo = h.ReplyToMsgID
		}
		out.Reactions = reactionsJSON(m.Reactions)
		switch rm := m.ReplyMarkup.(type) {
		case *tg.ReplyInlineMarkup:
			out.Inline = inlineRows(rm.Rows)
		case nil:
		default:
			out.Markup = MarkupJSON(rm)
		}
		return out, true
	case *tg.MessageService:
		out := BotMessage{ID: m.ID, Date: ISO(unix(m.Date)), From: from(m.Out), Service: serviceJSON(m.Action)}
		if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID > 0 {
			out.ReplyTo = h.ReplyToMsgID
		}
		out.Reactions = reactionsJSON(m.Reactions)
		return out, true
	}
	return BotMessage{}, false
}

func serviceJSON(a tg.MessageActionClass) *Service {
	s := &Service{Type: actionName(a)}
	switch v := a.(type) {
	case *tg.MessageActionWebViewDataSentMe:
		s.ButtonText, s.Data = v.Text, v.Data
	case *tg.MessageActionWebViewDataSent:
		s.ButtonText = v.Text
	}
	return s
}

// MarkupJSON converts a non-inline reply markup (nil for inline or none).
func MarkupJSON(rm tg.ReplyMarkupClass) *ReplyMarkup {
	switch v := rm.(type) {
	case *tg.ReplyKeyboardMarkup:
		return &ReplyMarkup{
			Type: "keyboard", Rows: keyboardRows(v.Rows), Persistent: v.Persistent, Resize: v.Resize,
			OneTime: v.SingleUse, Selective: v.Selective, Placeholder: v.Placeholder,
		}
	case *tg.ReplyKeyboardHide:
		return &ReplyMarkup{Type: "hide", Selective: v.Selective}
	case *tg.ReplyKeyboardForceReply:
		return &ReplyMarkup{Type: "force_reply", OneTime: v.SingleUse, Selective: v.Selective, Placeholder: v.Placeholder}
	}
	return nil
}

func keyboardRows(rows []tg.KeyboardButtonRow) [][]Button {
	out := make([][]Button, 0, len(rows))
	for _, r := range rows {
		row := make([]Button, 0, len(r.Buttons))
		for _, b := range r.Buttons {
			btn := Button{Text: b.Text, Type: "text"}
			switch t := b.Type.(type) {
			case *tg.ButtonTypeRequestPhone:
				btn.Type = "request_phone"
			case *tg.ButtonTypeRequestGeoLocation:
				btn.Type = "request_location"
			case *tg.ButtonTypeRequestPoll:
				btn.Type = "request_poll"
			case *tg.ButtonTypeRequestPeer:
				btn.Type = "request_peer"
			case *tg.ButtonTypeSimpleWebView:
				btn.Type, btn.URL = "web_app", t.URL
			}
			row = append(row, btn)
		}
		out = append(out, row)
	}
	return out
}

func inlineRows(rows []tg.KeyboardInlineButtonRow) [][]Button {
	out := make([][]Button, 0, len(rows))
	for _, r := range rows {
		row := make([]Button, 0, len(r.Buttons))
		for _, b := range r.Buttons {
			btn := Button{Text: b.Text, Type: strings.TrimPrefix(strings.ToLower(typeName(b.Type)), "inlinebuttontype")}
			switch t := b.Type.(type) {
			case *tg.InlineButtonTypeCallback:
				btn.Type, btn.raw, btn.Password = "callback", t.Data, t.RequiresPassword
				if utf8.Valid(t.Data) {
					btn.Data = string(t.Data)
				} else {
					btn.DataBase64 = base64.StdEncoding.EncodeToString(t.Data)
				}
			case *tg.InlineButtonTypeURL:
				btn.Type, btn.URL = "url", t.URL
			case *tg.InlineButtonTypeWebView:
				btn.Type, btn.URL = "web_app", t.URL
			case *tg.InlineButtonTypeURLAuth:
				btn.Type, btn.URL = "url_auth", t.URL
			case *tg.InlineButtonTypeSwitchInline:
				btn.Type, btn.Query = "switch_inline", t.Query
			case *tg.InlineButtonTypeCopy:
				btn.Type, btn.Copy = "copy", t.CopyText
			case *tg.InlineButtonTypeUserProfile:
				btn.Type = "user_profile"
			}
			row = append(row, btn)
		}
		out = append(out, row)
	}
	return out
}

func typeName(v any) string {
	if n, ok := v.(interface{ TypeName() string }); ok {
		return n.TypeName()
	}
	return ""
}

func reactionsJSON(r tg.MessageReactions) []Reaction {
	if len(r.Results) == 0 {
		return nil
	}
	recent := map[string][]int64{}
	for _, pr := range r.RecentReactions {
		k := reactionKey(pr.Reaction)
		recent[k] = append(recent[k], PeerMarkedID(pr.PeerID))
	}
	out := make([]Reaction, 0, len(r.Results))
	for _, rc := range r.Results {
		x := Reaction{Count: rc.Count, Mine: rc.ChosenOrder > 0 || hasChosen(rc)}
		switch v := rc.Reaction.(type) {
		case *tg.ReactionEmoji:
			x.Emoji = v.Emoticon
		case *tg.ReactionCustomEmoji:
			x.CustomEmoji = v.DocumentID
		case *tg.ReactionPaid:
			x.Paid = true
		}
		x.By = recent[reactionKey(rc.Reaction)]
		out = append(out, x)
	}
	return out
}

func hasChosen(rc tg.ReactionCount) bool {
	_, ok := rc.GetChosenOrder()
	return ok
}

func reactionKey(r tg.ReactionClass) string {
	switch v := r.(type) {
	case *tg.ReactionEmoji:
		return "e:" + v.Emoticon
	case *tg.ReactionCustomEmoji:
		return fmt.Sprintf("c:%d", v.DocumentID)
	case *tg.ReactionPaid:
		return "paid"
	}
	return "?"
}

func entitiesJSON(text string, ents []tg.MessageEntityClass) []Entity {
	if len(ents) == 0 {
		return nil
	}
	u := utf16.Encode([]rune(text))
	out := make([]Entity, 0, len(ents))
	for _, ec := range ents {
		off, n := ec.GetOffset(), ec.GetLength()
		e := Entity{Type: entityType(ec.TypeName()), Offset: off, Length: n}
		if off >= 0 && n >= 0 && off+n <= len(u) {
			e.Text = string(utf16.Decode(u[off : off+n]))
		}
		switch v := ec.(type) {
		case *tg.MessageEntityTextURL:
			e.URL = v.URL
		case *tg.MessageEntityMentionName:
			e.UserID = v.UserID
		case *tg.MessageEntityPre:
			e.Language = v.Language
		}
		out = append(out, e)
	}
	return out
}

// entityRenames maps MTProto entity names to the Bot API's where they differ.
var entityRenames = map[string]string{
	"text_url": "text_link", "mention_name": "text_mention", "strike": "strikethrough",
	"phone": "phone_number", "blockquote": "blockquote",
}

// entityType turns "messageEntityBotCommand" into the Bot API name "bot_command".
func entityType(tl string) string {
	name := strings.TrimPrefix(tl, "messageEntity")
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	if n, ok := entityRenames[b.String()]; ok {
		return n
	}
	return b.String()
}

// sendMessageStopDraftActionTypeID is sendMessageStopDraftAction#fbf902b0
// random_id:long (newer than gotd's layer): what a client sends when the user
// taps stop on a live draft.
const sendMessageStopDraftActionTypeID = 0xfbf902b0

// StopDraftRequest is messages.setTyping with sendMessageStopDraftAction,
// encoded by hand because gotd's schema predates the action.
type StopDraftRequest struct {
	Peer     *tg.InputPeerUser
	RandomID int64
}

// TypeName is the method name the guard checks.
func (r *StopDraftRequest) TypeName() string { return "messages.setTyping" }

// TargetUserIDs names the user the request addresses (for the guard's bot grant).
func (r *StopDraftRequest) TargetUserIDs() ([]int64, bool) {
	if r.Peer == nil {
		return nil, false
	}
	return []int64{r.Peer.UserID}, true
}

func (r *StopDraftRequest) Encode(b *bin.Buffer) error {
	if r.Peer == nil {
		return fmt.Errorf("stop draft: no peer")
	}
	b.PutID(tg.MessagesSetTypingRequestTypeID)
	b.PutInt(0) // flags: no top_msg_id
	if err := r.Peer.Encode(b); err != nil {
		return err
	}
	b.PutID(sendMessageStopDraftActionTypeID)
	b.PutLong(r.RandomID)
	return nil
}

func (r *StopDraftRequest) Decode(*bin.Buffer) error {
	return fmt.Errorf("stop draft: decode not supported")
}

// StopDraft taps "stop" on the bot's live draft draftID.
func (s *Session) StopDraft(ctx context.Context, p *Peer, draftID int64) error {
	if p.Kind != KindUser {
		return output.Usage("%s is not a user", p.Name)
	}
	var ok tg.BoolBox
	return s.api.Invoker().Invoke(ctx, &StopDraftRequest{
		Peer: &tg.InputPeerUser{UserID: p.RawID, AccessHash: p.AccessHash}, RandomID: draftID,
	}, &ok)
}
