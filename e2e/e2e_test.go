//go:build e2e

// Package e2e runs read-only checks against the real account in $TGAC_HOME.
// Run: TGAC_E2E=1 go test -tags e2e -count=1 -v ./e2e/
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/cli"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func TestMain(m *testing.M) {
	if os.Getenv("TGAC_E2E") != "1" {
		fmt.Println("e2e skipped: set TGAC_E2E=1 (uses the real account in $TGAC_HOME, read-only)")
		os.Exit(0)
	}
	// Never write, whatever the caller's environment says.
	os.Setenv(config.EnvReadOnly, "1")
	os.Exit(m.Run())
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// run executes the CLI in-process and returns the exit code and envelope.
func run(t *testing.T, args ...string) (int, envelope) {
	t.Helper()
	app, err := cli.New()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app.Stdout = &out
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	code := app.Execute(ctx, args)
	var env envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("%v: not a JSON envelope: %v\n%s", args, err, out.String())
	}
	if env.OK != (code == 0) {
		t.Fatalf("%v: ok=%v but exit %d", args, env.OK, code)
	}
	return code, env
}

func ok(t *testing.T, v any, args ...string) {
	t.Helper()
	code, env := run(t, args...)
	if code != 0 {
		t.Fatalf("%v: exit %d %+v", args, code, env.Error)
	}
	if v != nil {
		if err := json.Unmarshal(env.Data, v); err != nil {
			t.Fatal(err)
		}
	}
}

type dialog struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Unread int    `json:"unread"`
}

type chats struct {
	Chats []dialog `json:"chats"`
}

type messages struct {
	Messages []struct {
		ID     int   `json:"id"`
		ChatID int64 `json:"chat_id"`
	} `json:"messages"`
}

func TestDoctor(t *testing.T) {
	var d struct {
		ReadOnly   bool     `json:"readonly"`
		Authorized bool     `json:"authorized"`
		Problems   []string `json:"problems"`
		Error      string   `json:"error"`
	}
	ok(t, &d, "doctor")
	if !d.ReadOnly || !d.Authorized || len(d.Problems) > 0 || d.Error != "" {
		t.Fatalf("doctor: %+v", d)
	}
	ok(t, nil, "me")
}

func TestReadChat(t *testing.T) {
	var c chats
	ok(t, &c, "chats", "list", "--limit", "10")
	if len(c.Chats) == 0 {
		t.Fatal("no chats")
	}
	id := fmt.Sprint(c.Chats[0].ID)
	ok(t, nil, "chats", "show", "--chat", id)
	var m messages
	ok(t, &m, "messages", "list", "--chat", id, "--limit", "5")
	if len(m.Messages) > 0 {
		mid := fmt.Sprint(m.Messages[0].ID)
		ok(t, nil, "messages", "show", "--chat", id, "--id", mid)
		ok(t, nil, "messages", "context", "--chat", id, "--id", mid, "--around", "2")
	}
}

func TestSearchAndMentions(t *testing.T) {
	ok(t, nil, "messages", "search", "a", "--limit", "3")
	var c chats
	ok(t, &c, "chats", "list", "--limit", "1")
	ok(t, nil, "messages", "search", "a", "--chat", fmt.Sprint(c.Chats[0].ID), "--limit", "3")
	ok(t, nil, "messages", "mentions", "--limit", "5", "--scan", "10")
	ok(t, nil, "messages", "mentions", "--unread", "--limit", "5")
	ok(t, nil, "contacts", "search", "a", "--limit", "3")
}

func TestReadingKeepsUnreadCount(t *testing.T) {
	var before chats
	ok(t, &before, "chats", "list", "--unread", "--limit", "3")
	if len(before.Chats) == 0 {
		t.Skip("no unread chats to check")
	}
	for _, d := range before.Chats {
		id := fmt.Sprint(d.ID)
		ok(t, nil, "messages", "list", "--chat", id, "--limit", "20")
		ok(t, nil, "messages", "mentions", "--chat", id, "--unread", "--limit", "5")
	}
	var after chats
	ok(t, &after, "chats", "list", "--unread", "--limit", "50")
	now := map[int64]int{}
	for _, d := range after.Chats {
		now[d.ID] = d.Unread
	}
	for _, d := range before.Chats {
		if now[d.ID] < d.Unread {
			t.Errorf("%d: unread went %d → %d after reading", d.ID, d.Unread, now[d.ID])
		}
	}
}

