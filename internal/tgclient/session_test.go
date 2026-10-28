package tgclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// fakeServer answers MTProto calls in-process, emulating Telegram's paging rules.
type fakeServer struct {
	t        *testing.T
	mu       sync.Mutex
	seen     []string
	users    []tg.UserClass
	chats    []tg.ChatClass
	msgs     map[int64][]*tg.Message // by marked chat id
	dialogs  []*tg.Dialog
	handlers map[string]func(bin.Encoder) (bin.Encoder, error)
}

const selfID = 1

var base = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func msgDate(id int) int { return int(base.Add(time.Duration(id) * time.Minute).Unix()) }

func newServer(t *testing.T) *fakeServer {
	s := &fakeServer{t: t, msgs: map[int64][]*tg.Message{}, handlers: map[string]func(bin.Encoder) (bin.Encoder, error){}}
	s.users = []tg.UserClass{&tg.User{ID: selfID, Self: true, FirstName: "Owner", AccessHash: 1}}
	return s
}

func (s *fakeServer) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	name := guard.MethodName(in)
	s.mu.Lock()
	s.seen = append(s.seen, name)
	h := s.handlers[name]
	s.mu.Unlock()
	var resp bin.Encoder
	var err error
	if h != nil {
		resp, err = h(in)
	} else {
		resp, err = s.builtin(in)
	}
	if err != nil {
		return err
	}
	var b bin.Buffer
	if err := resp.Encode(&b); err != nil {
		s.t.Fatalf("encode %s response: %v", name, err)
	}
	return out.Decode(&b)
}

func (s *fakeServer) calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.seen {
		if x == name {
			n++
		}
	}
	return n
}

// window applies offset_id/offset_date + add_offset/limit to a newest-first list.
func window(list []*tg.Message, offsetID, offsetDate, addOffset, limit int) []tg.MessageClass {
	start := 0
	switch {
	case offsetID > 0:
		start = sort.Search(len(list), func(i int) bool { return list[i].ID < offsetID })
	case offsetDate > 0:
		start = sort.Search(len(list), func(i int) bool { return list[i].Date < offsetDate })
	}
	from := max(0, min(len(list), start+addOffset))
	to := max(0, min(len(list), from+limit))
	out := make([]tg.MessageClass, 0, to-from)
	for _, m := range list[from:to] {
		out = append(out, m)
	}
	return out
}

