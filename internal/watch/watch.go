// Package watch holds the watcher registry, per-watcher state, the send logs and
// the gate that decides whether a watcher may send one reply.
package watch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scopes: which chats a watcher may answer.
const (
	ScopeListed   = "listed"   // chats in state.json
	ScopeContacts = "contacts" // mutual contacts, or chats in state.json
	ScopeDMFirst  = "dm-first" // 1:1 chats where the other side wrote first
)

// Defaults for omitted registry fields.
const (
	DefaultScope          = ScopeListed
	DefaultMentionsOnly   = true
	DefaultMaxChars       = 300
	DefaultPerHour        = 20
	DefaultPerChatSeconds = 20
	DefaultBackoffMinutes = 15
)

// Watcher dir file names.
const (
	StateFile = "state.json"
	PauseFile = "PAUSE"
	LogFile   = "sends.log"
)

// RegistryHelp documents watchers.json for --help.
const RegistryHelp = `Registry: watchers.json in the data dir, edited by the user (never by an agent):
  {"NAME": {"dir": "/abs/path/to/watcher",   // required; "~/" is expanded
            "scope": "listed",               // listed | contacts | dm-first (default listed)
            "groups": false,                 // allow groups (they must also be listed in state.json)
            "mentions_only": true,           // in groups, only answer messages that mention/reply to you
            "max_chars": 300,                // reply length limit (characters)
            "per_hour": 20,                  // this watcher's sends per hour
            "per_chat_seconds": 20,          // min seconds between replies in one chat (any watcher)
            "backoff_minutes": 15,           // stay quiet when you wrote in the chat this recently
            "approved": "who approved it, when, for what"}}
Watcher dir: state.json {"active_watcher": "<session id>", "chats": {"<chat id>": "note"}},
  optional PAUSE file, sends.log (jsonl, written by watch reply).
Data dir: PAUSE-ALL stops every watcher; answered.jsonl is the shared ledger of answered messages.
Config: watcher_session_env names the env var holding the caller's session id
  (default TGAC_WATCHER_SESSION); global_per_hour caps sends across all watchers (default 30).`

// entry is one raw registry record; pointers tell omitted from explicit values.
type entry struct {
	Dir            string `json:"dir"`
	Scope          string `json:"scope"`
	Groups         *bool  `json:"groups"`
	MentionsOnly   *bool  `json:"mentions_only"`
	MaxChars       *int   `json:"max_chars"`
	PerHour        *int   `json:"per_hour"`
	PerChatSeconds *int   `json:"per_chat_seconds"`
	BackoffMinutes *int   `json:"backoff_minutes"`
	Approved       string `json:"approved"`
}

// Watcher is a registry entry with defaults applied.
type Watcher struct {
	Name           string `json:"name"`
	Dir            string `json:"dir"`
	Scope          string `json:"scope"`
	Groups         bool   `json:"groups"`
	MentionsOnly   bool   `json:"mentions_only"`
	MaxChars       int    `json:"max_chars"`
	PerHour        int    `json:"per_hour"`
	PerChatSeconds int    `json:"per_chat_seconds"`
	BackoffMinutes int    `json:"backoff_minutes"`
	Approved       string `json:"approved"`
}

func (w *Watcher) StatePath() string { return filepath.Join(w.Dir, StateFile) }
func (w *Watcher) PausePath() string { return filepath.Join(w.Dir, PauseFile) }
func (w *Watcher) LogPath() string   { return filepath.Join(w.Dir, LogFile) }

// Registry is watchers.json; entries are decoded on demand so one bad entry
// does not hide the others.
type Registry struct {
	Path    string
	Missing bool
	raw     map[string]json.RawMessage
}

// LoadRegistry reads watchers.json; a missing file is an empty registry.
func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{Path: path, raw: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		r.Missing = true
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &r.raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object of watchers: %w", path, err)
	}
	return r, nil
}

