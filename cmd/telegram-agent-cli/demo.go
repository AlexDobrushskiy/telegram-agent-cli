//go:build demo

// Demo build (go build -tags demo): the CLI runs against an in-memory fake account
// seeded with fictional chats, for recording docs/demo.gif. It never connects to
// Telegram. init runs the whole CLI and exits, so main never runs.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/cli"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient/tgfake"
)

func init() {
	ctx, stop := context.Background(), func() {}
	hookMode := len(os.Args) > 2 && os.Args[1] == "hook" && os.Args[2] == "claude-pretooluse"
	if !hookMode {
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	}
	os.Exit(runDemo(ctx, stop))
}

func runDemo(ctx context.Context, stop func()) int {
	defer stop()
	app, err := cli.New()
	if err != nil {
		return output.Failure(os.Stdout, err)
	}
	// The demo only ever uses a throwaway data dir, never the real one.
	def, _ := os.UserHomeDir()
	def = filepath.Join(def, ".config", "telegram-agent-cli")
	if os.Getenv(config.EnvHome) == "" || app.Paths.Home == def {
		return output.Failure(os.Stdout, output.Usage("demo build: set %s to a temp dir", config.EnvHome))
	}
	fake := seed(time.Now().UTC())
	app.Connect = func(ctx context.Context, _ tgclient.Options, fn func(context.Context, tgclient.Telegram) error) error {
		return tgclient.MapError(fn(ctx, fake))
	}
	app.Raw = func(context.Context, tgclient.Options, func(context.Context, *tgclient.Conn) error) error {
		return errors.New("demo build: no Telegram connection")
	}
	return app.Execute(ctx, os.Args[1:])
}

// Fictional people and chats.
const (
	meID     = int64(5550001)
	priya    = int64(5550101)
	mateo    = int64(5550102)
	yuki     = int64(5550103)
	kwame    = int64(5550104)
	pantry   = int64(5550201)
	flat4b   = int64(-5550301)
	goMeetup = int64(-1005550401)
	runClub  = int64(-1005550402)
	brief    = int64(-1005550501)
)

func seed(now time.Time) *tgfake.Fake {
	f := tgfake.New(tgclient.User{ID: meID, Name: "Robin Vale", Type: "user", Username: ptr("robin_vale")})
	f.Recency = true
	f.Mutual = map[int64]bool{priya: true, mateo: true, yuki: true, kwame: true}
	for _, d := range []tgclient.Dialog{
		{ID: meID, Name: "Saved Messages", Type: "user", Pinned: true},
		{ID: priya, Name: "Priya Raman", Type: "user", Username: ptr("priya_draws"), Unread: 2},
		{ID: mateo, Name: "Mateo Álvarez", Type: "user", Unread: 1},
		{ID: yuki, Name: "Yuki Tanaka", Type: "user"},
		{ID: kwame, Name: "Kwame Mensah", Type: "user"},
		{ID: pantry, Name: "Pantry Bot", Type: "bot", Username: ptr("pantry_demo_bot")},
		{ID: flat4b, Name: "Flat 4B Neighbours", Type: "group", Unread: 4, Mentions: 1},
		{ID: goMeetup, Name: "Go Meetup Porto", Type: "supergroup", Unread: 12, Mentions: 1, Muted: true},
		{ID: runClub, Name: "Harbour Run Club", Type: "supergroup"},
		{ID: brief, Name: "The Morning Brief", Type: "channel", Unread: 3, Muted: true},
	} {
		f.AddDialog(d)
	}
	names := map[int64]string{meID: "Robin Vale", priya: "Priya Raman", mateo: "Mateo Álvarez", yuki: "Yuki Tanaka",
		kwame: "Kwame Mensah", pantry: "Pantry Bot", 5550111: "Lena Fischer", 5550112: "Omar Haddad",
		5550113: "Sofia Rossi", 5550114: "Arjun Mehta", brief: "The Morning Brief"}
	chatNames := map[int64]string{}
	for _, d := range f.Dialogs {
		chatNames[d.ID] = d.Name
	}
	type msg struct {
		chat    int64
		id      int
		from    int64
		ago     time.Duration
		text    string
		reply   int
		mention bool
	}
	for _, m := range []msg{
		{meID, 1, meID, 50 * time.Hour, "ideas: ferry trip, repaint the bike, call the bank", 0, false},
		{priya, 40, meID, 26 * time.Hour, "Could you send the invoice when the logo is final?", 0, false},
		{priya, 41, priya, 3 * time.Hour, "Logo is final! Sent you the invoice for the logo work, no rush 🙂", 0, false},
		{priya, 42, priya, 3*time.Hour - 2*time.Minute, "Also, are we still on for Thursday?", 0, false},
		{mateo, 17, meID, 30 * time.Hour, "Safe flight! Ping me when you land.", 0, false},
		{mateo, 18, mateo, 40 * time.Minute, "Landed in Lisbon! Coffee tomorrow morning?", 0, false},
		{yuki, 9, yuki, 5 * time.Hour, "The photos from the hike are in the shared album", 0, false},
		{yuki, 10, meID, 4 * time.Hour, "Amazing, thank you!", 0, false},
		{kwame, 22, kwame, 9 * time.Hour, "Invoice #2041 is paid, thanks for the quick turnaround", 0, false},
		{pantry, 3, pantry, 20 * time.Hour, "Reminder: oat milk is running low", 0, false},
		{flat4b, 120, 5550112, 6 * time.Hour, "Who has last month's water invoice? Need it for the split", 0, false},
		{flat4b, 121, 5550113, 5 * time.Hour, "I think the building manager emailed it", 0, false},
		{flat4b, 122, 5550111, 2 * time.Hour, "@robin_vale a parcel for you is at my door, grab it tonight?", 0, true},
		{flat4b, 123, 5550112, 90 * time.Minute, "Recycling day moved to Friday", 0, false},
		{goMeetup, 880, meID, 48 * time.Hour, "Slides from tonight's talk coming soon!", 0, false},
		{goMeetup, 901, 5550114, 1 * time.Hour, "Robin, could you share the slides from your generics talk?", 880, true},
		{goMeetup, 902, 5550113, 50 * time.Minute, "Next meetup: 14th, venue TBD", 0, false},
		{runClub, 310, 5550111, 26 * time.Hour, "Sunday 8:00 at the harbour, 12 km easy", 0, false},
		{brief, 77, brief, 7 * time.Hour, "Five stories to start your day", 0, false},
	} {
		from := m.from
		cn := chatNames[m.chat]
		sn := names[m.from]
		f.AddMessage(tgclient.Message{ID: m.id, ChatID: m.chat, ChatName: &cn, FromMe: m.from == meID, SenderID: &from,
			SenderName: &sn, Text: m.text, ReplyTo: m.reply, Mentioned: m.mention, Time: now.Add(-m.ago)})
	}
	return f
}

func ptr[T any](v T) *T { return &v }
