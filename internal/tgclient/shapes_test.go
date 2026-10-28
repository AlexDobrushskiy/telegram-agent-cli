package tgclient

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

func TestMarkedIDRoundTrip(t *testing.T) {
	cases := []struct {
		kind Kind
		raw  int64
		want int64
	}{
		{KindUser, 12345, 12345},
		{KindChat, 678, -678},
		{KindChannel, 1234567890, -1001234567890},
	}
	for _, c := range cases {
		got := MarkedID(c.kind, c.raw)
		if got != c.want {
			t.Errorf("MarkedID(%s, %d) = %d, want %d", c.kind, c.raw, got, c.want)
		}
		k, r := SplitMarked(got)
		if k != c.kind || r != c.raw {
			t.Errorf("SplitMarked(%d) = %s %d", got, k, r)
		}
	}
	if PeerMarkedID(&tg.PeerChannel{ChannelID: 5}) != -1000000000005 {
		t.Error("PeerMarkedID channel")
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in    string
		kind  RefKind
		id    int64
		value string
	}{
		{"12345", RefID, 12345, ""},
		{" -1001234567890 ", RefID, -1001234567890, ""},
		{"-42", RefID, -42, ""},
		{"@example_user", RefUsername, 0, "example_user"},
		{"t.me/example_channel", RefUsername, 0, "example_channel"},
		{"https://t.me/example_channel/123", RefUsername, 0, "example_channel"},
		{"https://www.telegram.me/s/example_channel", RefUsername, 0, "example_channel"},
		{"tg://resolve?domain=example_bot", RefUsername, 0, "example_bot"},
		{"https://t.me/c/1234567890/55", RefID, -1001234567890, ""},
		{"tg://privatepost?channel=77&post=1", RefID, -1000000000077, ""},
		{"https://t.me/+AbCdEf123_x", RefInvite, 0, "AbCdEf123_x"},
		{"t.me/joinchat/AbCdEf", RefInvite, 0, "AbCdEf"},
		{"https://t.me/+15550000000", RefPhone, 0, "15550000000"},
		{"+1 (555) 000-0000", RefPhone, 0, "15550000000"},
		{"Book Club", RefName, 0, "Book Club"},
		{"+", RefName, 0, "+"},
	}
	for _, c := range cases {
		r, err := ParseRef(c.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", c.in, err)
			continue
		}
		if r.Kind != c.kind || r.ID != c.id || r.Value != c.value {
			t.Errorf("ParseRef(%q) = %+v, want kind=%d id=%d value=%q", c.in, r, c.kind, c.id, c.value)
		}
	}
	for _, bad := range []string{"", "@", "@a", "https://t.me/", "t.me/c/abc", "99999999999999999999"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func dlg(id int64, name string) Dialog {
	k, raw := SplitMarked(id)
	return Dialog{ID: id, Name: name, Type: "user", Peer: Peer{ID: id, Name: name, Type: "user", Kind: k, RawID: raw}}
}

func TestPickByName(t *testing.T) {
	ds := []Dialog{dlg(1, "Book Club"), dlg(2, "Book Club Archive"), dlg(3, "Chess"), dlg(4, "chess fans")}

	p, err := PickByName("book club", ds)
	if err != nil || p.ID != 1 {
		t.Fatalf("exact match should win: %v %v", p, err)
	}
	p, err = PickByName("archive", ds)
	if err != nil || p.ID != 2 {
		t.Fatalf("single substring: %v %v", p, err)
	}
	if _, err = PickByName("nobody", ds); output.As(err).Code != output.CodeNotFound || output.As(err).Exit != output.ExitNotFound {
		t.Fatalf("want not_found, got %v", err)
	}
	_, err = PickByName("book", ds)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != output.CodeAmbiguous || oe.Exit != output.ExitNotFound {
		t.Fatalf("want ambiguous, got %v", err)
	}
	if !strings.Contains(oe.Message, "Book Club (1)") || len(oe.Details["candidates"].([]ChatRef)) != 2 {
		t.Fatalf("candidates missing: %+v", oe)
	}
	p, err = PickByName("CHESS", ds)
	if err != nil || p.ID != 3 {
		t.Fatalf("case-insensitive exact: %v %v", p, err)
	}
}

func testEntities() *entities {
	return newEntities(
		[]tg.UserClass{
			&tg.User{ID: 10, FirstName: "Ann", LastName: "Example", Username: "ann_example", AccessHash: 1},
			&tg.User{ID: 11, FirstName: "Me", Self: true},
			&tg.User{ID: 12, FirstName: "Helper", Bot: true, Usernames: []tg.Username{{Username: "helper_bot", Active: true}}},
		},
		[]tg.ChatClass{
			&tg.Chat{ID: 20, Title: "Small Group"},
			&tg.Channel{ID: 30, Title: "News", Broadcast: true, AccessHash: 3},
			&tg.Channel{ID: 31, Title: "Big Group", Megagroup: true},
			&tg.Channel{ID: 32, Title: "Forum", Megagroup: true, Forum: true},
		},
	)
}

func TestEntityPeers(t *testing.T) {
	e := testEntities()
	want := map[int64]string{10: "user", 12: "bot", -20: "group", -1000000000030: "channel", -1000000000031: "supergroup", -1000000000032: "forum"}
	for id, typ := range want {
		if e.peers[id].Type != typ {
			t.Errorf("%d type = %q, want %q", id, e.peers[id].Type, typ)
		}
	}
	if e.peers[10].Name != "Ann Example" || e.peers[12].Username != "helper_bot" {
		t.Errorf("names/usernames: %+v %+v", e.peers[10], e.peers[12])
	}
}

func TestToMessage(t *testing.T) {
	e := testEntities()
	date := int(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix())

	// Incoming 1:1 message without from_id: sender is the peer.
	m, ok := e.toMessage(&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 10}, Date: date, Message: "hi",
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 4}}, 11)
	if !ok || m.ChatID != 10 || *m.ChatName != "Ann Example" || *m.SenderID != 10 || m.FromMe ||
		m.Date != "2026-01-02T03:04:05Z" || m.ReplyTo != 4 {
		t.Fatalf("incoming: %+v", m)
	}
	// Outgoing 1:1 message: sender is self.
	m, _ = e.toMessage(&tg.Message{ID: 6, Out: true, PeerID: &tg.PeerUser{UserID: 10}, Date: date}, 11)
	if !m.FromMe || *m.SenderID != 11 || *m.SenderName != "Me" {
		t.Fatalf("outgoing: %+v", m)
	}
	// Channel post: sender is the channel; views and forward header kept.
	views := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 30}, Date: date, Post: true}
	views.SetViews(42)
	views.SetFwdFrom(tg.MessageFwdHeader{FromName: "Someone"})
	m, _ = e.toMessage(views, 11)
	if m.ChatID != -1000000000030 || *m.SenderID != -1000000000030 || *m.Views != 42 || m.ForwardedFrom != "Someone" {
		t.Fatalf("channel post: %+v", m)
	}
	// Group message with from_id and an unknown sender name.
	m, _ = e.toMessage(&tg.Message{ID: 8, PeerID: &tg.PeerChat{ChatID: 20}, FromID: &tg.PeerUser{UserID: 99}, Date: date, Mentioned: true}, 11)
	if m.ChatID != -20 || *m.SenderID != 99 || m.SenderName != nil || !m.Mentioned {
		t.Fatalf("group: %+v", m)
	}
	// Service message.
	m, _ = e.toMessage(&tg.MessageService{ID: 9, PeerID: &tg.PeerChat{ChatID: 20}, FromID: &tg.PeerUser{UserID: 10},
		Date: date, Action: &tg.MessageActionChatAddUser{}}, 11)
	if m.Service != "ChatAddUser" || m.Text != "" {
		t.Fatalf("service: %+v", m)
	}
	if _, ok := e.toMessage(&tg.MessageEmpty{ID: 1}, 11); ok {
		t.Fatal("empty message converted")
	}
	// JSON keeps the prototype's keys, including nulls.
	b, _ := json.Marshal(Message{ID: 1, ChatID: 2, Date: "x"})
	for _, k := range []string{`"chat_name":null`, `"sender_id":null`, `"sender_name":null`, `"from_me":false`, `"text":""`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("message JSON lacks %s: %s", k, b)
		}
	}
}