// Names returns the registered watcher names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.raw))
	for n := range r.raw {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Has reports whether name is registered.
func (r *Registry) Has(name string) bool { _, ok := r.raw[name]; return ok }

// Watcher decodes one entry strictly. It returns the best-effort watcher and
// every problem found; a watcher with problems must not send.
func (r *Registry) Watcher(name string) (*Watcher, []string) {
	raw, ok := r.raw[name]
	if !ok {
		return nil, []string{fmt.Sprintf("watcher %q is not registered in %s", name, r.Path)}
	}
	var e entry
	var problems []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		problems = append(problems, fmt.Sprintf("entry is invalid: %v", err))
		_ = json.Unmarshal(raw, &e) // keep what parses, for listings
	}
	w := &Watcher{
		Name: name, Scope: e.Scope, Groups: e.Groups != nil && *e.Groups, MentionsOnly: DefaultMentionsOnly,
		Approved: strings.TrimSpace(e.Approved),
	}
	if e.MentionsOnly != nil {
		w.MentionsOnly = *e.MentionsOnly
	}
	if w.Scope == "" {
		w.Scope = DefaultScope
	}
	switch w.Scope {
	case ScopeListed, ScopeContacts, ScopeDMFirst:
	default:
		problems = append(problems, fmt.Sprintf("scope %q is not one of listed, contacts, dm-first", w.Scope))
	}
	positive := func(field string, v *int, def int) int {
		if v == nil {
			return def
		}
		if *v <= 0 {
			problems = append(problems, fmt.Sprintf("%s must be positive (got %d)", field, *v))
		}
		return *v
	}
	w.MaxChars = positive("max_chars", e.MaxChars, DefaultMaxChars)
	w.PerHour = positive("per_hour", e.PerHour, DefaultPerHour)
	w.PerChatSeconds = positive("per_chat_seconds", e.PerChatSeconds, DefaultPerChatSeconds)
	w.BackoffMinutes = positive("backoff_minutes", e.BackoffMinutes, DefaultBackoffMinutes)

	dir, err := expandDir(e.Dir)
	if err != nil {
		problems = append(problems, err.Error())
	} else if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		problems = append(problems, fmt.Sprintf("dir %s is not a directory", dir))
	}
	w.Dir = dir
	return w, problems
}

func expandDir(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", errors.New("dir is missing")
	}
	if d == "~" || strings.HasPrefix(d, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(home, strings.TrimPrefix(d[1:], "/"))
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("dir %q must be an absolute path", d)
	}
	return filepath.Clean(d), nil
}

// State is a watcher's state.json. The watcher maintains it; other keys are ignored.
type State struct {
	ActiveWatcher string           `json:"active_watcher"`
	Chats         map[int64]string `json:"-"`
	// BadChats are chat keys that are not numeric ids.
	BadChats []string `json:"-"`
}

// LoadState reads state.json.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ActiveWatcher string                     `json:"active_watcher"`
		Chats         map[string]json.RawMessage `json:"chats"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	st := &State{ActiveWatcher: strings.TrimSpace(raw.ActiveWatcher), Chats: map[int64]string{}}
	for k, v := range raw.Chats {
		id, err := strconv.ParseInt(strings.TrimSpace(k), 10, 64)
		if err != nil || id == 0 {
			st.BadChats = append(st.BadChats, k)
			continue
		}
		var note string
		if json.Unmarshal(v, &note) != nil {
			note = string(v)
		}
		st.Chats[id] = note
	}
	sort.Strings(st.BadChats)
	return st, nil
}

// Send is one sends.log line: an attempt that reached Telegram.
type Send struct {
	T        float64 `json:"t"`
	At       string  `json:"at"`
	Watcher  string  `json:"watcher"`
	By       string  `json:"by"`
	ChatID   int64   `json:"chat_id"`
	ReplyTo  int     `json:"reply_to,omitempty"`
	Answered int     `json:"answered,omitempty"`
	Text     string  `json:"text"`
	OK       bool    `json:"ok"`
	MsgID    int     `json:"msg_id,omitempty"`
	Err      string  `json:"err,omitempty"`
}

// Answer is one answered.jsonl line: a message some watcher answered, and the reply.
type Answer struct {
	T       float64 `json:"t"`
	At      string  `json:"at"`
	Watcher string  `json:"watcher"`
	ChatID  int64   `json:"chat_id"`
	MsgID   int     `json:"msg_id"`
	SentID  int     `json:"sent_id"`
}

// Time returns the record's time.
func (s Send) Time() time.Time   { return unixFloat(s.T) }
func (a Answer) Time() time.Time { return unixFloat(a.T) }

func unixFloat(t float64) time.Time {
	sec := int64(t)
	return time.Unix(sec, int64((t-float64(sec))*1e9)).UTC()
}

func stamp(t time.Time) (float64, string) {
	return float64(t.UnixNano()) / 1e9, t.UTC().Format(time.RFC3339)
}

// ReadJSONL reads a jsonl file; a missing file is empty. bad counts unparsable lines.
func ReadJSONL[T any](path string) (out []T, bad int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var v T
		if json.Unmarshal(line, &v) != nil {
			bad++
			continue
		}
		out = append(out, v)
	}
	return out, bad, sc.Err()
}

// AppendJSONL appends one line and syncs it. Callers hold the watch lock.
func AppendJSONL(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
