package hook

import (
	"encoding/json"
	"strings"
	"testing"
)

// e2e writes to allow-listed bots pass silently (the binary enforces the list);
// every way of changing that list asks, so an agent cannot add a chat and then
// send there without the owner's OK.
func e2eOptions() Options {
	o := testOptions()
	o.E2EAllowed = []string{"@my_test_bot"}
	return o
}

func TestE2EGuard(t *testing.T) {
	o := e2eOptions()
	pass := []string{
		`telegram-agent-cli e2e send text "e2e ping 42"`,
		"telegram-agent-cli e2e send text --chat @my_test_bot --message hi --wait reply --timeout 20",
		"telegram-agent-cli e2e send photo --path /tmp/p.jpg --caption 'describe this'",
		"telegram-agent-cli e2e send voice --path /tmp/v.ogg",
		"telegram-agent-cli e2e send location --lat 41.5 --long -8.4",
		"telegram-agent-cli e2e react --msg 5 --emoji 👍",
		"telegram-agent-cli e2e react --msg 5 --remove",
		"telegram-agent-cli e2e press --msg 5 --button B",
		"telegram-agent-cli e2e press --keyboard --button 'vault'",
		`telegram-agent-cli e2e web-app-data --button 0 --data '{"t":"x"}'`,
		"telegram-agent-cli e2e webapp-url --menu",
		"telegram-agent-cli e2e read messages --since 10",
		"telegram-agent-cli e2e read keyboard",
		"telegram-agent-cli e2e read pinned",
		"telegram-agent-cli e2e wait --for reply --timeout 20",
		"telegram-agent-cli e2e status",
		"telegram-agent-cli e2e send text hi --chat=my_test_bot",
		"telegram-agent-cli e2e react --chat MY_TEST_BOT --msg 1 --emoji 👍",
		"telegram-agent-cli e2e stop --chat https://t.me/my_test_bot --draft 5",
		"telegram-agent-cli e2e read messages --chat 12345",
		"telegram-agent-cli e2e wait --for reply --chat @anyone",
		"telegram-agent-cli e2e --help",
		"telegram-agent-cli e2e",
	}
	for _, c := range pass {
		if r := CheckBash(c, o); len(r) > 0 {
			t.Errorf("CheckBash(%q) = %v, want allow", c, r)
		}
	}
	ask := map[string]string{
		// an e2e write to a chat that is not literally on the list
		"telegram-agent-cli e2e send text hi --chat 337053497":              "not an e2e.allowed_chats entry",
		"telegram-agent-cli e2e send text hi --chat=@other_bot":             "not an e2e.allowed_chats entry",
		"telegram-agent-cli e2e press --msg 1 --button B --chat me":         "not an e2e.allowed_chats entry",
		"telegram-agent-cli e2e send text hi --chat $BOT":                   "expansion",
		"telegram-agent-cli e2e send text hi --chat @my_test_bot --chat @x": "not an e2e.allowed_chats entry",
		// changing the allow-list is a write, like a normal send
		"telegram-agent-cli e2e allow @someone":                         "e2e allow",
		"telegram-agent-cli e2e disallow @my_test_bot":                  "e2e disallow",
		"telegram-agent-cli e2e --chat x allow @someone":                "not on the read-only allowlist",
		"TGAC_READONLY=0 telegram-agent-cli e2e allow @someone":         "TGAC_READONLY",
		"sh -c 'telegram-agent-cli e2e allow @someone'":                 "e2e allow",
		"telegram-agent-cli e2e $SUB":                                   "expansion",
		"echo '{}' > ~/.config/telegram-agent-cli/config.json":          "data dir",
		"sed -i s/x/y/ /xhome/u/.config/telegram-agent-cli/config.json": "data dir",
		// an e2e send from another data dir (its own allow-list) or with writes switched on
		"TGAC_HOME=/tmp/fake telegram-agent-cli e2e send text hi": "TGAC_HOME",
		"TGAC_READONLY=0 telegram-agent-cli e2e send text hi":     "TGAC_READONLY",
		// the ordinary send path is unchanged
		"telegram-agent-cli send text --chat @my_test_bot --message hi": "send text",
	}
	for c, want := range ask {
		r := CheckBash(c, o)
		if len(r) == 0 {
			t.Errorf("CheckBash(%q) allowed, want ask (%s)", c, want)
			continue
		}
		if !strings.Contains(strings.Join(r, "; "), want) {
			t.Errorf("CheckBash(%q) = %v, want a reason containing %q", c, r, want)
		}
	}
	// Editing config.json with the file tools asks too.
	for _, tool := range []string{"Edit", "Write"} {
		if CheckFile(tool, testDir+"/config.json", o) == "" {
			t.Errorf("%s of config.json allowed", tool)
		}
	}
}