func doc(mime string, attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, MimeType: mime, Size: 1000, Attributes: attrs}}
}

func TestMediaInfo(t *testing.T) {
	cases := []struct {
		media tg.MessageMediaClass
		want  Media
	}{
		{&tg.MessageMediaPhoto{Photo: &tg.Photo{Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "m", Size: 100}, &tg.PhotoSizeProgressive{Type: "y", Sizes: []int{10, 500}}}}},
			Media{Type: "photo", Mime: "image/jpeg", Size: 500}},
		{doc("audio/ogg", &tg.DocumentAttributeAudio{Voice: true}), Media{Type: "voice", Mime: "audio/ogg", Size: 1000}},
		{doc("video/mp4", &tg.DocumentAttributeVideo{RoundMessage: true}), Media{Type: "video_note", Mime: "video/mp4", Size: 1000}},
		{doc("video/mp4", &tg.DocumentAttributeVideo{}, &tg.DocumentAttributeAnimated{}), Media{Type: "gif", Mime: "video/mp4", Size: 1000}},
		{doc("video/webm", &tg.DocumentAttributeVideo{}, &tg.DocumentAttributeSticker{}), Media{Type: "sticker", Mime: "video/webm", Size: 1000}},
		{doc("video/mp4", &tg.DocumentAttributeVideo{}, &tg.DocumentAttributeFilename{FileName: "clip.mp4"}),
			Media{Type: "video", Name: "clip.mp4", Mime: "video/mp4", Size: 1000}},
		{doc("audio/mpeg", &tg.DocumentAttributeAudio{}), Media{Type: "audio", Mime: "audio/mpeg", Size: 1000}},
		{doc("application/pdf", &tg.DocumentAttributeFilename{FileName: "a.pdf"}), Media{Type: "document", Name: "a.pdf", Mime: "application/pdf", Size: 1000}},
		{&tg.MessageMediaWebPage{Webpage: &tg.WebPage{URL: "https://example.com", Title: "Example"}},
			Media{Type: "webpage", URL: "https://example.com", Title: "Example"}},
		{&tg.MessageMediaPoll{Poll: tg.Poll{Question: tg.TextWithEntities{Text: "Lunch?"}}}, Media{Type: "poll", Question: "Lunch?"}},
		{&tg.MessageMediaGeo{Geo: &tg.GeoPointEmpty{}}, Media{Type: "geo"}},
		{&tg.MessageMediaGeoLive{Geo: &tg.GeoPointEmpty{}}, Media{Type: "geolive"}},
	}
	for _, c := range cases {
		got := MediaInfo(c.media)
		if got == nil || *got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.media.TypeName(), got, c.want)
		}
	}
	if MediaInfo(nil) != nil || MediaInfo(&tg.MessageMediaEmpty{}) != nil {
		t.Error("empty media should be nil")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"report.pdf":         "report.pdf",
		"../../etc/passwd":   "passwd",
		"..\\..\\evil.exe":   "evil.exe",
		".hidden":            "hidden",
		"a\x00b\nc.txt":      "abc.txt",
		"":                   "",
		"..":                 "",
		"/":                  "",
		"dir/inner/name.ogg": "name.ogg",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("я", 300) + ".txt"
	if got := SanitizeFilename(long); len(got) > 200 || !strings.HasSuffix(got, ".txt") {
		t.Errorf("long name: %d bytes %q", len(got), got[len(got)-8:])
	}
	d := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := MediaFilename(&Media{Type: "voice", Mime: "audio/ogg"}, d); got != "voice_2026-01-02_03-04-05.oga" {
		t.Errorf("generated name %q", got)
	}
}

