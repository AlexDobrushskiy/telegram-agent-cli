package tgclient

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestToBotMessage(t *testing.T) {
	text := "🎯 p · green:~/x see docs"
	m := &tg.Message{
		ID: 7, Date: 1793525724, EditDate: 1793525776, Message: text,
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 5},
		Entities: []tg.MessageEntityClass{
			// "🎯" is two UTF-16 units, so "/x" starts at 14
			&tg.MessageEntityBotCommand{Offset: 14, Length: 2},
			&tg.MessageEntityTextURL{Offset: 21, Length: 4, URL: "https://example.com"},
		},
		Reactions: tg.MessageReactions{
			Results: []tg.ReactionCount{
				{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1, ChosenOrder: 1},
				{Reaction: &tg.ReactionEmoji{Emoticon: "👀"}, Count: 1},
			},
			RecentReactions: []tg.MessagePeerReaction{
				{PeerID: &tg.PeerUser{UserID: 500}, Reaction: &tg.ReactionEmoji{Emoticon: "👀"}},
			},
		},
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{
			{Text: "A", Type: &tg.InlineButtonTypeCallback{Data: []byte("cb_A")}},
			{Text: "bin", Type: &tg.InlineButtonTypeCallback{Data: []byte{0xff, 0x00}}},
			{Text: "Open", Type: &tg.InlineButtonTypeWebView{URL: "https://app.example/"}},
			{Text: "Site", Type: &tg.InlineButtonTypeURL{URL: "https://example.com"}},
		}}}},
	}
	got, ok := ToBotMessage(m)
	if !ok || got.ID != 7 || got.From != "bot" || got.ReplyTo != 5 || got.Edited != "2026-11-01T09:36:16Z" {
		t.Fatalf("message = %+v", got)
	}
	if e := got.Entities[0]; e.Type != "bot_command" || e.Text != "/x" {
		t.Errorf("entity 0 = %+v", e)
	}
	if e := got.Entities[1]; e.Type != "text_link" || e.Text != "docs" || e.URL != "https://example.com" {
		t.Errorf("entity 1 = %+v", e)
	}
	r := got.Reactions
	if len(r) != 2 || r[0].Emoji != "👍" || !r[0].Mine || r[1].Mine || len(r[1].By) != 1 || r[1].By[0] != 500 {
		t.Errorf("reactions = %+v", r)
	}
	row := got.Inline[0]
	if row[0].Type != "callback" || row[0].Data != "cb_A" || string(row[0].CallbackData()) != "cb_A" {
		t.Errorf("callback button = %+v", row[0])
	}
	if row[1].Data != "" || row[1].DataBase64 != "/wA=" || len(row[1].CallbackData()) != 2 {
		t.Errorf("binary callback = %+v", row[1])
	}
	if row[2].Type != "web_app" || row[3].Type != "url" || row[3].URL != "https://example.com" {
		t.Errorf("other buttons = %+v", row[2:])
	}
}

func TestReplyKeyboardShapes(t *testing.T) {
	m := &tg.Message{ID: 3, Out: false, ReplyMarkup: &tg.ReplyKeyboardMarkup{
		Persistent: true, Resize: true, Placeholder: "→ vault · black",
		Rows: []tg.KeyboardButtonRow{
			{Buttons: []tg.KeyboardButton{
				{Text: "🎯 vault@black ▾", Type: &tg.ButtonTypeSimpleWebView{URL: "https://picker.example/"}},
				{Text: "ev-test@green", Type: &tg.ButtonTypeDefault{}},
			}},
			{Buttons: []tg.KeyboardButton{{Text: "phone", Type: &tg.ButtonTypeRequestPhone{}}}},
		},
	}}
	got, _ := ToBotMessage(m)
	k := got.Markup
	if k == nil || k.Type != "keyboard" || !k.Persistent || !k.Resize || k.Placeholder != "→ vault · black" {
		t.Fatalf("markup = %+v", k)
	}
	if b := k.Rows[0][0]; b.Type != "web_app" || b.URL != "https://picker.example/" {
		t.Errorf("web_app button = %+v", b)
	}
	if k.Rows[0][1].Type != "text" || k.Rows[1][0].Type != "request_phone" {
		t.Errorf("buttons = %+v", k.Rows)
	}
	for rm, want := range map[tg.ReplyMarkupClass]string{
		&tg.ReplyKeyboardHide{}:                         "hide",
		&tg.ReplyKeyboardForceReply{Placeholder: "say"}: "force_reply",
	} {
		got, _ := ToBotMessage(&tg.Message{ID: 1, ReplyMarkup: rm})
		if got.Markup == nil || got.Markup.Type != want || got.Inline != nil {
			t.Errorf("%T -> %+v", rm, got.Markup)
		}
	}
}

