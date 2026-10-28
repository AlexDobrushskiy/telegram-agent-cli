package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryDefaults(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "watchers.json")
	writeFile(t, reg, `{"w": {"dir": "`+dir+`"}}`)
	r, err := LoadRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	w, problems := r.Watcher("w")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	want := Watcher{Name: "w", Dir: dir, Scope: ScopeListed, MentionsOnly: true, MaxChars: DefaultMaxChars,
		PerHour: DefaultPerHour, PerChatSeconds: DefaultPerChatSeconds, BackoffMinutes: DefaultBackoffMinutes}
	if *w != want {
		t.Fatalf("got %+v want %+v", *w, want)
	}
}

func TestRegistryProblems(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "watchers.json")
	writeFile(t, reg, `{"bad": {"dir": "rel", "scope": "x", "max_chars": 0, "extra": 1},
		"ok": {"dir": "`+dir+`", "scope": "dm-first", "groups": true, "mentions_only": false}}`)
	r, err := LoadRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, p := r.Watcher("bad"); len(p) != 4 {
		t.Fatalf("problems %v", p)
	}
	w, p := r.Watcher("ok")
	if len(p) != 0 || !w.Groups || w.MentionsOnly || w.Scope != ScopeDMFirst {
		t.Fatalf("ok = %+v %v", w, p)
	}
	if _, p := r.Watcher("missing"); len(p) != 1 {
		t.Fatal("missing watcher has no problem")
	}
	if got := strings.Join(r.Names(), ","); got != "bad,ok" {
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

func TestStateAndJSONL(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, StateFile)
	writeFile(t, sp, `{"active_watcher": " s1 ", "chats": {"101": "lead", "-201": {"n": 1}, "abc": "x"}, "cursor": 5}`)
	st, err := LoadState(sp)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveWatcher != "s1" || st.Chats[101] != "lead" || st.Chats[-201] != `{"n": 1}` || len(st.BadChats) != 1 {
		t.Fatalf("state %+v", st)
	}
	lp := filepath.Join(dir, LogFile)
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
	if s, bad, err := ReadJSONL[Send](filepath.Join(dir, "none")); s != nil || bad != 0 || err != nil {
		t.Fatal("missing file is not empty")
	}
}
