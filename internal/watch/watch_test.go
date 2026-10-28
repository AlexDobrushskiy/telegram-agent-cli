package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

func registry(t *testing.T, js string) *Registry {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	if err := os.WriteFile(p.Watchers(), []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryDefaults(t *testing.T) {
	r := registry(t, `{"w": {"session": "s1", "chats": {"101": "lead", "-201": {"n": 1}}}}`)
	w, problems := r.Watcher("w")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if w.Session != "s1" || w.Chats[101] != "lead" || w.Chats[-201] != `{"n": 1}` || !w.Listed(101) || w.Listed(5) {
		t.Fatalf("binding/chats %+v", w)
	}
	if w.Scope != ScopeListed || !w.MentionsOnly || w.Groups || w.MaxChars != DefaultMaxChars || w.PerHour != DefaultPerHour ||
		w.PerChatSeconds != DefaultPerChatSeconds || w.BackoffMinutes != DefaultBackoffMinutes || w.Dir != "" {
		t.Fatalf("defaults %+v", w)
	}
	if w.LogPath() != filepath.Join(filepath.Dir(r.Path), "watchers", "w", LogFile) || len(w.PausePaths()) != 1 {
		t.Fatalf("paths %s %v", w.LogPath(), w.PausePaths())
	}
}

func TestRegistryProblems(t *testing.T) {
	dir := t.TempDir()
	r := registry(t, `{"bad": {"dir": "rel", "scope": "x", "max_chars": 0, "extra": 1, "chats": {"abc": ""}},
		"ok": {"session": "s", "dir": "`+dir+`", "scope": "dm-first", "groups": true, "mentions_only": false},
		"../evil": {"session": "s"}}`)
	if _, p := r.Watcher("bad"); len(p) != 6 { // dir, scope, max_chars, unknown field, chats key, session
		t.Fatalf("problems %v", p)
	}
	w, p := r.Watcher("ok")
	if len(p) != 0 || !w.Groups || w.MentionsOnly || w.Scope != ScopeDMFirst || len(w.PausePaths()) != 2 {
		t.Fatalf("ok = %+v %v", w, p)
	}
	if w, p := r.Watcher("../evil"); len(p) != 1 || w.LogPath() != "" {
		t.Fatalf("unsafe name: %v %q", p, w.LogPath())
	}
	if _, p := r.Watcher("missing"); len(p) != 1 {
		t.Fatal("missing watcher has no problem")
	}
	if got := strings.Join(r.Names(), ","); got != "../evil,bad,ok" {
		t.Fatalf("names %s", got)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := expandDir("~/w")
	if err != nil || got != filepath.Join(home, "w") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestJSONL(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "sub", LogFile)
	if err := AppendJSONL(lp, Send{T: 1, ChatID: 1, OK: true}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(lp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("not json\n\n")
	f.Close()
	if err := AppendJSONL(lp, Send{T: 2, ChatID: 2}); err != nil {
		t.Fatal(err)
	}
	sends, bad, err := ReadJSONL[Send](lp)
	if err != nil || bad != 1 || len(sends) != 2 || sends[1].ChatID != 2 {
		t.Fatalf("%v %d %v", sends, bad, err)
	}
	if st, _ := os.Stat(filepath.Dir(lp)); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	if s, bad, err := ReadJSONL[Send](filepath.Join(t.TempDir(), "none")); s != nil || bad != 0 || err != nil {
		t.Fatal("missing file is not empty")
	}
}