func (s *fakeServer) newestFirst(chat int64, keep func(*tg.Message) bool) []*tg.Message {
	var list []*tg.Message
	for _, m := range s.msgs[chat] {
		if keep == nil || keep(m) {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return list
}

func (s *fakeServer) slice(msgs []tg.MessageClass, count int) *tg.MessagesMessagesSlice {
	return &tg.MessagesMessagesSlice{Count: count, Messages: msgs, Users: s.users, Chats: s.chats}
}

func (s *fakeServer) builtin(in bin.Encoder) (bin.Encoder, error) {
	switch r := in.(type) {
	case *tg.MessagesGetHistoryRequest:
		list := s.newestFirst(PeerMarkedID(inputToPeer(r.Peer)), func(m *tg.Message) bool {
			return (r.MinID == 0 || m.ID > r.MinID) && (r.MaxID == 0 || m.ID < r.MaxID)
		})
		return s.slice(window(list, r.OffsetID, r.OffsetDate, r.AddOffset, r.Limit), len(list)), nil
	case *tg.MessagesSearchRequest:
		_, mentions := r.Filter.(*tg.InputMessagesFilterMyMentions)
		list := s.newestFirst(PeerMarkedID(inputToPeer(r.Peer)), func(m *tg.Message) bool {
			if mentions && !m.Mentioned {
				return false
			}
			if r.FromID != nil && PeerMarkedID(m.FromID) != PeerMarkedID(inputToPeer(r.FromID)) {
				return false
			}
			return strings.Contains(m.Message, r.Q) && (r.MaxDate == 0 || m.Date < r.MaxDate) && m.Date >= r.MinDate
		})
		return s.slice(window(list, r.OffsetID, 0, r.AddOffset, r.Limit), len(list)), nil
	case *tg.MessagesSearchGlobalRequest:
		var all []tg.MessageClass
		for chat := range s.msgs {
			for _, m := range s.newestFirst(chat, func(m *tg.Message) bool { return strings.Contains(m.Message, r.Q) }) {
				all = append(all, m)
			}
		}
		return &tg.MessagesMessages{Messages: all, Users: s.users, Chats: s.chats}, nil
	case *tg.MessagesGetUnreadMentionsRequest:
		list := s.newestFirst(PeerMarkedID(inputToPeer(r.Peer)), func(m *tg.Message) bool { return m.Mentioned })
		return &tg.MessagesMessages{Messages: window(list, r.OffsetID, 0, r.AddOffset, r.Limit), Users: s.users, Chats: s.chats}, nil
	case *tg.MessagesGetDialogsRequest:
		var list []tg.DialogClass
		var top []tg.MessageClass
		started := r.OffsetID == 0 && r.OffsetDate == 0
		offset := PeerMarkedID(inputToPeer(r.OffsetPeer))
		for _, d := range s.dialogs {
			if r.ExcludePinned && d.Pinned {
				continue
			}
			if !started {
				started = PeerMarkedID(d.Peer) == offset
				continue
			}
			if len(list) == r.Limit {
				break
			}
			list = append(list, d)
			for _, m := range s.msgs[PeerMarkedID(d.Peer)] {
				if m.ID == d.TopMessage {
					top = append(top, m)
				}
			}
		}
		return &tg.MessagesDialogsSlice{Count: len(s.dialogs), Dialogs: list, Messages: top, Users: s.users, Chats: s.chats}, nil
	case *tg.MessagesGetMessagesRequest, *tg.ChannelsGetMessagesRequest:
		var ids []tg.InputMessageClass
		if v, ok := r.(*tg.MessagesGetMessagesRequest); ok {
			ids = v.ID
		} else {
			ids = r.(*tg.ChannelsGetMessagesRequest).ID
		}
		var out []tg.MessageClass
		for _, im := range ids {
			want := im.(*tg.InputMessageID).ID
			found := false
			for _, list := range s.msgs {
				for _, m := range list {
					if m.ID == want && !found {
						out, found = append(out, m), true
					}
				}
			}
			if !found {
				out = append(out, &tg.MessageEmpty{ID: want})
			}
		}
		return &tg.MessagesMessages{Messages: out, Users: s.users, Chats: s.chats}, nil
	case *tg.UsersGetUsersRequest:
		return &tg.UserClassVector{Elems: s.users}, nil
	}
	s.t.Fatalf("unhandled request %s", guard.MethodName(in))
	return nil, nil
}

func inputToPeer(p tg.InputPeerClass) tg.PeerClass {
	switch v := p.(type) {
	case *tg.InputPeerUser:
		return &tg.PeerUser{UserID: v.UserID}
	case *tg.InputPeerChat:
		return &tg.PeerChat{ChatID: v.ChatID}
	case *tg.InputPeerChannel:
		return &tg.PeerChannel{ChannelID: v.ChannelID}
	case *tg.InputPeerSelf:
		return &tg.PeerUser{UserID: selfID}
	}
	return nil
}

// fill adds a 1:1 chat with user 10 holding messages 1..n.
func (s *fakeServer) fill(n int) *Peer {
	s.users = append(s.users, &tg.User{ID: 10, FirstName: "Ann", AccessHash: 77})
	for id := 1; id <= n; id++ {
		m := &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: 10}, Date: msgDate(id), Message: fmt.Sprintf("msg %d", id)}
		if id%2 == 0 {
			m.Out = true
		} else {
			m.FromID = &tg.PeerUser{UserID: 10}
		}
		m.Mentioned = id%50 == 0
		s.msgs[10] = append(s.msgs[10], m)
	}
	return &Peer{ID: 10, Kind: KindUser, RawID: 10, AccessHash: 77, Name: "Ann", Type: "user"}
}

