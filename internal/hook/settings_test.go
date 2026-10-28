package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC)

const bin = "/opt/tools/bin/telegram-agent-cli"

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

const freshInstall = `{
  "env": {
    "TGAC_READONLY": "1"
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Monitor",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/tools/bin/telegram-agent-cli hook claude-pretooluse"
          }
        ]
      },
      {
        "matcher": "Read|Edit|Write|MultiEdit|NotebookEdit|Grep",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/tools/bin/telegram-agent-cli hook claude-pretooluse"
          }
        ]
      }
    ]
  }
}
`

func TestInstallFresh(t *testing.T) {
	for _, initial := range []string{"", "{}", "  \n"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "sub", "settings.json")
		if initial != "" {
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			write(t, p, initial)
		}
		r, err := Install(p, bin, false, now)
		if err != nil {
			t.Fatal(err)
		}
		first := read(t, p)
		if strings.TrimSuffix(first, "\n") != strings.TrimSuffix(freshInstall, "\n") ||
			strings.HasSuffix(first, "\n") != (initial != "{}") {
			t.Errorf("initial %q: got\n%q", initial, first)
		}
		if !r.Changed || len(r.Actions) != 3 || r.Diff == "" {
			t.Errorf("result %+v", r)
		}
		if (initial == "") != (r.Backup == "") {
			t.Errorf("initial %q: backup %q", initial, r.Backup)
		}
		// idempotent
		r, err = Install(p, bin, false, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if r.Changed || r.Backup != "" || len(r.Actions) != 0 || read(t, p) != first {
			t.Errorf("second install changed: %+v", r)
		}
	}
}

const existing = `{
    "model": "some-model",
    "env": {
        "OTHER": "x",
        "TGAC_READONLY": "0"
    },
    "permissions": {
        "allow": ["Bash(ls:*)"],
        "deny": []
    },
    "hooks": {
        "PreToolUse": [
            {
                "matcher": "Bash",
                "hooks": [
                    {"type": "command", "command": "~/.claude/hooks/other-guard.py", "timeout": 1.50}
                ]
            },
            {
                "matcher": "WebFetch",
                "hooks": [{"type": "command", "command": "fetch-guard"}]
            }
        ],
        "PostToolUse": [
            {"matcher": "Edit", "hooks": [{"type": "command", "command": "fmt <file> & > \"x\""}]}
        ]
    },
    "z_last": null,
    "unicode": "é ✓"
}`

func TestInstallPreserves(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, existing)
	r, err := Install(p, bin, false, now)
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	want := `{
    "model": "some-model",
    "env": {
        "OTHER": "x",
        "TGAC_READONLY": "1"
    },
    "permissions": {
        "allow": [
            "Bash(ls:*)"
        ],
        "deny": []
    },
    "hooks": {
        "PreToolUse": [
            {
                "matcher": "Bash",
                "hooks": [
                    {
                        "type": "command",
                        "command": "~/.claude/hooks/other-guard.py",
                        "timeout": 1.50
                    }
                ]
            },
            {
                "matcher": "WebFetch",
                "hooks": [
                    {
                        "type": "command",
                        "command": "fetch-guard"
                    }
                ]
            },
            {
                "matcher": "Bash|Monitor",
                "hooks": [
                    {
                        "type": "command",
                        "command": "/opt/tools/bin/telegram-agent-cli hook claude-pretooluse"
                    }
                ]
            },
            {
                "matcher": "Read|Edit|Write|MultiEdit|NotebookEdit|Grep",
                "hooks": [
                    {
                        "type": "command",
                        "command": "/opt/tools/bin/telegram-agent-cli hook claude-pretooluse"
                    }
                ]
            }
        ],
        "PostToolUse": [
            {
                "matcher": "Edit",
                "hooks": [
                    {
                        "type": "command",
                        "command": "fmt <file> & > \"x\""
                    }
                ]
            }
        ]
    },
    "z_last": null,
    "unicode": "é ✓"
}`
	if got != want {
		t.Errorf("got\n%s\n\ndiff:\n%s", got, unifiedDiff("s", want, got))
	}
	real, _ := filepath.EvalSymlinks(p)
	if r.Backup != real+".bak-20260501-123045" || read(t, r.Backup) != existing {
		t.Errorf("backup %q", r.Backup)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want the original 0600", st.Mode().Perm())
	}

	// uninstall removes exactly what install added (TGAC_READONLY was "0" before: now unset)
	r, err = Uninstall(p, false, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	after := read(t, p)
	if strings.Contains(after, BinaryName) || strings.Contains(after, "TGAC_READONLY") ||
		!strings.Contains(after, "other-guard.py") || !strings.Contains(after, `"matcher": "WebFetch"`) ||
		strings.Contains(after, "NotebookEdit") || !strings.Contains(after, `"OTHER": "x"`) {
		t.Errorf("after uninstall:\n%s", after)
	}
	if len(r.Actions) != 3 || r.Backup == "" {
		t.Errorf("uninstall result %+v", r)
	}
}

