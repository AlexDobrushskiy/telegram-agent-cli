package guard

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

var (
	peer    = &tg.InputPeerUser{UserID: 1, AccessHash: 2}
	channel = &tg.InputChannel{ChannelID: 3, AccessHash: 4}
)

func TestReadsPass(t *testing.T) {
	reads := []bin.Object{
		&tg.MessagesGetHistoryRequest{Peer: peer},
		&tg.MessagesSearchRequest{Peer: peer, Q: "x", Filter: &tg.InputMessagesFilterEmpty{}},
		&tg.MessagesSearchGlobalRequest{Q: "x", Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}},
		&tg.ContactsResolveUsernameRequest{Username: "example"},
		&tg.ContactsResolvePhoneRequest{Phone: "10000000000"},
		&tg.ContactsSearchRequest{Q: "x"},
		&tg.UploadGetFileRequest{Location: &tg.InputDocumentFileLocation{}},
		&tg.UpdatesGetStateRequest{},
		&tg.MessagesGetMessagesViewsRequest{Peer: peer, ID: []int{1}},
		&tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}},
		&tg.MessagesGetRepliesRequest{Peer: peer},
		&tg.MessagesGetUnreadMentionsRequest{Peer: peer},
		&tg.MessagesGetMessagesRequest{},
		&tg.ChannelsGetMessagesRequest{Channel: channel},
		&tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}},
		&tg.UsersGetFullUserRequest{ID: &tg.InputUserSelf{}},
		&tg.ChannelsGetFullChannelRequest{Channel: channel},
		&tg.MessagesGetFullChatRequest{ChatID: 5},
		&tg.MessagesCheckChatInviteRequest{Hash: "abc"},
		&tg.ContactsGetContactsRequest{},
		&tg.HelpGetConfigRequest{},
		&tg.AccountGetPasswordRequest{},
		&tg.AuthExportAuthorizationRequest{DCID: 4},
		&tg.AuthImportAuthorizationRequest{ID: 1},
		&tg.UploadReuploadCDNFileRequest{},
		&tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: &tg.HelpGetConfigRequest{}},
		&tg.InitConnectionRequest{Query: &tg.InvokeWithoutUpdatesRequest{Query: &tg.UsersGetUsersRequest{}}},
	}
	for _, r := range reads {
		t.Run(MethodName(r), func(t *testing.T) {
			if err := Check(r, true, false); err != nil {
				t.Fatalf("read blocked: %v", err)
			}
		})
	}
}

