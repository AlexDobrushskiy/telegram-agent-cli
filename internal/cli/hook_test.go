package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/hook"
)

func TestHookPreToolUse(t *testing.T) {
	h := newHarness(t)
	session := filepath.Join(h.app.Paths.Home, "session.json")
	cases := map[string]bool{
		`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli send text --chat 1 --message hi"}}`: true,
		`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli chats list"}}`:                      false,
		`{"tool_name":"Read","tool_input":{"file_path":"` + session + `"}}`:                                  true,
		`{"tool_name":"Bash","tool_input":{"command":"cat ` + session + `"}}`:                                true,
		// a payload the guard can't read might run anything: fail closed
		`garbage`: true,
	}
	for payload, ask := range cases {
		out := &bytes.Buffer{}
		h.app.Stdout = out
		h.app.Stdin = strings.NewReader(payload)
		if code := h.app.Execute(context.Background(), []string{"hook", "claude-pretooluse"}); code != 0 {
			t.Errorf("%s: exit %d", payload, code)
		}
		if got := strings.Contains(out.String(), `"permissionDecision":"ask"`); got != ask {
			t.Errorf("%s: output %q, want ask=%v", payload, out.String(), ask)
		}
	}
	// --help alone prints the command's help, not a decision
	out0 := &bytes.Buffer{}
	h.app.Stdout = out0
	h.app.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli send text"}}`)
	if code := h.app.Execute(context.Background(), []string{"hook", "claude-pretooluse", "--help"}); code != 0 ||
		!strings.Contains(out0.String(), "PreToolUse hook") || strings.Contains(out0.String(), `"permissionDecision":"ask"`) {
		t.Errorf("--help: exit %d out %q", code, out0.String())
	}

	// unexpected args never fail the hook; they are ignored and the payload decides
	for _, cmd := range []string{"claude-pretooluse", "codex-pretooluse"} {
		out := &bytes.Buffer{}
		h.app.Stdout = out
		h.app.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
		if code := h.app.Execute(context.Background(), []string{"hook", cmd, "--bogus", "x"}); code != 0 || out.Len() != 0 {
			t.Errorf("%s bad args: exit %d out %q", cmd, code, out.String())
		}
	}
	// e2e r1 row 11 through the command: Codex-native envelopes are denied
	out := &bytes.Buffer{}
	h.app.Stdout = out
	h.app.Stdin = strings.NewReader(`{"tool_name":"exec_command","tool_input":{"cmd":"telegram-agent-cli e2e send text x --chat me"}}`)
	if code := h.app.Execute(context.Background(), []string{"hook", "codex-pretooluse"}); code != 0 ||
		!strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Errorf("codex exec_command: exit %d out %q", code, out.String())
	}
}

func TestHookInstallCommand(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "telegram-agent-cli")
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.json")

	d := h.ok("hook", "install", "--settings", settings, "--binary", bin, "--dry-run")
	if d["dry_run"] != true || d["changed"] != true || !strings.Contains(d["diff"].(string), bin+" hook claude-pretooluse") {
		t.Fatalf("dry run %v", d)
	}
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("dry run wrote the settings")
	}

	// A real install is a write: refused in read-only mode, before touching the file.
	h.fail(3, "readonly", "hook", "install", "--settings", settings, "--binary", bin)
	h.fail(3, "readonly", "hook", "uninstall", "--settings", settings)
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("read-only install wrote the settings")
	}
	h.ok("hook", "uninstall", "--settings", settings, "--dry-run")

	t.Setenv(config.EnvReadOnly, "0")
	d = h.ok("hook", "install", "--settings", settings, "--binary", bin)
	if d["changed"] != true || d["result"] != nil {
		t.Fatalf("install %v", d)
	}
	var s map[string]any
	b, _ := os.ReadFile(settings)
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s["env"].(map[string]any)["TGAC_READONLY"] != "1" {
		t.Errorf("settings %s", b)
	}
	if d = h.ok("hook", "install", "--settings", settings, "--binary", bin); d["changed"] != false {
		t.Errorf("reinstall %v", d)
	}
	if d = h.ok("hook", "uninstall", "--settings", settings); d["changed"] != true || d["backup"] == "" {
		t.Errorf("uninstall %v", d)
	}

	h.fail(2, "usage", "hook", "install", "--settings", settings, "--binary", filepath.Join(dir, "missing"))
	executable = func() (string, error) { return "/tmp/go-build123/b001/exe/telegram-agent-cli", nil }
	defer func() { executable = os.Executable }()
	h.fail(2, "usage", "hook", "install", "--settings", settings)
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	h.fail(2, "usage", "hook", "uninstall", "--settings", bad)
}

// TestHookAgreesWithBinary: the guard hook passes every command the binary
// runs as a read (including the e2e and watch commands that are their own gate)
// without a prompt, and asks before every write. A command added to one list
// and not the other fails here.
func TestHookAgreesWithBinary(t *testing.T) {
	h := newHarness(t)
	leaves := map[string]bool{}
	leafPaths(h.app.Root(), "", leaves)
	o := hook.Options{DataDirs: []string{"/nonexistent/tgac"}, UserHome: "/nonexistent", Cwd: "/tmp"}
	for path := range leaves {
		r := hook.CheckBash("telegram-agent-cli "+path, o)
		switch {
		case readCommands[path] && len(r) > 0:
			t.Errorf("read %q asks in the hook: %v", path, r)
		case !readCommands[path] && !dryRunReads[path] && len(r) == 0:
			t.Errorf("write %q passes the hook without a prompt", path)
		}
	}
}