// session builds a Session whose client goes through the real guard middleware.
func (s *fakeServer) session(readOnly bool) *Session {
	inv := guard.Middleware(func() bool { return readOnly }, false).Handle(s)
	cache := LoadPeerCache(filepath.Join(s.t.TempDir(), "peers.json"))
	return NewSession(tg.NewClient(inv), cache, s.users[0].(*tg.User))
}

// assertNoWrites fails if anything that isn't a read reached the server.
func (s *fakeServer) assertNoWrites() {
	s.t.Helper()
	for _, name := range s.seen {
		if !guard.Allowed(name, false) {
			s.t.Errorf("non-read %s reached the server", name)
		}
	}
}

func ids(msgs []Message) []int {
	out := make([]int, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func seq(from, to int) []int {
	var out []int
	if from <= to {
		for i := from; i <= to; i++ {
			out = append(out, i)
		}
	} else {
		for i := from; i >= to; i-- {
			out = append(out, i)
		}
	}
	return out
}

func TestHistoryPaging(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		q    HistoryQuery
		want []int
	}{
		{"newest first across chunks", HistoryQuery{Limit: 250}, seq(250, 1)},
		{"more than exists", HistoryQuery{Limit: 400}, seq(250, 1)},
		{"offset id", HistoryQuery{Limit: 30, OffsetID: 100}, seq(99, 70)},
		{"min id stops", HistoryQuery{Limit: 50, MinID: 240}, seq(250, 241)},
		{"max id", HistoryQuery{Limit: 5, MaxID: 50}, seq(49, 45)},
		{"before date", HistoryQuery{Limit: 3, OffsetDate: time.Unix(int64(msgDate(100)), 0)}, seq(99, 97)},
		{"reverse from min id", HistoryQuery{Limit: 5, MinID: 100, Reverse: true}, seq(101, 105)},
		{"reverse after date", HistoryQuery{Limit: 10, Reverse: true, OffsetDate: time.Unix(int64(msgDate(200))+30, 0)}, seq(201, 210)},
		{"reverse across chunks", HistoryQuery{Limit: 150, Reverse: true}, seq(1, 150)},
		{"reverse with max id", HistoryQuery{Limit: 50, Reverse: true, MinID: 10, MaxID: 15}, seq(11, 14)},
		{"context older (includes the message)", HistoryQuery{Limit: 4, OffsetID: 121}, seq(120, 117)},
		{"empty window", HistoryQuery{Limit: 10, MinID: 5, MaxID: 6}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t)
			p := srv.fill(250)
			got, err := srv.session(true).History(ctx, p, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(ids(got)) != fmt.Sprint(c.want) {
				t.Fatalf("ids = %v\nwant %v", ids(got), c.want)
			}
			srv.assertNoWrites()
		})
	}
}

