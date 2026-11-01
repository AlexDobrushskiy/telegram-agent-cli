package tgfake

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// E2EWrite records one write the harness made.
type E2EWrite struct {
	Kind    string // text|media|react|press|webappdata|webappurl|stop
	Text    string
	Media   tgclient.OutMedia
	ReplyTo int
	MsgID   int
	Data    []byte
	DraftID int64
	// Grant is the bot id the guard grant on the call's ctx named (0 = none).
	Grant int64
}

// E2E is an in-memory tgclient.E2E: one bot chat, no network.
type E2E struct {
	mu       sync.Mutex
	Peers    map[string]*tgclient.Peer // by normalized ref ("name" without @, or the id)
	Msgs     []tgclient.BotMessage
	Pinned   int
	Menu     *tgclient.MenuButton
	DraftsCh chan tgclient.Draft
	Writes   []E2EWrite
	Resolved []string
	NextID   int
	// Answer is the callback answer a press gets (nil: answered:false).
	Answer *tgclient.CallbackAnswer
	// OnHistory runs before each BotHistory with the call count, to script events.
	OnHistory func(n int)
	history   int
}

var _ tgclient.E2E = (*E2E)(nil)

// NewE2E returns a fake with the given peers.
func NewE2E(peers ...tgclient.Peer) *E2E {
	f := &E2E{Peers: map[string]*tgclient.Peer{}, DraftsCh: make(chan tgclient.Draft, 64), NextID: 1000}
	for i := range peers {
		p := peers[i]
		f.Peers[strconv.FormatInt(p.ID, 10)] = &p
		if p.Username != "" {
			f.Peers[strings.ToLower(p.Username)] = &p
		}
	}
	return f
}

// Add appends messages to the chat.
func (f *E2E) Add(msgs ...tgclient.BotMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Msgs = append(f.Msgs, msgs...)
}

// Edit replaces a message by id.
func (f *E2E) Edit(m tgclient.BotMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Msgs {
		if f.Msgs[i].ID == m.ID {
			f.Msgs[i] = m
		}
	}
}

func (f *E2E) record(ctx context.Context, w E2EWrite) {
	w.Grant = guard.GrantedBot(ctx)
	f.Writes = append(f.Writes, w)
}

func (f *E2E) Self(context.Context) (*tgclient.User, error) {
	return &tgclient.User{ID: 1, Name: "Owner", Type: "user"}, nil
}

func (f *E2E) Resolve(_ context.Context, ref string) (*tgclient.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Resolved = append(f.Resolved, ref)
	key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ref), "@"))
	if p, ok := f.Peers[key]; ok {
		c := *p
		return &c, nil
	}
	return nil, output.NotFound("cannot resolve %s", ref)
}

func (f *E2E) BotHistory(_ context.Context, _ *tgclient.Peer, minID, limit int) ([]tgclient.BotMessage, error) {
	f.mu.Lock()
	f.history++
	n, hook := f.history, f.OnHistory
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgclient.BotMessage
	for _, m := range f.Msgs {
		if m.ID > minID {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b tgclient.BotMessage) int { return b.ID - a.ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *E2E) BotMessagesByID(_ context.Context, _ *tgclient.Peer, ids []int) ([]tgclient.BotMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgclient.BotMessage
	for _, m := range f.Msgs {
		if slices.Contains(ids, m.ID) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *E2E) PinnedID(context.Context, *tgclient.Peer) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Pinned, nil
}

func (f *E2E) MenuButton(context.Context, *tgclient.Peer) (*tgclient.MenuButton, error) {
	return f.Menu, nil
}

func (f *E2E) newMessage(m tgclient.BotMessage) int {
	f.NextID++
	m.ID, m.From, m.Date = f.NextID, "me", "2026-01-01T00:00:00Z"
	f.Msgs = append(f.Msgs, m)
	return m.ID
}

func (f *E2E) BotSendText(ctx context.Context, _ *tgclient.Peer, text string, replyTo int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "text", Text: text, ReplyTo: replyTo})
	return f.newMessage(tgclient.BotMessage{Text: text, ReplyTo: replyTo}), nil
}

func (f *E2E) BotSendMedia(ctx context.Context, _ *tgclient.Peer, m tgclient.OutMedia, caption string, replyTo int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "media", Media: m, Text: caption, ReplyTo: replyTo})
	return f.newMessage(tgclient.BotMessage{Text: caption, ReplyTo: replyTo, Media: &tgclient.Media{Type: m.Kind}}), nil
}

func (f *E2E) React(ctx context.Context, _ *tgclient.Peer, msgID int, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "react", MsgID: msgID, Text: emoji})
	for i := range f.Msgs {
		if f.Msgs[i].ID == msgID {
			f.Msgs[i].Reactions = nil
			if emoji != "" {
				f.Msgs[i].Reactions = []tgclient.Reaction{{Emoji: emoji, Count: 1, Mine: true}}
			}
		}
	}
	return nil
}

func (f *E2E) PressCallback(ctx context.Context, _ *tgclient.Peer, msgID int, data []byte) (*tgclient.CallbackAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "press", MsgID: msgID, Data: data})
	if f.Answer == nil {
		return &tgclient.CallbackAnswer{}, nil
	}
	return f.Answer, nil
}

func (f *E2E) SendWebAppData(ctx context.Context, _ *tgclient.Peer, buttonText, data string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "webappdata", Text: buttonText, Data: []byte(data)})
	f.newMessage(tgclient.BotMessage{Service: &tgclient.Service{Type: "WebViewDataSent", ButtonText: buttonText}})
	return nil
}

func (f *E2E) WebAppURL(ctx context.Context, _ *tgclient.Peer, r tgclient.WebAppRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "webappurl", Text: r.Kind + " " + r.URL})
	return r.URL + "#tgWebAppData=fake", nil
}

func (f *E2E) StopDraft(ctx context.Context, _ *tgclient.Peer, draftID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, E2EWrite{Kind: "stop", DraftID: draftID})
	return nil
}

func (f *E2E) Drafts() <-chan tgclient.Draft { return f.DraftsCh }