func TestWritesBlocked(t *testing.T) {
	writes := []bin.Object{
		&tg.MessagesSendMessageRequest{Peer: peer, Message: "x"},
		&tg.MessagesSendMediaRequest{Peer: peer},
		&tg.MessagesEditMessageRequest{Peer: peer, ID: 1},
		&tg.MessagesDeleteMessagesRequest{ID: []int{1}},
		&tg.ChannelsDeleteMessagesRequest{Channel: channel, ID: []int{1}},
		&tg.MessagesForwardMessagesRequest{FromPeer: peer, ToPeer: peer},
		&tg.MessagesSendReactionRequest{Peer: peer},
		&tg.MessagesReadHistoryRequest{Peer: peer},
		&tg.ChannelsReadHistoryRequest{Channel: channel},
		&tg.MessagesReadMentionsRequest{Peer: peer},
		&tg.MessagesReadReactionsRequest{Peer: peer},
		&tg.MessagesReadMessageContentsRequest{ID: []int{1}},
		&tg.ChannelsReadMessageContentsRequest{Channel: channel},
		&tg.MessagesSetTypingRequest{Peer: peer, Action: &tg.SendMessageTypingAction{}},
		&tg.AccountUpdateStatusRequest{Offline: false},
		&tg.AccountUpdateProfileRequest{},
		&tg.AccountUpdateUsernameRequest{},
		&tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: peer}},
		&tg.ChannelsJoinChannelRequest{Channel: channel},
		&tg.ChannelsLeaveChannelRequest{Channel: channel},
		&tg.MessagesImportChatInviteRequest{Hash: "abc"},
		&tg.StoriesReadStoriesRequest{Peer: peer},
		&tg.StoriesIncrementStoryViewsRequest{Peer: peer},
		&tg.MessagesGetMessagesViewsRequest{Peer: peer, ID: []int{1}, Increment: true},
		&tg.MessagesGetBotCallbackAnswerRequest{Peer: peer},
		&tg.MessagesGetInlineBotResultsRequest{Bot: &tg.InputUserSelf{}, Peer: peer},
		&tg.MessagesGetAttachMenuBotRequest{Bot: &tg.InputUserSelf{}},
		&tg.MessagesRequestWebViewRequest{Peer: peer, Bot: &tg.InputUserSelf{}},
		&tg.MessagesRequestAppWebViewRequest{Peer: peer},
		&tg.MessagesGetBotAppRequest{},
		&tg.MessagesStartBotRequest{Bot: &tg.InputUserSelf{}, Peer: peer},
		&tg.MessagesSendVoteRequest{Peer: peer},
		&tg.MessagesSaveDraftRequest{Peer: peer},
		&tg.MessagesMarkDialogUnreadRequest{},
		&tg.MessagesToggleDialogPinRequest{},
		&tg.MessagesReportSpamRequest{Peer: peer},
		&tg.ContactsAddContactRequest{ID: &tg.InputUserSelf{}},
		&tg.ContactsBlockRequest{ID: peer},
		&tg.UploadSaveFilePartRequest{},
		&tg.PhotosUploadProfilePhotoRequest{},
		&tg.ChannelsInviteToChannelRequest{Channel: channel},
		&tg.AuthSendCodeRequest{},
		&tg.AuthLogOutRequest{},
		&tg.AuthAcceptLoginTokenRequest{},
		&tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: &tg.MessagesSendMessageRequest{Peer: peer}},
		&tg.InitConnectionRequest{Query: &tg.InvokeWithoutUpdatesRequest{Query: &tg.MessagesReadHistoryRequest{Peer: peer}}},
		&tg.InvokeWithLayerRequest{Layer: tg.Layer},
	}
	for _, r := range writes {
		t.Run(MethodName(r), func(t *testing.T) {
			err := Check(r, true, false)
			var be *BlockedError
			if !errors.As(err, &be) {
				t.Fatalf("write not blocked: %v", err)
			}
		})
	}
}

func TestAuthOnlyInAuthMode(t *testing.T) {
	auth := []bin.Object{
		&tg.AuthSendCodeRequest{},
		&tg.AuthResendCodeRequest{},
		&tg.AuthSignInRequest{},
		&tg.AuthCheckPasswordRequest{},
		&tg.AuthExportLoginTokenRequest{},
		&tg.AuthImportLoginTokenRequest{},
		&tg.AuthLogOutRequest{},
	}
	for _, r := range auth {
		if err := Check(r, true, true); err != nil {
			t.Errorf("%s blocked in auth mode: %v", MethodName(r), err)
		}
		if err := Check(r, true, false); err == nil {
			t.Errorf("%s allowed outside auth mode", MethodName(r))
		}
	}
	if err := Check(&tg.MessagesSendMessageRequest{Peer: peer}, true, true); err == nil {
		t.Error("auth mode let a send through")
	}
	if err := Check(&tg.AuthSignUpRequest{}, true, true); err == nil {
		t.Error("auth mode allowed signUp")
	}
}

func TestAcceptLoginTokenAlwaysDenied(t *testing.T) {
	for _, ro := range []bool{true, false} {
		for _, auth := range []bool{true, false} {
			err := Check(&tg.AuthAcceptLoginTokenRequest{}, ro, auth)
			var be *BlockedError
			if !errors.As(err, &be) || !be.Always {
				t.Fatalf("readonly=%v auth=%v: %v", ro, auth, err)
			}
			wrapped := &tg.InvokeWithLayerRequest{Query: &tg.AuthAcceptLoginTokenRequest{}}
			if Check(wrapped, ro, auth) == nil {
				t.Fatalf("wrapped acceptLoginToken allowed (readonly=%v)", ro)
			}
		}
	}
}