// Both guard paths: allow-listed e2e writes run with no decision (no prompt for
// an unattended worker); other writes ask on Claude Code and are denied on Codex.
func TestE2EGuardPaths(t *testing.T) {
	o := e2eOptions()
	for _, tc := range []struct {
		name     string
		run      func(r *strings.Reader, w *strings.Builder) error
		decision string
	}{
		{"claude", func(r *strings.Reader, w *strings.Builder) error { return Run(r, w, o) }, "ask"},
		{"codex", func(r *strings.Reader, w *strings.Builder) error { return RunCodex(r, w, o) }, "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := func(payload string) string {
				var out strings.Builder
				if err := tc.run(strings.NewReader(payload), &out); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			bash := func(cmd string) string {
				b, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}, "cwd": "/work"})
				return string(b)
			}
			for _, cmd := range []string{
				`telegram-agent-cli e2e send text "e2e ping 7"`,
				"telegram-agent-cli e2e press --msg 3 --button B --wait reply",
				"telegram-agent-cli e2e wait --for reaction --msg 3",
			} {
				if out := call(bash(cmd)); out != "" {
					t.Errorf("%q: %s, want no decision", cmd, out)
				}
			}
			for _, cmd := range []string{
				"telegram-agent-cli send text --chat 1 --message hi",
				"telegram-agent-cli e2e allow @someone",
				"telegram-agent-cli e2e disallow @my_test_bot",
				"TGAC_READONLY=0 telegram-agent-cli e2e send text hi",
				"telegram-agent-cli e2e send text hi --chat 337053497",
			} {
				var d Decision
				out := call(bash(cmd))
				if err := json.Unmarshal([]byte(out), &d); err != nil {
					t.Fatalf("%q: output %q: %v", cmd, out, err)
				}
				if h := d.HookSpecificOutput; h.HookEventName != "PreToolUse" || h.PermissionDecision != tc.decision {
					t.Errorf("%q: decision %+v, want %s", cmd, h, tc.decision)
				}
			}
		})
	}
}

// Codex-only tools: a patch touching the data dir or a hook config is denied;
// ordinary patches, even ones mentioning the binary, pass.
func TestCodexOtherTools(t *testing.T) {
	o := testOptions()
	patch := func(body string) Input {
		return Input{ToolName: "apply_patch", ToolInput: map[string]any{"input": body}}
	}
	deny := []Input{
		patch("*** Begin Patch\n*** Update File: " + testDir + "/config.json\n+x\n*** End Patch"),
		patch("*** Begin Patch\n*** Update File: ~/.config/telegram-agent-cli/config.json\n*** End Patch"),
		patch("*** Begin Patch\n*** Update File: /xhome/u/.codex/hooks.json\n*** End Patch"),
		patch("*** Begin Patch\n*** Update File: .codex/config.toml\n*** End Patch"),
		patch("*** Begin Patch\n*** Update File: /xhome/u/.claude/settings.json\n*** End Patch"),
	}
	for _, in := range deny {
		if EvaluateCodex(in, o) == "" {
			t.Errorf("EvaluateCodex(%v) passed", in.ToolInput)
		}
	}
	pass := []Input{
		patch("*** Begin Patch\n*** Update File: docs/reference.md\n+run telegram-agent-cli e2e send text\n*** End Patch"),
		{ToolName: "mcp__x__y", ToolInput: map[string]any{"q": "hello"}},
	}
	for _, in := range pass {
		if r := EvaluateCodex(in, o); r != "" {
			t.Errorf("EvaluateCodex(%v) = %q, want pass", in.ToolInput, r)
		}
	}
	// Writing a Codex hook config through Bash or the file tools asks (and so is denied on Codex).
	if len(CheckBash("echo '{}' > /xhome/u/.codex/hooks.json", o)) == 0 {
		t.Error("Bash write to ~/.codex/hooks.json allowed")
	}
	if CheckContent("Write", map[string]any{"file_path": "/work/.codex/config.toml", "content": "x = 1\n"}, o) == "" {
		t.Error("Write of .codex/config.toml allowed")
	}
	if len(CheckBash("cat /xhome/u/.codex/hooks.json", o)) != 0 {
		t.Error("reading ~/.codex/hooks.json asked")
	}
}
