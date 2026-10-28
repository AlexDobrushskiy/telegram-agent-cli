package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
)

type harness struct {
	t        *testing.T
	app      *App
	fake     *tgfake.Fake
	out      *bytes.Buffer
	connects int
	raws     int
}

var t0 = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	t.Setenv(config.EnvReadOnly, "")
	t.Setenv(config.EnvLockWait, "")
	h := &harness{t: t, out: &bytes.Buffer{}}
	h.fake = tgfake.New(tgclient.User{ID: 1, Name: "Owner", Type: "user"})
	user := "ann_example"
	h.fake.AddDialog(tgclient.Dialog{ID: 10, Name: "Ann Example", Type: "user", Username: &user, Unread: 2})
	h.fake.AddDialog(tgclient.Dialog{ID: -20, Name: "Book Club", Type: "group"})
	h.fake.AddDialog(tgclient.Dialog{ID: -21, Name: "Bookworms", Type: "group", Mentions: 1})
	h.fake.AddDialog(tgclient.Dialog{ID: -1000000000030, Name: "News", Type: "channel", Archived: true})
	h.fake.AddDialog(tgclient.Dialog{ID: 12, Name: "Helper", Type: "bot"})
	for id := 1; id <= 60; id++ {
		sender := int64(10)
		if id%2 == 0 {
			sender = 1
		}
		m := tgclient.Message{ID: id, ChatID: 10, FromMe: sender == 1, SenderID: &sender, Text: fmt.Sprintf("note %d", id),
			Time: t0.Add(time.Duration(id) * time.Minute)}
		if id == 7 {
			m.Media = &tgclient.Media{Type: "document", Name: "report.pdf", Mime: "application/pdf"}
		}
		h.fake.AddMessage(m)
	}
	h.fake.AddMessage(tgclient.Message{ID: 3, ChatID: -21, Text: "@owner look", Mentioned: true, Time: t0})

	h.app = &App{
		Paths:  config.Paths{Home: filepath.Join(t.TempDir(), "home")},
		Stdout: h.out, Stderr: io.Discard, Stdin: strings.NewReader(""),
		Connect: func(ctx context.Context, _ tgclient.Options, fn func(context.Context, tgclient.Telegram) error) error {
			h.connects++
			return tgclient.MapError(fn(ctx, h.fake))
		},
		Raw: func(context.Context, tgclient.Options, func(context.Context, *tgclient.Conn) error) error {
			h.raws++
			return errors.New("no network in tests")
		},
		StdinIsTerminal: func() bool { return false },
	}
	h.app.ReadSecret = h.app.readSecret
	return h
}