func TestWritesAllowedWhenNotReadOnly(t *testing.T) {
	for _, r := range []bin.Object{
		&tg.MessagesSendMessageRequest{Peer: peer},
		&tg.MessagesSendMediaRequest{Peer: peer},
		&tg.UploadSaveFilePartRequest{},
	} {
		if err := Check(r, false, false); err != nil {
			t.Errorf("%s blocked with read-only off: %v", MethodName(r), err)
		}
	}
}

type untyped struct{}

func (untyped) Encode(*bin.Buffer) error { return nil }

func TestUnknownTypeBlocked(t *testing.T) {
	if Check(untyped{}, true, false) == nil {
		t.Fatal("request without TL name allowed")
	}
}

func TestAllowedNames(t *testing.T) {
	cases := []struct {
		name string
		auth bool
		want bool
	}{
		{"messages.getHistory", false, true},
		{"contacts.search", false, true},
		{"messages.search", false, true},
		{"messages.searchGlobal", false, true},
		{"contacts.resolveUsername", false, true},
		{"account.checkUsername", false, true},
		{"messages.getWebPagePreview", false, true},
		{"ping", false, true},
		{"ping_delay_disconnect", false, true},
		{"messages.getaway", false, false},
		{"messages.gets", false, false},
		{"messages.checker", false, false},
		{"messages.sendMessage", false, false},
		{"messages.requestWebView", false, false},
		{"messages.getAttachMenuBots", false, false},
		{"messages.getBotCallbackAnswer", true, false},
		{"auth.checkPassword", false, false},
		{"auth.checkPassword", true, true},
		{"auth.acceptLoginToken", true, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := Allowed(c.name, c.auth); got != c.want {
			t.Errorf("Allowed(%q, auth=%v) = %v, want %v", c.name, c.auth, got, c.want)
		}
	}
}

type countingInvoker struct{ calls int }

func (c *countingInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	c.calls++
	return nil
}

func TestMiddleware(t *testing.T) {
	next := &countingInvoker{}
	readOnly := true
	inv := Middleware(func() bool { return readOnly }, false).Handle(next)
	ctx := context.Background()

	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: peer}, nil); err == nil {
		t.Fatal("send passed the middleware")
	}
	if next.calls != 0 {
		t.Fatal("blocked request reached the network layer")
	}
	if err := inv.Invoke(ctx, &tg.MessagesGetHistoryRequest{Peer: peer}, nil); err != nil {
		t.Fatal(err)
	}
	readOnly = false
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: peer}, nil); err != nil {
		t.Fatal(err)
	}
	if next.calls != 2 {
		t.Fatalf("calls = %d, want 2", next.calls)
	}
}

func TestAllowOnce(t *testing.T) {
	next := &countingInvoker{}
	inv := Middleware(func() bool { return true }, false).Handle(next)
	ctx := AllowOnce(context.Background(), MethodSendMessage)

	for _, req := range []bin.Encoder{
		&tg.MessagesReadHistoryRequest{Peer: peer},
		&tg.MessagesSendMediaRequest{Peer: peer},
		&tg.AuthAcceptLoginTokenRequest{},
		&tg.InvokeWithLayerRequest{Query: &tg.MessagesSendMessageRequest{Peer: peer}},
	} {
		if err := inv.Invoke(ctx, req, nil); err == nil {
			t.Fatalf("%T passed with a sendMessage grant", req)
		}
	}
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: peer}, nil); err != nil {
		t.Fatalf("granted send blocked: %v", err)
	}
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: peer}, nil); err == nil {
		t.Fatal("grant allowed a second send")
	}
	if err := inv.Invoke(context.Background(), &tg.MessagesSendMessageRequest{Peer: peer}, nil); err == nil {
		t.Fatal("send passed without a grant")
	}
	if next.calls != 1 {
		t.Fatalf("calls = %d, want 1", next.calls)
	}
}