func TestUninstallRoundTrip(t *testing.T) {
	orig := "{\n  \"model\": \"m\",\n  \"hooks\": {\n    \"Stop\": []\n  }\n}\n"
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, orig)
	if _, err := Install(p, bin, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(p, false, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != orig {
		t.Errorf("round trip:\n%s", got)
	}
	r, err := Uninstall(p, false, now)
	if err != nil || r.Changed {
		t.Errorf("second uninstall %+v %v", r, err)
	}

	// fresh install then uninstall leaves an empty object
	p2 := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Install(p2, bin, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(p2, false, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p2); got != "{}\n" {
		t.Errorf("fresh round trip: %q", got)
	}
	// missing file: nothing to do, nothing written
	p3 := filepath.Join(t.TempDir(), "settings.json")
	if r, err := Uninstall(p3, false, now); err != nil || r.Changed {
		t.Errorf("uninstall missing: %+v %v", r, err)
	}
	if _, err := os.Stat(p3); err == nil {
		t.Error("uninstall created a file")
	}
}

func TestInstallUpdatesAndDedupes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, `{"hooks": {"PreToolUse": [
	  {"matcher": "Bash", "hooks": [
	    {"type": "command", "command": "/old/telegram-agent-cli hook claude-pretooluse"},
	    {"type": "command", "command": "'/other place/telegram-agent-cli' hook claude-pretooluse"}
	  ]},
	  {"matcher": "Read|Edit|Write", "hooks": [{"type": "command", "command": "/old/telegram-agent-cli hook claude-pretooluse"}]},
	  {"matcher": "Read", "hooks": [
	    {"type": "command", "command": "keep-me"},
	    {"type": "command", "command": "/old/telegram-agent-cli hook claude-pretooluse"}
	  ]}
	]}}`)
	newBin := "/new place/telegram-agent-cli"
	r, err := Install(p, newBin, false, now)
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	cmd := `"'/new place/telegram-agent-cli' hook claude-pretooluse"`
	if n := strings.Count(got, "hook claude-pretooluse"); n != 2 || strings.Count(got, cmd) != 2 {
		t.Errorf("want exactly 2 guard hooks with the new path, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "keep-me") || strings.Contains(got, `"Read|Edit|Write"`) {
		t.Errorf("stale entries:\n%s", got)
	}
	if len(r.Actions) < 4 {
		t.Errorf("actions %v", r.Actions)
	}
	if r, _ := Install(p, newBin, false, now); r.Changed {
		t.Errorf("not idempotent: %v", r.Actions)
	}
}

func TestInstallAppendsToExistingEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, `{"hooks": {"PreToolUse": [{"matcher": "Bash|Monitor", "hooks": [{"type": "command", "command": "other"}]}]}}`)
	if _, err := Install(p, bin, false, now); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.Count(got, `"matcher": "Bash|Monitor"`) != 1 || !strings.Contains(got, `"command": "other"`) ||
		strings.Count(got, "hook claude-pretooluse") != 2 {
		t.Errorf("got\n%s", got)
	}
}