func TestServiceMessages(t *testing.T) {
	got, ok := ToBotMessage(&tg.MessageService{ID: 9, Out: true, Action: &tg.MessageActionWebViewDataSent{Text: "🎯 pick"}})
	if !ok || got.From != "me" || got.Service.Type != "WebViewDataSent" || got.Service.ButtonText != "🎯 pick" {
		t.Errorf("web view data sent = %+v", got)
	}
	got, _ = ToBotMessage(&tg.MessageService{ID: 10, Action: &tg.MessageActionWebViewDataSentMe{Text: "b", Data: "{}"}})
	if got.Service.Data != "{}" {
		t.Errorf("web view data sent me = %+v", got.Service)
	}
	got, _ = ToBotMessage(&tg.MessageService{ID: 11, Action: &tg.MessageActionPinMessage{}, ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 8}})
	if got.Service.Type != "PinMessage" || got.ReplyTo != 8 {
		t.Errorf("pin = %+v", got)
	}
	if _, ok := ToBotMessage(&tg.MessageEmpty{ID: 1}); ok {
		t.Error("empty message converted")
	}
}

func TestEntityType(t *testing.T) {
	for tl, want := range map[string]string{
		"messageEntityBotCommand":  "bot_command",
		"messageEntityTextUrl":     "text_link",
		"messageEntityMentionName": "text_mention",
		"messageEntityStrike":      "strikethrough",
		"messageEntityPhone":       "phone_number",
		"messageEntityBold":        "bold",
		"messageEntityCustomEmoji": "custom_emoji",
		"messageEntityUrl":         "url",
	} {
		if got := entityType(tl); got != want {
			t.Errorf("entityType(%s) = %s, want %s", tl, got, want)
		}
	}
}

func TestSentID(t *testing.T) {
	if id, err := sentID(&tg.UpdateShortSentMessage{ID: 42}, 1); err != nil || id != 42 {
		t.Errorf("short = %d, %v", id, err)
	}
	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 9, RandomID: 2},
		&tg.UpdateMessageID{ID: 43, RandomID: 7},
	}}
	if id, err := sentID(upd, 7); err != nil || id != 43 {
		t.Errorf("by random id = %d, %v", id, err)
	}
	upd = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 44, Out: true}}}}
	if id, err := sentID(upd, 7); err != nil || id != 44 {
		t.Errorf("by new message = %d, %v", id, err)
	}
	if _, err := sentID(&tg.Updates{}, 7); err == nil {
		t.Error("no id: no error")
	}
}

func TestStopDraftEncoding(t *testing.T) {
	req := &StopDraftRequest{Peer: &tg.InputPeerUser{UserID: 500, AccessHash: 9}, RandomID: 888}
	var b bin.Buffer
	if err := req.Encode(&b); err != nil {
		t.Fatal(err)
	}
	// messages.setTyping#58943ee2 flags:# peer:InputPeer action:SendMessageAction
	if id, _ := b.ID(); id != tg.MessagesSetTypingRequestTypeID {
		t.Fatalf("id = %#x", id)
	}
	_ = b.ConsumeID(tg.MessagesSetTypingRequestTypeID)
	if flags, _ := b.Int(); flags != 0 {
		t.Fatalf("flags = %d", flags)
	}
	peer, err := tg.DecodeInputPeer(&b)
	if u, ok := peer.(*tg.InputPeerUser); err != nil || !ok || u.UserID != 500 || u.AccessHash != 9 {
		t.Fatalf("peer = %+v, %v", peer, err)
	}
	if id, _ := b.ID(); id != sendMessageStopDraftActionTypeID {
		t.Fatalf("action id = %#x", id)
	}
	_ = b.ConsumeID(sendMessageStopDraftActionTypeID)
	if rid, _ := b.Long(); rid != 888 || b.Len() != 0 {
		t.Fatalf("random id = %d, %d bytes left", rid, b.Len())
	}
	if ids, ok := req.TargetUserIDs(); !ok || len(ids) != 1 || ids[0] != 500 || req.TypeName() != "messages.setTyping" {
		t.Error("guard view of the request")
	}
	if err := (&StopDraftRequest{}).Encode(&b); err == nil {
		t.Error("nil peer encoded")
	}
}

func TestDraftHandler(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := &draftHandler{ch: make(chan Draft, 1), now: func() time.Time { return at }}
	draft := &tg.UpdateUserTyping{UserID: 500, Action: &tg.SendMessageTextDraftAction{
		RandomID: 7, Text: tg.TextWithEntities{Text: "Thinking"}, CanStop: true,
	}}
	typing := &tg.UpdateUserTyping{UserID: 500, Action: &tg.SendMessageTypingAction{}}
	ctx := context.Background()
	_ = h.Handle(ctx, &tg.UpdateShort{Update: typing})
	_ = h.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{typing, draft}})
	_ = h.Handle(ctx, &tg.UpdateShort{Update: draft}) // buffer full: dropped, never blocks
	d := <-h.ch
	if d.UserID != 500 || d.RandomID != 7 || d.Text != "Thinking" || !d.CanStop || d.Date != "2026-01-01T00:00:00Z" {
		t.Fatalf("draft = %+v", d)
	}
	select {
	case extra := <-h.ch:
		t.Fatalf("unexpected draft %+v", extra)
	default:
	}
}
