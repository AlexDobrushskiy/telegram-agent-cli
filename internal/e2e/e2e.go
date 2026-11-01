// Package e2e is the logic of the e2e test harness: which bot chats it may
// write to, the reply keyboard a chat currently shows, button lookup, and the
// wait helpers. Telegram access goes through tgclient.E2E.
package e2e

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// Resolver resolves a chat reference.
type Resolver interface {
	Resolve(ctx context.Context, ref string) (*tgclient.Peer, error)
}

// normalize folds the textual forms of a chat reference (@name, name, t.me/name).
func normalize(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, p := range []string{"https://t.me/", "http://t.me/", "t.me/"} {
		if strings.HasPrefix(low, p) {
			low = low[len(p):]
			break
		}
	}
	return strings.TrimPrefix(low, "@")
}

// plan is the result of the offline allow-list check.
type plan struct {
	ref  string // entry to resolve
	byID int64  // numeric --chat: must equal the id of some allowed entry
}

// precheck matches --chat against the allow-list without touching the network.
func precheck(allowed []string, chat string) (plan, error) {
	if len(allowed) == 0 {
		return plan{}, output.E2ERefused("no e2e.allowed_chats in config.json: add the bot with " +
			"'TGAC_READONLY=0 telegram-agent-cli e2e allow @your_bot'")
	}
	if strings.TrimSpace(chat) == "" {
		if len(allowed) == 1 {
			return plan{ref: allowed[0]}, nil
		}
		return plan{}, output.Usage("--chat is required when e2e.allowed_chats has several entries (%s)", strings.Join(allowed, ", "))
	}
	want := normalize(chat)
	for _, a := range allowed {
		if normalize(a) == want {
			return plan{ref: a}, nil
		}
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(chat), 10, 64); err == nil && id != 0 {
		return plan{byID: id}, nil
	}
	return plan{}, notAllowed(chat)
}

// Precheck refuses a --chat that cannot be on the allow-list, without network access.
func Precheck(allowed []string, chat string) error {
	_, err := precheck(allowed, chat)
	return err
}

// SameRef reports whether two chat references are the same textual form.
func SameRef(a, b string) bool { return normalize(a) == normalize(b) }

func notAllowed(chat string) *output.Error {
	return output.E2ERefused("%q is not in e2e.allowed_chats; e2e commands only act on allow-listed bots", chat).
		WithDetail("chat", chat)
}

// Target resolves the chat an e2e command acts on: an e2e.allowed_chats entry
// that is a bot. Anything else is refused (exit 8) before any write.
func Target(ctx context.Context, r Resolver, allowed []string, chat string) (*tgclient.Peer, error) {
	pl, err := precheck(allowed, chat)
	if err != nil {
		return nil, err
	}
	var p *tgclient.Peer
	if pl.byID != 0 {
		for _, a := range allowed {
			ap, err := r.Resolve(ctx, a)
			if err != nil {
				continue // an unresolvable entry cannot match
			}
			if ap.ID == pl.byID {
				p = ap
				break
			}
		}
		if p == nil {
			return nil, notAllowed(chat)
		}
	} else {
		if p, err = r.Resolve(ctx, pl.ref); err != nil {
			return nil, err
		}
	}
	if p.Type != "bot" || p.Kind != tgclient.KindUser {
		return nil, output.E2ERefused("%s (%d) is a %s, not a bot; e2e commands only act on bots", pl.ref, p.ID, p.Type).
			WithDetail("chat", p.ID)
	}
	return p, nil
}

// Keyboard is the reply keyboard a chat shows now.
type Keyboard struct {
	MessageID int                   `json:"message_id"` // the bot message that set it
	Markup    *tgclient.ReplyMarkup `json:"keyboard"`
}

// CurrentKeyboard finds the reply keyboard in effect: the newest bot message
// with a non-inline markup decides; hide and force_reply leave none. msgs are
// newest first. The result is nil when no keyboard is shown.
func CurrentKeyboard(msgs []tgclient.BotMessage) *Keyboard {
	for _, m := range msgs {
		if m.From != "bot" || m.Markup == nil {
			continue
		}
		if m.Markup.Type != "keyboard" {
			return nil
		}
		return &Keyboard{MessageID: m.ID, Markup: m.Markup}
	}
	return nil
}

// FindButton picks a button by exact text, else by its 0-based position counted
// row by row.
func FindButton(rows [][]tgclient.Button, sel string) (tgclient.Button, error) {
	var all []tgclient.Button
	for _, r := range rows {
		all = append(all, r...)
	}
	var hits []tgclient.Button
	for _, b := range all {
		if b.Text == sel {
			hits = append(hits, b)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return tgclient.Button{}, output.Ambiguous("%d buttons are labelled %q; pick one by index", len(hits), sel)
	}
	if i, err := strconv.Atoi(strings.TrimSpace(sel)); err == nil && i >= 0 && i < len(all) {
		return all[i], nil
	}
	labels := make([]string, len(all))
	for i, b := range all {
		labels[i] = fmt.Sprintf("%d:%q", i, b.Text)
	}
	return tgclient.Button{}, output.NotFound("no button %q (buttons: %s)", sel, strings.Join(labels, ", ")).
		WithDetail("buttons", labels)
}

// NormalizeEmoji drops variation selectors: Telegram's reaction list uses "❤", not "❤️".
func NormalizeEmoji(e string) string {
	return strings.ReplaceAll(strings.TrimSpace(e), "️", "")
}