func TestHistoryShapes(t *testing.T) {
	srv := newServer(t)
	p := srv.fill(3)
	got, err := srv.session(true).History(context.Background(), p, HistoryQuery{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	out, in := got[1], got[0] // ids 2 (out) and 3 (in)
	if !out.FromMe || *out.SenderID != selfID || *out.SenderName != "Owner" || *out.ChatName != "Ann" {
		t.Errorf("outgoing %+v", out)
	}
	if in.FromMe || *in.SenderID != 10 || in.Text != "msg 3" {
		t.Errorf("incoming %+v", in)
	}
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	p := srv.fill(250)
	s := srv.session(true)

	got, err := s.Search(ctx, p, SearchQuery{Query: "msg 1", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1+10+100 { // "msg 1", "msg 1x", "msg 1xx"
		t.Fatalf("search hits = %d", len(got))
	}
	// 1:1 chat: --from is filtered client-side, like Telethon.
	from := &Peer{ID: 10, Kind: KindUser, RawID: 10}
	got, err = s.Search(ctx, p, SearchQuery{Query: "msg", From: from, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.FromMe {
			t.Fatalf("--from kept my message %d", m.ID)
		}
	}
	if fmt.Sprint(ids(got)) != fmt.Sprint([]int{249, 247, 245, 243, 241}) {
		t.Fatalf("from ids %v", ids(got))
	}
	// --after cuts older results.
	got, _ = s.Search(ctx, p, SearchQuery{Query: "msg", Limit: 100, After: time.Unix(int64(msgDate(245)), 0)})
	if fmt.Sprint(ids(got)) != fmt.Sprint(seq(250, 245)) {
		t.Fatalf("after ids %v", ids(got))
	}
	// Account-wide.
	got, err = s.Search(ctx, nil, SearchQuery{Query: "msg 25", Limit: 10})
	if err != nil || len(got) != 2 {
		t.Fatalf("global: %v %v", ids(got), err)
	}
	srv.assertNoWrites()
}

func TestMentionsDoNotMarkRead(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	p := srv.fill(250)
	s := srv.session(true)
	for _, unread := range []bool{false, true} {
		got, err := s.Mentions(ctx, p, MentionsQuery{Unread: unread, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(ids(got)) != fmt.Sprint([]int{250, 200, 150, 100, 50}) {
			t.Fatalf("unread=%v ids %v", unread, ids(got))
		}
	}
	if srv.calls("messages.getUnreadMentions") == 0 || srv.calls("messages.search") == 0 {
		t.Fatalf("calls: %v", srv.seen)
	}
	srv.assertNoWrites()
}

func (s *fakeServer) addDialogs(n, pinned int) {
	for i := 0; i < n; i++ {
		uid := int64(1000 + i)
		s.users = append(s.users, &tg.User{ID: uid, FirstName: fmt.Sprintf("Person %03d", i), AccessHash: uid * 3})
		s.msgs[uid] = append(s.msgs[uid], &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: uid}, Date: msgDate(n - i), Message: "hello"})
		d := &tg.Dialog{Peer: &tg.PeerUser{UserID: uid}, TopMessage: 1, UnreadCount: i % 3, Pinned: i < pinned}
		if i%10 == 9 {
			d.SetFolderID(1)
		}
		if i == 5 {
			d.NotifySettings.MuteUntil = int(time.Now().Add(time.Hour).Unix())
		}
		s.dialogs = append(s.dialogs, d)
	}
}

func TestIterDialogsPaging(t *testing.T) {
	srv := newServer(t)
	srv.addDialogs(150, 2)
	s := srv.session(true)
	var got []Dialog
	if err := s.IterDialogs(context.Background(), FolderAll, func(d Dialog) bool {
		got = append(got, d)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 {
		t.Fatalf("dialogs = %d, want 150", len(got))
	}
	seen := map[int64]bool{}
	for _, d := range got {
		if seen[d.ID] {
			t.Fatalf("duplicate dialog %d", d.ID)
		}
		seen[d.ID] = true
	}
	if srv.calls("messages.getDialogs") != 2 {
		t.Fatalf("getDialogs calls = %d", srv.calls("messages.getDialogs"))
	}
	d := got[5]
	if !d.Muted || d.Last == nil || d.Last.Text != "hello" || d.Name != "Person 005" || d.Type != "user" {
		t.Fatalf("dialog shape %+v", d)
	}
	// Stopping early makes no further requests.
	srv2 := newServer(t)
	srv2.addDialogs(150, 0)
	n := 0
	_ = srv2.session(true).IterDialogs(context.Background(), FolderMain, func(Dialog) bool { n++; return n < 3 })
	if n != 3 || srv2.calls("messages.getDialogs") != 1 {
		t.Fatalf("early stop: n=%d calls=%d", n, srv2.calls("messages.getDialogs"))
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	srv.addDialogs(20, 0)
	srv.handlers["contacts.resolveUsername"] = func(in bin.Encoder) (bin.Encoder, error) {
		if in.(*tg.ContactsResolveUsernameRequest).Username != "known_name" {
			return nil, tgerr.New(400, "USERNAME_NOT_OCCUPIED")
		}
		return &tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 55},
			Chats: []tg.ChatClass{&tg.Channel{ID: 55, Title: "Known", AccessHash: 9, Broadcast: true, Photo: &tg.ChatPhotoEmpty{}}}}, nil
	}
	srv.handlers["contacts.getContacts"] = func(bin.Encoder) (bin.Encoder, error) {
		return &tg.ContactsContacts{Users: []tg.UserClass{&tg.User{ID: 77, FirstName: "Phone", Phone: "15550000001", AccessHash: 1}}}, nil
	}
	s := srv.session(true)

	p, err := s.Resolve(ctx, "1007")
	if err != nil || p.Name != "Person 007" || p.AccessHash != 1007*3 {
		t.Fatalf("id via dialogs: %+v %v", p, err)
	}
	before := srv.calls("messages.getDialogs")
	if p, err = s.Resolve(ctx, "1012"); err != nil || p.Name != "Person 012" {
		t.Fatalf("cached id: %+v %v", p, err)
	}
	if srv.calls("messages.getDialogs") != before {
		t.Fatal("cached id walked the dialogs again")
	}
	if _, err = s.Resolve(ctx, "424242"); output.As(err).Code != output.CodeNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if p, err = s.Resolve(ctx, "@known_name"); err != nil || p.ID != -1000000000055 || p.Type != "channel" {
		t.Fatalf("username: %+v %v", p, err)
	}
	if _, err = s.Resolve(ctx, "https://t.me/missing_name"); output.As(err).Code != output.CodeNotFound {
		t.Fatalf("missing username: %v", err)
	}
	if p, err = s.Resolve(ctx, "+1 555 000 0001"); err != nil || p.ID != 77 {
		t.Fatalf("phone: %+v %v", p, err)
	}
	if p, err = s.Resolve(ctx, "person 019"); err != nil || p.ID != 1019 {
		t.Fatalf("name: %+v %v", p, err)
	}
	if _, err = s.Resolve(ctx, "Person 01"); output.As(err).Code != output.CodeAmbiguous {
		t.Fatalf("ambiguous: %v", err)
	}
	srv.assertNoWrites()
}

func TestMessagesByIDFiltersOtherChats(t *testing.T) {
	srv := newServer(t)
	p := srv.fill(10)
	got, err := srv.session(true).MessagesByID(context.Background(), p, []int{3, 5, 999})
	if err != nil || fmt.Sprint(ids(got)) != "[3 5]" {
		t.Fatalf("%v %v", ids(got), err)
	}
}

func TestSendBlockedByGuard(t *testing.T) {
	srv := newServer(t)
	p := srv.fill(1)
	_, err := srv.session(true).SendText(context.Background(), p, "hello", 0)
	var be *guard.BlockedError
	if !errors.As(err, &be) || be.Method != "messages.sendMessage" {
		t.Fatalf("want guard block, got %v", err)
	}
	if srv.calls("messages.sendMessage") != 0 {
		t.Fatal("sendMessage reached the server")
	}
	if oe := output.As(MapError(err)); oe.Code != output.CodeReadOnly || oe.Exit != output.ExitAuth {
		t.Fatalf("mapped %+v", oe)
	}
}

func TestSendParsesUpdates(t *testing.T) {
	srv := newServer(t)
	p := srv.fill(1)
	srv.handlers["messages.sendMessage"] = func(in bin.Encoder) (bin.Encoder, error) {
		r := in.(*tg.MessagesSendMessageRequest)
		if !r.NoWebpage {
			t.Error("link previews should be off")
		}
		if rt, ok := r.ReplyTo.(*tg.InputReplyToMessage); !ok || rt.ReplyToMsgID != 1 {
			t.Errorf("reply_to %+v", r.ReplyTo)
		}
		return &tg.Updates{
			Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{ID: 2, RandomID: r.RandomID},
				&tg.UpdateNewMessage{Message: &tg.Message{ID: 2, Out: true, PeerID: &tg.PeerUser{UserID: 10}, Date: msgDate(2), Message: r.Message}},
			},
			Users: srv.users,
		}, nil
	}
	m, err := srv.session(false).SendText(context.Background(), p, "hello there", 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != 2 || !m.FromMe || m.Text != "hello there" || m.ChatID != 10 {
		t.Fatalf("sent %+v", m)
	}
	srv.handlers["messages.sendMessage"] = func(bin.Encoder) (bin.Encoder, error) {
		return &tg.UpdateShortSentMessage{ID: 3, Date: msgDate(3), Out: true}, nil
	}
	m, err = srv.session(false).SendText(context.Background(), p, "short", 0)
	if err != nil || m.ID != 3 || m.Text != "short" || *m.ChatName != "Ann" {
		t.Fatalf("short sent %+v %v", m, err)
	}
}

func TestDownloadMedia(t *testing.T) {
	srv := newServer(t)
	p := srv.fill(1)
	photo := &tg.Photo{ID: 5, AccessHash: 6, FileReference: []byte{1}, Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "s", Size: 10}, &tg.PhotoSize{Type: "x", Size: 11}}}
	srv.msgs[10][0].Media = &tg.MessageMediaPhoto{Photo: photo}
	content := []byte("photo-bytes")
	expired := true
	srv.handlers["upload.getFile"] = func(in bin.Encoder) (bin.Encoder, error) {
		r := in.(*tg.UploadGetFileRequest)
		loc := r.Location.(*tg.InputPhotoFileLocation)
		if loc.ThumbSize != "x" || loc.ID != 5 {
			t.Errorf("location %+v", loc)
		}
		if expired {
			expired = false
			return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		end := min(int64(len(content)), r.Offset+int64(r.Limit))
		return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: content[min(r.Offset, end):end]}, nil
	}
	dir := filepath.Join(t.TempDir(), "out")
	s := srv.session(true)
	d, err := s.DownloadMedia(context.Background(), p, 1, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d.Path) != "photo_2026-03-01_00-01-00.jpg" || d.Media.Type != "photo" {
		t.Fatalf("download %+v", d)
	}
	b, _ := os.ReadFile(d.Path)
	st, _ := os.Stat(d.Path)
	if !bytes.Equal(b, content) || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %q mode %v", b, st.Mode())
	}
	// Second download does not overwrite.
	expired = false
	d2, err := s.DownloadMedia(context.Background(), p, 1, dir)
	if err != nil || filepath.Base(d2.Path) != "photo_2026-03-01_00-01-00 (1).jpg" {
		t.Fatalf("second download %+v %v", d2, err)
	}
	// A message without media is not_found and leaves no file.
	srv.msgs[10][0] = &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 10}, Date: msgDate(1)}
	if _, err := s.DownloadMedia(context.Background(), p, 1, dir); output.As(err).Code != output.CodeNotFound {
		t.Fatalf("no media: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("files left: %d", len(entries))
	}
	srv.assertNoWrites()
}

// addGroup adds a basic group with message ids 1..n; every mentionEvery-th mentions the owner.
func (s *fakeServer) addGroup(id int64, title string, n, mentionEvery, unreadMentions int, newest int) {
	s.chats = append(s.chats, &tg.Chat{ID: id, Title: title, Photo: &tg.ChatPhotoEmpty{}})
	for i := 1; i <= n; i++ {
		s.msgs[-id] = append(s.msgs[-id], &tg.Message{ID: i, PeerID: &tg.PeerChat{ChatID: id}, FromID: &tg.PeerUser{UserID: 10},
			Date: msgDate(newest - n + i), Message: fmt.Sprintf("%s %d", title, i), Mentioned: i%mentionEvery == 0})
	}
	s.dialogs = append(s.dialogs, &tg.Dialog{Peer: &tg.PeerChat{ChatID: id}, TopMessage: n, UnreadMentionsCount: unreadMentions})
}

func TestMentionsAccountWide(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	srv.users = append(srv.users, &tg.User{ID: 10, FirstName: "Ann", AccessHash: 77})
	// Most recent first: a 1:1 chat (never scanned), then three groups.
	srv.msgs[10] = []*tg.Message{{ID: 1, PeerID: &tg.PeerUser{UserID: 10}, Date: msgDate(500), Message: "dm", Mentioned: true}}
	srv.dialogs = append(srv.dialogs, &tg.Dialog{Peer: &tg.PeerUser{UserID: 10}, TopMessage: 1, UnreadMentionsCount: 1})
	srv.addGroup(20, "alpha", 10, 5, 0, 400) // mentions at ids 5, 10
	srv.addGroup(21, "beta", 10, 3, 2, 300)  // mentions at ids 3, 6, 9
	srv.addGroup(22, "gamma", 10, 2, 0, 200) // only reached with --scan ≥ 3
	srv.handlers["messages.searchGlobal"] = func(bin.Encoder) (bin.Encoder, error) {
		t.Fatal("searchGlobal must not be used for mentions")
		return nil, nil
	}
	s := srv.session(true)

	got, err := s.Mentions(ctx, nil, MentionsQuery{Limit: 4, Scan: 2})
	if err != nil {
		t.Fatal(err)
	}
	var chats []int64
	for _, m := range got {
		chats = append(chats, m.ChatID)
	}
	// Newest first across alpha (ids 10, 5) and beta (9, 6, 3), trimmed to 4.
	if fmt.Sprint(ids(got)) != "[10 5 9 6]" || fmt.Sprint(chats) != "[-20 -20 -21 -21]" {
		t.Fatalf("scan=2: ids %v chats %v", ids(got), chats)
	}
	if n := srv.calls("messages.search"); n != 2 {
		t.Fatalf("searched %d chats, want 2", n)
	}
	if got, _ := s.Mentions(ctx, nil, MentionsQuery{Limit: 100, Scan: 3}); len(got) != 2+3+5 {
		t.Fatalf("scan=3: %d mentions", len(got))
	}

	// Unread: only dialogs with unread mentions (the 1:1 chat and beta), via getUnreadMentions.
	before := srv.calls("messages.search")
	got, err = s.Mentions(ctx, nil, MentionsQuery{Unread: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(got)) != "[1 9 6 3]" || srv.calls("messages.getUnreadMentions") != 2 || srv.calls("messages.search") != before {
		t.Fatalf("unread: ids %v calls %v", ids(got), srv.seen)
	}
	if srv.calls("messages.readMentions") != 0 {
		t.Fatal("mentions were marked read")
	}
	srv.assertNoWrites()
}

func TestMentionsSkipsUnreadableChat(t *testing.T) {
	srv := newServer(t)
	srv.addGroup(20, "alpha", 4, 2, 0, 400)
	srv.addGroup(21, "gone", 4, 2, 0, 300)
	srv.handlers["messages.search"] = func(in bin.Encoder) (bin.Encoder, error) {
		if PeerMarkedID(inputToPeer(in.(*tg.MessagesSearchRequest).Peer)) == -21 {
			return nil, tgerr.New(400, "CHAT_ID_INVALID")
		}
		return srv.builtin(in)
	}
	got, err := srv.session(true).Mentions(context.Background(), nil, MentionsQuery{Limit: 10})
	if err != nil || fmt.Sprint(ids(got)) != "[4 2]" {
		t.Fatalf("%v %v", ids(got), err)
	}
}

func TestDialogArchivedFlag(t *testing.T) {
	srv := newServer(t)
	srv.addDialogs(20, 0) // every 10th has folder_id=1
	s := srv.session(true)
	archived := func(folder Folder) map[int64]bool {
		out := map[int64]bool{}
		if err := s.IterDialogs(context.Background(), folder, func(d Dialog) bool {
			out[d.ID] = d.Archived
			return true
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// All folders: derived from folder_id.
	for id, a := range archived(FolderAll) {
		if want := (id-1000)%10 == 9; a != want {
			t.Errorf("FolderAll %d archived=%v, want %v", id, a, want)
		}
	}
	// Listed from the archive folder: always archived, even without folder_id.
	for id, a := range archived(FolderArchive) {
		if !a {
			t.Errorf("FolderArchive %d archived=false", id)
		}
	}
	if srv.calls("messages.getDialogs") < 2 {
		t.Fatal("expected one listing per folder")
	}
}