func TestSendRefusedReadOnly(t *testing.T) {
	code, env := run(t, "send", "text", "--chat", "1", "--message", "e2e must never send this")
	if code != 3 || env.Error == nil || env.Error.Code != "readonly" {
		t.Fatalf("exit %d %+v", code, env.Error)
	}
}

// recorder sits after the guard: everything it sees goes to the network.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		r.mu.Lock()
		r.seen = append(r.seen, guard.MethodName(in))
		r.mu.Unlock()
		return next.Invoke(ctx, in, out)
	}
}

func TestGuardBlocksLiveWrites(t *testing.T) {
	home, err := config.DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	opts := tgclient.Options{Paths: config.Paths{Home: home}, AppVersion: "telegram-agent-cli e2e", NeedAuth: true,
		Middlewares: []telegram.Middleware{rec}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = tgclient.Run(ctx, opts, func(ctx context.Context, c *tgclient.Conn) error {
		writes := map[string]func() error{
			"messages.readHistory": func() error {
				_, err := c.API.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: &tg.InputPeerSelf{}})
				return err
			},
			"account.updateStatus": func() error {
				_, err := c.API.AccountUpdateStatus(ctx, true)
				return err
			},
		}
		for name, call := range writes {
			err := call()
			var be *guard.BlockedError
			if !errors.As(err, &be) || be.Method != name {
				t.Errorf("%s: want guard block, got %v", name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	sawRead := false
	for _, name := range rec.seen {
		if name == "messages.readHistory" || name == "account.updateStatus" {
			t.Errorf("%s reached the network", name)
		}
		if !guard.Allowed(name, false) {
			t.Errorf("non-read %s reached the network", name)
		}
		sawRead = sawRead || name == "users.getUsers"
	}
	if !sawRead {
		t.Fatalf("recorder saw no traffic (%v): it is not in the chain", rec.seen)
	}
}

// noResolvePhone fails contacts.resolvePhone before the network and counts attempts.
type noResolvePhone struct {
	mu    sync.Mutex
	tries int
}

func (b *noResolvePhone) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if guard.MethodName(in) == "contacts.resolvePhone" {
			b.mu.Lock()
			b.tries++
			b.mu.Unlock()
			return errors.New("e2e: contacts.resolvePhone is not allowed in this suite")
		}
		return next.Invoke(ctx, in, out)
	}
}

// TestResolvePhoneFromContacts resolves +phone via the cache/contacts path only.
func TestResolvePhoneFromContacts(t *testing.T) {
	home, err := config.DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	block := &noResolvePhone{}
	opts := tgclient.Options{Paths: config.Paths{Home: home}, AppVersion: "telegram-agent-cli e2e", NeedAuth: true,
		Middlewares: []telegram.Middleware{block}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	skip := "" // t.Skip must run on the test goroutine, not inside gotd's callback
	err = tgclient.Run(ctx, opts, func(ctx context.Context, c *tgclient.Conn) error {
		r, err := c.API.ContactsGetContacts(ctx, 0)
		if err != nil {
			return err
		}
		list, ok := r.(*tg.ContactsContacts)
		if !ok {
			skip = "contacts not returned"
			return nil
		}
		for _, uc := range list.Users {
			u, ok := uc.(*tg.User)
			if !ok || u.Phone == "" || u.Self {
				continue
			}
			p, err := c.Session().Resolve(ctx, "+"+u.Phone)
			if err != nil {
				return err
			}
			if p.ID != u.ID {
				t.Errorf("+phone resolved to %d, want %d", p.ID, u.ID) // ids only: no phone numbers in logs
			}
			return nil
		}
		skip = "no contact with a visible phone"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	if block.tries != 0 {
		t.Fatalf("contacts.resolvePhone was attempted %d times", block.tries)
	}
}