func TestInstallMigratesOldBashMatcher(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [
	  {"type": "command", "command": "other"},
	  {"type": "command", "command": "`+bin+` hook claude-pretooluse"}]}]}}`)
	if _, err := Install(p, bin, false, now); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if !strings.Contains(got, `"matcher": "Bash"`) || !strings.Contains(got, `"matcher": "Bash|Monitor"`) ||
		strings.Count(got, "hook claude-pretooluse") != 2 || !strings.Contains(got, `"command": "other"`) {
		t.Errorf("got\n%s", got)
	}
}

func TestInstallDryRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	write(t, p, `{"model": "m"}`)
	r, err := Install(p, bin, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, p) != `{"model": "m"}` || r.Backup != "" || !r.DryRun || !r.Changed {
		t.Errorf("dry run wrote: %+v", r)
	}
	if !strings.Contains(r.Diff, `+  "env": {`) || !strings.Contains(r.Diff, `-{"model": "m"}`) {
		t.Errorf("diff:\n%s", r.Diff)
	}
	if !strings.Contains(string(r.Result), `"TGAC_READONLY": "1"`) {
		t.Errorf("result %s", r.Result)
	}
	if matches, _ := filepath.Glob(p + ".bak-*"); len(matches) != 0 {
		t.Errorf("backups %v", matches)
	}
}

func TestInstallSymlinkedSettings(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-settings.json")
	write(t, target, `{}`)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(link, bin, false, now); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if !strings.Contains(read(t, target), BinaryName) {
		t.Error("target not updated")
	}
}

func TestInstallErrors(t *testing.T) {
	for _, s := range []string{
		`{"model": `,
		`[]`,
		`{"env": "x"}`,
		`{"hooks": {"PreToolUse": {}}}`,
		`{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": "x"}]}}`,
		`{} {}`,
	} {
		p := filepath.Join(t.TempDir(), "settings.json")
		write(t, p, s)
		if _, err := Install(p, bin, false, now); err == nil {
			t.Errorf("Install(%q) succeeded", s)
		}
		if read(t, p) != s {
			t.Errorf("Install(%q) modified the file", s)
		}
	}
}

func TestSettingsPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg/claude")
	if p, _ := SettingsPath(); p != "/cfg/claude/settings.json" {
		t.Errorf("SettingsPath = %q", p)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, _ := os.UserHomeDir()
	if p, _ := SettingsPath(); p != filepath.Join(home, ".claude", "settings.json") {
		t.Errorf("SettingsPath = %q", p)
	}
}

func TestIsOurs(t *testing.T) {
	obj := func(cmd string) *object {
		return &object{members: []member{{"type", "command"}, {"command", cmd}}}
	}
	for cmd, want := range map[string]bool{
		"/usr/local/bin/telegram-agent-cli hook claude-pretooluse":   true,
		"'/a b/telegram-agent-cli' hook claude-pretooluse":           true,
		"telegram-agent-cli hook claude-pretooluse":                  true,
		"/usr/local/bin/other hook claude-pretooluse":                false,
		"/usr/local/bin/telegram-agent-cli hook claude-pretooluse x": false,
		"echo telegram-agent-cli hook claude-pretooluse":             false,
	} {
		if got := isOurs(obj(cmd)); got != want {
			t.Errorf("isOurs(%q) = %v", cmd, got)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	b := "1\n2\nthree\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n"
	want := "--- a/f\n+++ b/f\n@@ -1,6 +1,6 @@\n 1\n 2\n-3\n+three\n 4\n 5\n 6\n@@ -10,3 +10,4 @@\n 10\n 11\n 12\n+13\n"
	if got := unifiedDiff("f", a, b); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if unifiedDiff("f", a, a) != "" {
		t.Error("equal texts gave a diff")
	}
	if got := unifiedDiff("f", "", "x\n"); got != "--- a/f\n+++ b/f\n@@ -0,0 +1 @@\n+x\n" {
		t.Errorf("from empty: %q", got)
	}
}
