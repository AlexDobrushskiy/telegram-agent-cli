package guard

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
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
		&tg.UploadGetFileHashesRequest{Location: &tg.InputDocumentFileLocation{}},
		&tg.UploadGetCDNFileRequest{},
		&tg.UploadGetCDNFileHashesRequest{},
		&tg.MessagesGetChatsRequest{ID: []int64{5}},
		&tg.ChannelsGetChannelsRequest{ID: []tg.InputChannelClass{channel}},
		&tg.HelpGetCDNConfigRequest{},
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
		// Read-named writes that a get/search/resolve/check prefix rule let through.
		&tg.PaymentsResolveStarGiftOfferRequest{},
		&tg.EphemeralGetCallbackAnswerRequest{},
		&tg.ContactsGetLocatedRequest{SelfExpires: 60},
		&tg.ChannelsSearchPostsRequest{AllowPaidStars: 10},
		&tg.MessagesCheckURLAuthMatchCodeRequest{},
		// Reads the CLI never makes are off the allowlist too.
		&tg.UpdatesGetStateRequest{},
		&tg.MessagesGetMessagesViewsRequest{Peer: peer, ID: []int{1}},
		&tg.MessagesGetWebPagePreviewRequest{},
		&tg.AccountCheckUsernameRequest{},
		&tg.AccountGetPasswordRequest{},
		// Allowed methods with write-like arguments.
		&tg.ContactsResolveUsernameRequest{Username: "example", Referer: "ref"},
		&tg.InvokeWithoutUpdatesRequest{Query: &tg.ContactsResolveUsernameRequest{Username: "x", Referer: "ref"}},
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
		&tg.AccountGetPasswordRequest{},
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
		{"ping", false, true},
		{"ping_delay_disconnect", false, true},
		{"account.checkUsername", false, false},
		{"messages.getWebPagePreview", false, false},
		{"payments.resolveStarGiftOffer", false, false},
		{"payments.resolveStarGiftOffer", true, false},
		{"messages.getaway", false, false},
		{"messages.sendMessage", false, false},
		{"messages.requestWebView", false, false},
		{"messages.getAttachMenuBots", false, false},
		{"messages.getBotCallbackAnswer", true, false},
		{"auth.checkPassword", false, false},
		{"auth.checkPassword", true, true},
		{"account.getPassword", false, false},
		{"account.getPassword", true, true},
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
	if err := inv.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: peer, AllowPaidStars: 5}, nil); err == nil {
		t.Fatal("grant let a paid send through")
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

// wantRead is the full read-only allowlist. Changing the guard or upgrading gotd
// must not change it without updating this list on purpose.
var wantRead = []string{
	"auth.exportAuthorization", "auth.importAuthorization",
	"channels.getChannels", "channels.getFullChannel", "channels.getMessages",
	"contacts.getContacts", "contacts.resolvePhone", "contacts.resolveUsername", "contacts.search",
	"help.getCdnConfig", "help.getConfig",
	"messages.checkChatInvite", "messages.getChats", "messages.getDialogs", "messages.getFullChat",
	"messages.getHistory", "messages.getMessages", "messages.getReplies", "messages.getUnreadMentions",
	"messages.search", "messages.searchGlobal",
	"upload.getCdnFile", "upload.getCdnFileHashes", "upload.getFile", "upload.getFileHashes", "upload.reuploadCdnFile",
	"users.getFullUser", "users.getUsers",
}

// wantAuthExtra is what auth mode adds.
var wantAuthExtra = []string{
	"account.getPassword", "auth.checkPassword", "auth.exportLoginToken", "auth.importLoginToken",
	"auth.logOut", "auth.resendCode", "auth.sendCode", "auth.signIn",
}

// allowedRequests walks every request type in gotd's tg package.
func allowedRequests(t *testing.T, authMode bool) (allowed []string, total int) {
	t.Helper()
	for _, ctor := range tg.TypesConstructorMap() {
		obj := ctor()
		if !strings.HasSuffix(reflect.TypeOf(obj).Elem().Name(), "Request") {
			continue
		}
		total++
		if Check(obj, true, authMode) == nil {
			allowed = append(allowed, MethodName(obj))
		}
	}
	sort.Strings(allowed)
	return allowed, total
}

func TestAllowlistIsPinned(t *testing.T) {
	got, total := allowedRequests(t, false)
	if total < 500 {
		t.Fatalf("walked only %d request types: the registry walk is broken", total)
	}
	if strings.Join(got, ",") != strings.Join(wantRead, ",") {
		t.Fatalf("read-only allowlist changed.\n got: %v\nwant: %v", got, wantRead)
	}
	gotAuth, _ := allowedRequests(t, true)
	want := append(append([]string{}, wantRead...), wantAuthExtra...)
	sort.Strings(want)
	if strings.Join(gotAuth, ",") != strings.Join(want, ",") {
		t.Fatalf("auth-mode allowlist changed.\n got: %v\nwant: %v", gotAuth, want)
	}
	// AllowedMethods = the walk plus ping/ping_delay_disconnect (gotd's mt package, not tg).
	var listed []string
	for _, m := range AllowedMethods(false) {
		if m != "ping" && m != "ping_delay_disconnect" {
			listed = append(listed, m)
		}
	}
	if strings.Join(listed, ",") != strings.Join(got, ",") {
		t.Fatalf("AllowedMethods %v disagrees with the walk %v", listed, got)
	}
}

func TestRiskyArguments(t *testing.T) {
	cases := map[string]any{
		"SelfExpires":    &tg.ContactsGetLocatedRequest{SelfExpires: 60},
		"AllowPaidStars": &tg.ChannelsSearchPostsRequest{AllowPaidStars: 1},
		"Increment":      &tg.MessagesGetMessagesViewsRequest{Increment: true},
		"Referer":        &tg.ContactsResolveUsernameRequest{Referer: "x"},
	}
	for want, req := range cases {
		if got := riskyArgument(req); got != want {
			t.Errorf("%T: riskyArgument = %q, want %q", req, got, want)
		}
	}
	if riskyArgument(&tg.ChannelsSearchPostsRequest{}) != "" {
		t.Error("zero values flagged")
	}
	// No allowlisted method may grow such an argument unnoticed.
	for _, ctor := range tg.TypesConstructorMap() {
		obj := ctor()
		if !Allowed(MethodName(obj), true) {
			continue
		}
		rt := reflect.TypeOf(obj).Elem()
		for i := 0; i < rt.NumField(); i++ {
			for _, r := range riskyFields {
				if strings.Contains(rt.Field(i).Name, r) {
					t.Errorf("allowed %s has %s: review it", MethodName(obj), rt.Field(i).Name)
				}
			}
		}
	}
}