func TestPeerCache(t *testing.T) {
	path := t.TempDir() + "/peers.json"
	c := LoadPeerCache(path)
	c.Put(Peer{ID: 10, Kind: KindUser, RawID: 10, AccessHash: 111, Name: "Ann", Type: "user"}, "15550000000", false)
	// A min access hash must not replace a full one.
	c.Put(Peer{ID: 10, Kind: KindUser, RawID: 10, AccessHash: 999, Name: "Ann B", Type: "user"}, "", true)
	// A min-only peer is not cached at all.
	c.Put(Peer{ID: 11, Kind: KindUser, RawID: 11, AccessHash: 5}, "", true)
	var wrote []byte
	if err := c.Save(func(_ string, b []byte) error { wrote = b; return nil }); err != nil {
		t.Fatal(err)
	}
	p, ok := c.Get(10)
	if !ok || p.AccessHash != 111 || p.Name != "Ann B" {
		t.Fatalf("cache entry %+v", p)
	}
	if _, ok := c.Get(11); ok {
		t.Fatal("min-only peer cached")
	}
	if p, ok := c.FindPhone("15550000000"); !ok || p.ID != 10 {
		t.Fatal("phone lookup")
	}
	if wrote == nil {
		t.Fatal("dirty cache not saved")
	}
	wrote = nil
	_ = c.Save(func(_ string, b []byte) error { wrote = b; return nil })
	if wrote != nil {
		t.Fatal("clean cache saved again")
	}
}
