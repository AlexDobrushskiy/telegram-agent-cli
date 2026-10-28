package tgclient

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// RefKind says how a --chat value should be resolved.
type RefKind int

const (
	RefID       RefKind = iota // marked id (also t.me/c/<id> links)
	RefUsername                // @name, t.me/name, tg://resolve
	RefInvite                  // t.me/+hash, t.me/joinchat/hash
	RefPhone                   // +digits
	RefName                    // dialog name
)

// Ref is a parsed --chat value.
type Ref struct {
	Kind  RefKind
	ID    int64
	Value string // username, invite hash, phone digits, or name
}

var (
	idRe       = regexp.MustCompile(`^-?\d+$`)
	usernameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{2,}$`)
	phoneRe    = regexp.MustCompile(`^\+[\d\s().-]{5,}$`)
	linkHosts  = []string{"t.me", "telegram.me", "telegram.dog"}
)

// ParseRef classifies a --chat value.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return Ref{}, output.Usage("empty chat reference")
	case idRe.MatchString(s):
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return Ref{}, output.Usage("chat id out of range: %s", s)
		}
		return Ref{Kind: RefID, ID: id}, nil
	case strings.HasPrefix(s, "@"):
		return usernameRef(s[1:], s)
	case isLink(s):
		return parseLink(s)
	case phoneRe.MatchString(s):
		return Ref{Kind: RefPhone, Value: digitsOnly(s)}, nil
	}
	return Ref{Kind: RefName, Value: s}, nil
}

func usernameRef(name, orig string) (Ref, error) {
	if !usernameRe.MatchString(name) {
		return Ref{}, output.Usage("not a valid username: %s", orig)
	}
	return Ref{Kind: RefUsername, Value: name}, nil
}

func isLink(s string) bool {
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "tg:") {
		return true
	}
	for _, h := range linkHosts {
		if strings.Contains(low, h+"/") {
			return true
		}
	}
	return false
}

func parseLink(s string) (Ref, error) {
	bad := output.Usage("unsupported Telegram link: %s", s)
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "tg:") {
		u, err := url.Parse("tg://" + strings.TrimLeft(s[len("tg:"):], "/"))
		if err != nil {
			return Ref{}, bad
		}
		q := u.Query()
		switch strings.ToLower(u.Host) {
		case "resolve":
			return usernameRef(q.Get("domain"), s)
		case "join":
			if h := q.Get("invite"); h != "" {
				return Ref{Kind: RefInvite, Value: h}, nil
			}
		case "privatepost":
			if id, err := strconv.ParseInt(q.Get("channel"), 10, 64); err == nil && id > 0 {
				return Ref{Kind: RefID, ID: MarkedID(KindChannel, id)}, nil
			}
		}
		return Ref{}, bad
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return Ref{}, bad
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	known := false
	for _, h := range linkHosts {
		known = known || host == h
	}
	if !known {
		return Ref{}, bad
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return Ref{}, bad
	}
	switch first := parts[0]; {
	case strings.HasPrefix(first, "+") && len(first) > 1:
		if phoneRe.MatchString(first) && digitsOnly(first) == first[1:] && len(first) <= 16 {
			return Ref{Kind: RefPhone, Value: first[1:]}, nil
		}
		return Ref{Kind: RefInvite, Value: first[1:]}, nil
	case first == "joinchat" && len(parts) > 1:
		return Ref{Kind: RefInvite, Value: parts[1]}, nil
	case first == "c" && len(parts) > 1:
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return Ref{}, bad
		}
		return Ref{Kind: RefID, ID: MarkedID(KindChannel, id)}, nil
	case first == "s" && len(parts) > 1:
		return usernameRef(parts[1], s)
	default:
		return usernameRef(first, s)
	}
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MatchDialogs picks dialogs by name: exact (case-insensitive) matches, else substring matches.
func MatchDialogs(name string, dialogs []Dialog) []Dialog {
	low := strings.ToLower(strings.TrimSpace(name))
	var exact, partial []Dialog
	for _, d := range dialogs {
		n := strings.ToLower(d.Name)
		switch {
		case n == low:
			exact = append(exact, d)
		case strings.Contains(n, low):
			partial = append(partial, d)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// PickByName resolves a name among dialogs: one hit, not found, or ambiguous (up to 10 candidates).
func PickByName(name string, dialogs []Dialog) (*Peer, error) {
	hits := MatchDialogs(name, dialogs)
	switch len(hits) {
	case 1:
		p := hits[0].Peer
		return &p, nil
	case 0:
		return nil, output.NotFound("no chat named '%s'", name)
	}
	var names []string
	var cands []ChatRef
	for i, h := range hits {
		if i == 10 {
			break
		}
		names = append(names, fmt.Sprintf("%s (%d)", h.Name, h.ID))
		cands = append(cands, h.Peer.Ref())
	}
	return nil, output.Ambiguous("ambiguous chat name: %s", strings.Join(names, "; ")).
		WithDetail("candidates", cands)
}
