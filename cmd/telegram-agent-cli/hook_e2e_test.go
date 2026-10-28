package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookEndToEnd builds the binary, installs the hook into a temp settings
// file and runs the installed command through sh, as Claude Code does.
func TestHookEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := filepath.Join(dir, "bin", "telegram-agent-cli")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	settings := filepath.Join(home, ".claude", "settings.json")

	run := func(stdin string, name string, args ...string) (string, int) {
		t.Helper()
		c := exec.Command(name, args...)
		c.Env = env
		c.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		c.Stdout = &out
		c.Stderr = os.Stderr
		err := c.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	}

	// Installing is a write: the binary refuses it in read-only mode.
	if out, code := run("", bin, "hook", "install"); code != 3 || !strings.Contains(out, `"code":"readonly"`) {
		t.Fatalf("read-only install: %d %s", code, out)
	}
	base := env
	env = append(append([]string{}, base...), "TGAC_READONLY=0")
	out, code := run("", bin, "hook", "install")
	env = base
	if code != 0 || !strings.Contains(out, `"changed":true`) {
		t.Fatalf("install: %d %s", code, out)
	}
	var s struct {
		Env   map[string]string `json:"env"`
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Env["TGAC_READONLY"] != "1" || len(s.Hooks.PreToolUse) != 2 {
		t.Fatalf("settings: %s", b)
	}
	cmds := map[string]string{}
	for _, e := range s.Hooks.PreToolUse {
		cmds[e.Matcher] = e.Hooks[0].Command
	}

	session := filepath.Join(home, ".config", "telegram-agent-cli", "session.json")
	for _, c := range []struct {
		matcher, payload string
		ask              bool
	}{
		{"Bash|Monitor", `{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli send text --chat 1 --message hi"}}`, true},
		{"Bash|Monitor", `{"tool_name":"Bash","tool_input":{"command":"TGAC_READONLY=0 telegram-agent-cli me"}}`, true},
		{"Bash|Monitor", `{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli messages list --chat 1"}}`, false},
		{"Bash|Monitor", `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"telegram-agent-cli send\""}}`, false},
		{"Read|Edit|Write|MultiEdit|NotebookEdit|Grep", `{"tool_name":"Read","tool_input":{"file_path":"` + session + `"}}`, true},
		{"Read|Edit|Write|MultiEdit|NotebookEdit|Grep", `{"tool_name":"Read","tool_input":{"file_path":"/etc/hosts"}}`, false},
		{"Bash|Monitor", `not json`, false},
		{"Bash|Monitor", `{"tool_name":"Monitor","tool_input":{"command":"telegram-agent-cli send text --chat 1 --message hi"}}`, true},
		{"Bash|Monitor", `{"tool_name":"Monitor","tool_input":{"command":"tail -f app.log"}}`, false},
	} {
		out, code := run(c.payload, "sh", "-c", cmds[c.matcher])
		if code != 0 {
			t.Errorf("%s: exit %d", c.payload, code)
		}
		if c.ask {
			var d map[string]map[string]string
			if err := json.Unmarshal([]byte(out), &d); err != nil || d["hookSpecificOutput"]["permissionDecision"] != "ask" {
				t.Errorf("%s: output %q", c.payload, out)
			}
		} else if out != "" {
			t.Errorf("%s: output %q, want none", c.payload, out)
		}
	}

	env = append(append([]string{}, base...), "TGAC_READONLY=0")
	if out, code := run("", bin, "hook", "uninstall"); code != 0 || !strings.Contains(out, `"changed":true`) {
		t.Fatalf("uninstall: %d %s", code, out)
	}
	if b, _ := os.ReadFile(settings); strings.TrimSpace(string(b)) != "{}" {
		t.Errorf("after uninstall: %s", b)
	}
}