type envelope struct {
	SchemaVersion string          `json:"schema_version"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Error         *output.Error   `json:"error"`
}

func (h *harness) run(args ...string) (int, envelope) {
	h.t.Helper()
	h.out.Reset()
	code := h.app.Execute(context.Background(), args)
	var env envelope
	if err := json.Unmarshal(h.out.Bytes(), &env); err != nil {
		h.t.Fatalf("%v: stdout is not one JSON envelope: %v\n%s", args, err, h.out.String())
	}
	if env.SchemaVersion != "1" || env.OK != (code == 0) {
		h.t.Fatalf("%v: envelope %+v exit %d", args, env, code)
	}
	return code, env
}

func (h *harness) ok(args ...string) map[string]any {
	h.t.Helper()
	code, env := h.run(args...)
	if code != 0 {
		h.t.Fatalf("%v: exit %d: %+v", args, code, env.Error)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		h.t.Fatal(err)
	}
	return data
}

func (h *harness) fail(wantExit int, wantCode string, args ...string) *output.Error {
	h.t.Helper()
	code, env := h.run(args...)
	if code != wantExit || env.Error == nil || env.Error.Code != wantCode {
		h.t.Fatalf("%v: exit %d error %+v, want %d/%s", args, code, env.Error, wantExit, wantCode)
	}
	return env.Error
}

func list(v any) []map[string]any {
	var out []map[string]any
	for _, x := range v.([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func idsOf(v any) []int {
	var out []int
	for _, m := range list(v) {
		out = append(out, int(m["id"].(float64)))
	}
	return out
}

func TestChatsList(t *testing.T) {
	h := newHarness(t)
	chats := list(h.ok("chats", "list")["chats"])
	if len(chats) != 4 {
		t.Fatalf("main list = %d", len(chats))
	}
	for _, k := range []string{"id", "name", "type", "username", "unread", "mentions", "pinned", "archived", "muted"} {
		if _, ok := chats[0][k]; !ok {
			t.Errorf("dialog lacks %q", k)
		}
	}
	cases := map[string][]int{
		"--unread":          {10},
		"--type=group":      {-20, -21},
		"--query=BOOK":      {-20, -21},
		"--archived":        {-1000000000030},
		"--limit=2":         {10, -20},
		"--type=bot":        {12},
		"--type=supergroup": nil,
	}
	for flag, want := range cases {
		if got := idsOf(h.ok("chats", "list", flag)["chats"]); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", flag, got, want)
		}
	}
	h.fail(2, output.CodeUsage, "chats", "list", "--type", "robot")
}

func TestChatsShowAndResolution(t *testing.T) {
	h := newHarness(t)
	for _, ref := range []string{"10", "@ann_example", "ann example", "Ann"} {
		if d := h.ok("chats", "show", "--chat", ref); d["id"].(float64) != 10 {
			t.Errorf("%s resolved to %v", ref, d["id"])
		}
	}
	e := h.fail(4, output.CodeAmbiguous, "chats", "show", "--chat", "book")
	if len(e.Details["candidates"].([]any)) != 2 || !strings.Contains(e.Message, "Book Club (-20)") {
		t.Fatalf("ambiguous: %+v", e)
	}
	h.fail(4, output.CodeNotFound, "chats", "show", "--chat", "nobody here")
	h.fail(2, output.CodeUsage, "chats", "show")
}

func TestMessagesList(t *testing.T) {
	h := newHarness(t)
	d := h.ok("messages", "list", "--chat", "10")
	got := idsOf(d["messages"])
	if len(got) != 50 || got[0] != 60 || got[49] != 11 || d["next_offset_id"].(float64) != 11 {
		t.Fatalf("default page: %v next=%v", got, d["next_offset_id"])
	}
	chat := d["chat"].(map[string]any)
	if chat["name"] != "Ann Example" || chat["type"] != "user" {
		t.Fatalf("chat %v", chat)
	}
	msg := list(d["messages"])[0]
	for _, k := range []string{"id", "chat_id", "chat_name", "date", "from_me", "sender_id", "sender_name", "text"} {
		if _, ok := msg[k]; !ok {
			t.Errorf("message lacks %q", k)
		}
	}
	d = h.ok("messages", "list", "--chat", "10", "--offset-id", "11")
	if got := idsOf(d["messages"]); len(got) != 10 || got[0] != 10 || d["next_offset_id"] != nil {
		t.Fatalf("second page %v next=%v", got, d["next_offset_id"])
	}
	if got := idsOf(h.ok("messages", "list", "--chat", "10", "--min-id", "55")["messages"]); fmt.Sprint(got) != "[60 59 58 57 56]" {
		t.Fatalf("min-id %v", got)
	}
	if got := idsOf(h.ok("messages", "list", "--chat", "10", "--limit", "3", "--asc")["messages"]); fmt.Sprint(got) != "[58 59 60]" {
		t.Fatalf("asc %v", got)
	}
	after := t0.Add(40*time.Minute + 30*time.Second).Format("2006-01-02T15:04:05Z")
	before := t0.Add(43 * time.Minute).Format("2006-01-02T15:04")
	if got := idsOf(h.ok("messages", "list", "--chat", "10", "--after", after, "--before", before)["messages"]); fmt.Sprint(got) != "[41 42]" {
		t.Fatalf("after/before %v", got)
	}
	h.fail(2, output.CodeUsage, "messages", "list", "--chat", "10", "--before", "yesterday")
}

func TestMessagesShowContextSearchMentions(t *testing.T) {
	h := newHarness(t)
	if got := idsOf(h.ok("messages", "show", "--chat", "10", "--id", "3,5")["messages"]); fmt.Sprint(got) != "[3 5]" {
		t.Fatalf("show %v", got)
	}
	if got := idsOf(h.ok("messages", "show", "--chat", "10", "--id", "3", "--id", "9")["messages"]); fmt.Sprint(got) != "[3 9]" {
		t.Fatalf("show repeated %v", got)
	}
	if got := idsOf(h.ok("messages", "context", "--chat", "10", "--id", "30", "--around", "2")["messages"]); fmt.Sprint(got) != "[28 29 30 31 32]" {
		t.Fatalf("context %v", got)
	}
	d := h.ok("messages", "search", "note 5", "--chat", "Ann")
	if d["query"] != "note 5" || fmt.Sprint(idsOf(d["messages"])) != "[59 58 57 56 55 54 53 52 51 50 5]" {
		t.Fatalf("search %v", idsOf(d["messages"]))
	}
	if got := idsOf(h.ok("messages", "search", "look")["messages"]); fmt.Sprint(got) != "[3]" {
		t.Fatalf("global search %v", got)
	}
	if got := idsOf(h.ok("messages", "search", "note", "--chat", "10", "--from", "10", "--limit", "2")["messages"]); fmt.Sprint(got) != "[59 57]" {
		t.Fatalf("from %v", got)
	}
	before := h.connects
	h.fail(2, output.CodeUsage, "messages", "search", "x", "--from", "10")
	if h.connects != before {
		t.Fatal("usage error connected to Telegram")
	}
	d = h.ok("messages", "mentions", "--unread")
	if fmt.Sprint(idsOf(d["messages"])) != "[3]" || d["unread_only"] != true {
		t.Fatalf("mentions %v", d)
	}
	if got := idsOf(h.ok("messages", "mentions")["messages"]); fmt.Sprint(got) != "[3]" {
		t.Fatalf("account-wide mentions %v", got)
	}
	// --scan 1 visits only the most recent group (Book Club), which has no mentions.
	if got := idsOf(h.ok("messages", "mentions", "--scan", "1")["messages"]); got != nil {
		t.Fatalf("scan=1 mentions %v", got)
	}
	h.fail(2, output.CodeUsage, "messages", "mentions", "--scan", "0")
}

func TestSendRespectsReadOnly(t *testing.T) {
	h := newHarness(t)
	e := h.fail(3, output.CodeReadOnly, "send", "text", "--chat", "10", "--message", "hello")
	if !strings.Contains(e.Message, "TGAC_READONLY=0") || h.connects != 0 || len(h.fake.Sent) != 0 {
		t.Fatalf("read-only send leaked: %+v connects=%d sent=%d", e, h.connects, len(h.fake.Sent))
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	h.fail(3, output.CodeReadOnly, "send", "file", "--chat", "10", "--path", file)

	for _, v := range []string{"1", "true", "yes", "on"} {
		t.Setenv(config.EnvReadOnly, v)
		h.fail(3, output.CodeReadOnly, "send", "text", "--chat", "10", "--message", "hello")
	}
	t.Setenv(config.EnvReadOnly, "0")
	d := h.ok("send", "text", "--chat", "10", "--message", "hello", "--reply-to", "60")
	sent := d["sent"].(map[string]any)
	if sent["id"].(float64) != 61 || sent["text"] != "hello" || len(h.fake.Sent) != 1 || h.fake.Sent[0].ReplyTo != 60 {
		t.Fatalf("send: %v %+v", sent, h.fake.Sent)
	}
	h.ok("send", "file", "--chat", "10", "--path", file, "--caption", "c")
	if h.fake.Sent[1].Path != file {
		t.Fatalf("send file %+v", h.fake.Sent[1])
	}
	h.fail(2, output.CodeUsage, "send", "file", "--chat", "10", "--path", filepath.Join(t.TempDir(), "missing"))
	h.fail(2, output.CodeUsage, "send", "text", "--chat", "10", "--message", "  ")
}

func TestContactsAndMedia(t *testing.T) {
	h := newHarness(t)
	d := h.ok("contacts", "search", "ann")
	if len(list(d["mine"])) != 1 || d["public"] == nil {
		t.Fatalf("contacts %v", d)
	}
	dir := filepath.Join(t.TempDir(), "dl")
	d = h.ok("media", "download", "--chat", "10", "--id", "7", "--output", dir)
	if filepath.Base(d["path"].(string)) != "report.pdf" || d["media"].(map[string]any)["type"] != "document" {
		t.Fatalf("download %v", d)
	}
	h.fail(4, output.CodeNotFound, "media", "download", "--chat", "10", "--id", "8", "--output", dir)
}

func TestMeAndVersion(t *testing.T) {
	h := newHarness(t)
	if d := h.ok("me"); d["id"].(float64) != 1 || d["name"] != "Owner" {
		t.Fatalf("me %v", d)
	}
	if d := h.ok("version"); d["version"] == nil || d["go"] == nil {
		t.Fatalf("version %v", d)
	}
}

func TestAuthInit(t *testing.T) {
	h := newHarness(t)
	h.app.Stdin = strings.NewReader("0123456789abcdef0123456789abcdef\n")
	d := h.ok("auth", "init", "--api-id", "12345", "--phone", "+1 555 000 0000")
	if d["phone"] != "+15550000000" {
		t.Fatalf("init %v", d)
	}
	p := h.app.Paths
	if config.Mode(p.Home) != "0700" || config.Mode(p.Config()) != "0600" {
		t.Fatalf("modes %s %s", config.Mode(p.Home), config.Mode(p.Config()))
	}
	// Re-init keeps optional fields.
	cfg, _ := p.LoadConfig()
	cfg.DeviceModel = "custom device"
	_ = p.SaveConfig(cfg)
	h.app.Stdin = strings.NewReader("ffffffffffffffffffffffffffffffff\n")
	h.ok("auth", "init", "--api-id", "999", "--phone", "+15550000001")
	cfg, _ = p.LoadConfig()
	if cfg.DeviceModel != "custom device" || cfg.APIID != 999 || cfg.APIHash != "ffffffffffffffffffffffffffffffff" {
		t.Fatalf("re-init %+v", cfg)
	}
	raw, _ := os.ReadFile(p.Config())
	if strings.Contains(h.out.String(), "ffffffff") {
		t.Fatal("api_hash echoed in output")
	}
	if !strings.Contains(string(raw), `"api_hash"`) {
		t.Fatal("config lacks api_hash")
	}
	h.app.Stdin = strings.NewReader("nothex\n")
	h.fail(2, output.CodeUsage, "auth", "init", "--api-id", "1", "--phone", "+15550000000")
	h.fail(2, output.CodeUsage, "auth", "init", "--api-id", "1", "--phone", "15550000000")
}

func TestOfflineStatusAndDoctor(t *testing.T) {
	h := newHarness(t)
	if d := h.ok("auth", "status"); d["authorized"] != false || d["me"] != nil {
		t.Fatalf("status %v", d)
	}
	d := h.ok("doctor")
	if d["readonly"] != true || d["config"] != false || d["session"] != false || d["authorized"] != false {
		t.Fatalf("doctor %v", d)
	}
	if h.raws != 0 || h.connects != 0 {
		t.Fatal("offline commands touched the network")
	}
	// Loose modes are reported.
	_ = h.app.Paths.SaveConfig(config.Config{APIID: 1, APIHash: "0123456789abcdef0123456789abcdef"})
	_ = os.Chmod(h.app.Paths.Config(), 0o644)
	d = h.ok("doctor")
	if probs := d["problems"].([]any); len(probs) != 1 || !strings.Contains(probs[0].(string), "config is 0644") {
		t.Fatalf("problems %v", d["problems"])
	}
	if d["device"].(map[string]any)["device_model"] != "telegram-agent-cli" {
		t.Fatalf("device %v", d["device"])
	}
}

func writeSyntheticTelethon(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.session")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := make([]byte, 256)
	_, _ = rand.Read(key)
	for _, q := range []string{
		`CREATE TABLE version (version integer primary key)`,
		`CREATE TABLE sessions (dc_id integer primary key, server_address text, port integer, auth_key blob, takeout_id integer, tmp_auth_key blob)`,
		`CREATE TABLE entities (id integer primary key, hash integer not null, username text, phone integer, name text, date integer)`,
		`INSERT INTO version VALUES (8)`,
		`INSERT INTO entities VALUES (424242, -5, 'example_user', 15550000002, 'Example User', 0)`,
		`INSERT INTO entities VALUES (-1001234567890, 9, NULL, NULL, 'Example Channel', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions VALUES (2, '192.0.2.10', 443, ?, NULL, NULL)`, key); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportTelethon(t *testing.T) {
	h := newHarness(t)
	src := writeSyntheticTelethon(t)
	d := h.ok("auth", "import-telethon", "--session", strings.TrimSuffix(src, ".session"), "--no-verify")
	if d["imported"] != true || d["dc"].(float64) != 2 || d["peers"].(float64) != 2 || d["verified"] != false {
		t.Fatalf("import %v", d)
	}
	p := h.app.Paths
	if config.Mode(p.Session()) != "0600" || config.Mode(p.Peers()) != "0600" || config.Mode(p.Home) != "0700" {
		t.Fatalf("modes session=%s peers=%s home=%s", config.Mode(p.Session()), config.Mode(p.Peers()), config.Mode(p.Home))
	}
	if strings.Contains(h.out.String(), "auth_key") || strings.Contains(h.out.String(), "AuthKey") {
		t.Fatal("key material in output")
	}
	cache := tgclient.LoadPeerCache(p.Peers())
	if pe, ok := cache.Get(-1001234567890); !ok || pe.AccessHash != 9 || pe.Kind != tgclient.KindChannel {
		t.Fatalf("peer cache %+v", pe)
	}
	h.fail(2, output.CodeUsage, "auth", "import-telethon", "--session", src, "--no-verify")
	// Without a config the check is skipped with a hint, and nothing connects.
	d = h.ok("auth", "import-telethon", "--session", src, "--force")
	if d["hint"] == nil || h.raws != 0 {
		t.Fatalf("no-config import %v raws=%d", d, h.raws)
	}
	h.fail(2, output.CodeUsage, "auth", "import-telethon", "--session", filepath.Join(t.TempDir(), "nope"))
}

func TestLockBusy(t *testing.T) {
	h := newHarness(t)
	t.Setenv(config.EnvLockWait, "0.3")
	_ = h.app.Paths.Ensure()
	lock := h.app.Paths.NewLock()
	if err := lock.Acquire(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	h.fail(6, output.CodeLockBusy, "auth", "import-telethon", "--session", writeSyntheticTelethon(t), "--no-verify")
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	h.fail(2, output.CodeUsage, "bogus")
	h.fail(2, output.CodeUsage, "chats", "bogus")
	h.fail(2, output.CodeUsage, "chats", "list", "--nope")
	h.fail(2, output.CodeUsage, "chats", "list", "--limit", "x")
	h.fail(2, output.CodeUsage, "chats", "list", "--limit", "0")
	h.fail(2, output.CodeUsage, "messages", "list")
	h.fail(2, output.CodeUsage, "messages", "search")
	h.fail(2, output.CodeUsage, "messages", "context", "--chat", "10", "--id", "0")
	if h.connects != 0 {
		t.Fatal("usage errors connected")
	}
	// The hidden --json flag is accepted.
	h.ok("version", "--json")
}

func TestCommandTree(t *testing.T) {
	h := newHarness(t)
	root := h.app.Root()
	want := []string{
		"auth init", "auth qr", "auth request-code", "auth verify", "auth password", "auth status", "auth logout",
		"auth import-telethon", "me", "doctor", "version", "chats list", "chats show",
		"messages list", "messages search", "messages show", "messages context", "messages mentions",
		"contacts search", "media download", "send text", "send file",
	}
	for _, path := range want {
		c, _, err := root.Find(strings.Fields(path))
		if err != nil || c.CommandPath() != "telegram-agent-cli "+path {
			t.Errorf("missing command %q", path)
		}
	}
}
