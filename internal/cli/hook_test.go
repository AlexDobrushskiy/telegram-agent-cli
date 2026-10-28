package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookPreToolUse(t *testing.T) {
	h := newHarness(t)
	session := filepath.Join(h.app.Paths.Home, "session.json")
	cases := map[string]bool{
		`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli send text --chat 1 --message hi"}}`: true,
		`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli chats list"}}`:                      false,
		`{"tool_name":"Read","tool_input":{"file_path":"` + session + `"}}`:                                  true,
		`{"tool_name":"Bash","tool_input":{"command":"cat ` + session + `"}}`:                                true,
		`garbage`: false,
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
	// unexpected args never fail the hook
	out := &bytes.Buffer{}
	h.app.Stdout = out
	h.app.Stdin = strings.NewReader("")
	if code := h.app.Execute(context.Background(), []string{"hook", "claude-pretooluse", "--bogus", "x"}); code != 0 || out.Len() != 0 {
		t.Errorf("bad args: exit %d out %q", code, out.String())
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
