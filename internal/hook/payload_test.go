package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookCall runs one payload through the Claude (ask) or Codex (deny) hook and
// returns the decision ("" = no output).
func hookCall(t *testing.T, codex bool, o Options, payload string) string {
	t.Helper()
	var out strings.Builder
	run := Run
	if codex {
		run = RunCodex
	}
	if err := run(strings.NewReader(payload), &out, o); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return ""
	}
	var d Decision
	if err := json.Unmarshal([]byte(out.String()), &d); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if d.HookSpecificOutput.HookEventName != "PreToolUse" || d.HookSpecificOutput.PermissionDecisionReason == "" {
		t.Fatalf("output %q", out.String())
	}
	return d.HookSpecificOutput.PermissionDecision
}

func payload(tool string, input any) string {
	b, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": input, "cwd": "/work"})
	return string(b)
}

// withInput returns a captured payload with its tool_input replaced.
func withInput(t *testing.T, raw []byte, input any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["tool_input"] = input
	b, _ := json.Marshal(m)
	return string(b)
}

const (
	sendOther = "telegram-agent-cli e2e send text x --chat me"
	sendOK    = `telegram-agent-cli e2e send text "e2e ping 7"`
)

// Payloads captured from codex-cli 0.165.0 (codex exec and the interactive TUI,
// hooks.PreToolUse matcher ".*"). Codex reports every exec_command as Bash
// {command}, apply_patch as {command: patch}, MCP calls as mcp__server__tool
// with their arguments; write_stdin and code-mode wait send no PreToolUse.
func TestCodexCapturedPayloads(t *testing.T) {
	o := e2eOptions()
	files, err := filepath.Glob("testdata/codex-0.165/*.json")
	if err != nil || len(files) < 8 {
		t.Fatalf("fixtures: %v %v", files, err)
	}
	want := map[string]string{
		// a bare shell on a tty: Codex's write_stdin types into it unchecked
		"Bash-zsh.json": "deny",
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		if got := hookCall(t, true, o, string(raw)); got != want[name] {
			t.Errorf("%s as captured: %q, want %q", name, got, want[name])
		}
		var in struct {
			ToolName string `json:"tool_name"`
		}
		_ = json.Unmarshal(raw, &in)
		// the same envelope carrying an action the guard must stop, and one it must not
		var bad, good any
		switch {
		case in.ToolName == "Bash":
			bad, good = map[string]any{"command": sendOther}, map[string]any{"command": sendOK}
		case in.ToolName == "apply_patch":
			bad = map[string]any{"command": "*** Begin Patch\n*** Add File: " + testDir + "/config.json\n+{}\n*** End Patch"}
			good = map[string]any{"command": "*** Begin Patch\n*** Add File: /work/notes.md\n+run telegram-agent-cli e2e send text\n*** End Patch"}
		case strings.HasPrefix(in.ToolName, "mcp__"):
			bad = map[string]any{"code": `const {execSync} = await import("node:child_process"); execSync("` + sendOther + `")`}
			good = map[string]any{"code": `const {execSync} = await import("node:child_process"); execSync("telegram-agent-cli chats list")`}
		default:
			t.Fatalf("%s: unexpected tool %q", name, in.ToolName)
		}
		if got := hookCall(t, true, o, withInput(t, raw, bad)); got != "deny" {
			t.Errorf("%s carrying %v: %q, want deny", name, bad, got)
		}
		if got := hookCall(t, true, o, withInput(t, raw, good)); got != "" {
			t.Errorf("%s carrying %v: %q, want no decision", name, good, got)
		}
	}
}

// e2e r1 row 11: Codex-native envelopes (exec_command / functions.exec_command
// with cmd) got no decision. Verbatim reproduction payloads.
func TestE2ER1NativeEnvelopes(t *testing.T) {
	o := e2eOptions()
	for _, p := range []string{
		`{"tool_name":"exec_command","tool_input":{"cmd":"telegram-agent-cli e2e send text x --chat me"}}`,
		`{"tool_name":"functions.exec_command","tool_input":{"cmd":"telegram-agent-cli e2e send text x --chat me"}}`,
		`{"tool_name":"exec_command","tool_input":{"cmd":"TGAC_READONLY=0 telegram-agent-cli e2e allow @not_allowed_e2e_test_bot"}}`,
		`{"tool_name":"functions.exec_command","tool_input":{"cmd":"TGAC_READONLY=0 telegram-agent-cli e2e allow @not_allowed_e2e_test_bot"}}`,
	} {
		if got := hookCall(t, true, o, p); got != "deny" {
			t.Errorf("codex %s: %q, want deny", p, got)
		}
		if got := hookCall(t, false, o, p); got != "ask" {
			t.Errorf("claude %s: %q, want ask", p, got)
		}
	}
}

// Every shell-capable shape gets the Bash checks, on both hooks: an allow-listed
// e2e send runs with no decision, anything else is asked (Claude) or denied (Codex).
func TestShellShapes(t *testing.T) {
	o := e2eOptions()
	shapes := map[string]func(cmd string) string{
		"Bash command":     func(c string) string { return payload("Bash", map[string]any{"command": c}) },
		"Bash cmd":         func(c string) string { return payload("Bash", map[string]any{"cmd": c}) },
		"exec_command cmd": func(c string) string { return payload("exec_command", map[string]any{"cmd": c, "tty": true}) },
		"functions.exec_command": func(c string) string {
			return payload("functions.exec_command", map[string]any{"cmd": c, "yield_time_ms": 1000})
		},
		"shell argv": func(c string) string { return payload("shell", map[string]any{"command": []string{"bash", "-lc", c}}) },
		"local_shell argv": func(c string) string {
			return payload("local_shell", map[string]any{"command": []string{"zsh", "-c", c}})
		},
		"container.exec": func(c string) string {
			return payload("container.exec", map[string]any{"cmd": []string{"bash", "-lc", c}})
		},
		"shell_command":    func(c string) string { return payload("shell_command", map[string]any{"command": c}) },
		"PowerShell":       func(c string) string { return payload("PowerShell", map[string]any{"command": c}) },
		"unknown tool cmd": func(c string) string { return payload("mcp__runner__run", map[string]any{"cmd": c}) },
		"unknown tool command": func(c string) string {
			return payload("mcp__runner__exec", map[string]any{"command": c, "timeout": 5})
		},
		"plain argv": func(c string) string {
			return payload("shell", map[string]any{"command": append([]string{}, strings.Fields(c)...)})
		},
	}
	for name, mk := range shapes {
		for _, codex := range []bool{false, true} {
			deny := "ask"
			if codex {
				deny = "deny"
			}
			if name != "plain argv" { // quoting: sendOK has a quoted argument
				if got := hookCall(t, codex, o, mk(sendOK)); got != "" {
					t.Errorf("%s codex=%v allow-listed send: %q, want no decision", name, codex, got)
				}
			}
			for _, c := range []string{sendOther, "TGAC_READONLY=0 telegram-agent-cli e2e allow @x", "cat " + testDir + "/session.json"} {
				if got := hookCall(t, codex, o, mk(c)); got != deny {
					t.Errorf("%s codex=%v %q: %q, want %s", name, codex, c, got, deny)
				}
			}
			if got := hookCall(t, codex, o, mk("ls -la")); got != "" {
				t.Errorf("%s codex=%v ls: %q, want no decision", name, codex, got)
			}
		}
	}
}

// A payload the guard can't read is decided, never let through silently.
func TestUnreadablePayloadsFailClosed(t *testing.T) {
	o := e2eOptions()
	for _, p := range []string{
		`not json`,
		``,
		`[]`,
		`{"tool_input":{"command":"ls"}}`,
		`{"tool_name":"Bash"}`,
		`{"tool_name":"Bash","tool_input":null}`,
		`{"tool_name":"Bash","tool_input":{"command":42}}`,
		`{"tool_name":"Bash","tool_input":{"command":["ls",7]}}`,
		`{"tool_name":"Bash","tool_input":"telegram-agent-cli send text --chat 1 --message hi"}`,
		`{"tool_name":"exec_command","tool_input":{"cmd":{"argv":"ls"}}}`,
		`{"tool_name":"exec_command","tool_input":{}}`,
		`{"tool_name":"shell","tool_input":{"command":[]}}`,
		`{"tool_name":"Write","tool_input":{"file_path":["x"],"content":"y"}}`,
		`{"tool_name":"Read","tool_input":{"file_path":7}}`,
		// Codex passes MCP arguments it can't parse as one string
		`{"tool_name":"mcp__x__y","tool_input":"telegram-agent-cli send text --chat 1 --message hi"}`,
	} {
		if got := hookCall(t, true, o, p); got != "deny" {
			t.Errorf("codex %.70q: %q, want deny", p, got)
		}
		if got := hookCall(t, false, o, p); got != "ask" {
			t.Errorf("claude %.70q: %q, want ask", p, got)
		}
	}
	for _, p := range []string{
		`{"tool_name":"mcp__x__y","tool_input":"just text"}`,
		`{"tool_name":"WebFetch","tool_input":{"url":"https://example.com"}}`,
		`{"tool_name":"Grep","tool_input":{"pattern":"x"}}`,
		`{"tool_name":"mcp__x__y"}`,
	} {
		for _, codex := range []bool{false, true} {
			if got := hookCall(t, codex, o, p); got != "" {
				t.Errorf("codex=%v %s: %q, want no decision", codex, p, got)
			}
		}
	}
}

// Code a tool runs (node_repl, a browser's run-code tool) gets the interpreter
// code checks on both hooks. Codex also denies any other tool input that names
// the binary or its variables: it can't tell what the tool does with them.
func TestCodeAndUnknownTools(t *testing.T) {
	o := e2eOptions()
	both := map[string]string{
		`execSync("telegram-agent-cli send text --chat 1 --message hi")`:                              "send text",
		`spawnSync("telegram-agent-cli", ["auth", "logout"])`:                                         "can't resolve",
		`execSync("telegram-agent-cli e2e send text x", {env: {...process.env, TGAC_READONLY: "0"}})`: "TGAC_READONLY",
		`const b = "telegram-agent-cli"; execSync(b + " send text --chat 1")`:                         "can't resolve",
		`fs.writeFileSync(process.env.HOME + "/.codex/hooks.json", "{}")`:                             "Codex hook config",
		`fs.readFileSync("` + testDir + `/session.json")`:                                             "data dir",
	}
	for code, want := range both {
		for _, tool := range []string{"mcp__node_repl__js", "mcp__pw-lane__browser_run_code_unsafe"} {
			for _, codex := range []bool{false, true} {
				deny := map[bool]string{false: "ask", true: "deny"}[codex]
				p := payload(tool, map[string]any{"code": code, "title": "t"})
				if got := hookCall(t, codex, o, p); got != deny {
					t.Errorf("%s codex=%v %q: %q, want %s (%s)", tool, codex, code, got, deny, want)
				}
			}
		}
		var in Input
		_ = json.Unmarshal([]byte(payload("mcp__node_repl__js", map[string]any{"code": code})), &in)
		if r := EvaluateCodex(in, o); !strings.Contains(r, want) {
			t.Errorf("%q: reason %q, want %q", code, r, want)
		}
	}
	for _, code := range []string{`1+1`, `execSync("telegram-agent-cli chats list")`, `execSync("` + sendOK + `")`} {
		for _, codex := range []bool{false, true} {
			if got := hookCall(t, codex, o, payload("mcp__node_repl__js", map[string]any{"code": code})); got != "" {
				t.Errorf("codex=%v %q: %q, want no decision", codex, code, got)
			}
		}
	}
	// other inputs naming the binary: Codex denies, Claude (Agent prompts, notes) stays silent
	for _, p := range []string{
		payload("mcp__x__y", map[string]any{"q": "telegram-agent-cli send text --chat 1"}),
		payload("mcp__x__y", map[string]any{"steps": []any{map[string]any{"run": "TGAC_READONLY=0 x"}}}),
	} {
		if got := hookCall(t, true, o, p); got != "deny" {
			t.Errorf("codex %s: %q, want deny", p, got)
		}
		if got := hookCall(t, false, o, p); got != "" {
			t.Errorf("claude %s: %q, want no decision", p, got)
		}
	}
	// prompts to agents that run under the same hooks (Codex spawn_agent, Claude Agent)
	agent := map[bool]string{
		true:  payload("spawn_agent", map[string]any{"message": "read the chat list with telegram-agent-cli chats list"}),
		false: payload("Agent", map[string]any{"prompt": "use telegram-agent-cli to read the chat list"}),
	}
	for codex, p := range agent {
		if got := hookCall(t, codex, o, p); got != "" {
			t.Errorf("codex=%v %s: %q, want no decision", codex, p, got)
		}
	}
}

// Codex's write_stdin sends no PreToolUse, so a shell or REPL started on a tty
// would run whatever is typed into it unchecked: Codex denies starting one.
// Claude's Bash has no terminal stdin, so there it stays allowed.
func TestCodexTerminalStdin(t *testing.T) {
	o := e2eOptions()
	deny := []string{
		"zsh -f", "bash", "bash -i", "sh -s", "bash -", "env zsh", "exec bash -l", "python3", "python3 -i", "node",
		"ruby", "ssh black-lan", "ssh -t black-lan", "cd /tmp && bash", "osascript",
		"tmux", "tmux new -s x", "tmux attach -t x", "tmux a",
	}
	for _, c := range deny {
		if got := hookCall(t, true, o, payload("Bash", map[string]any{"command": c})); got != "deny" {
			t.Errorf("codex %q: %q, want deny", c, got)
		}
		if got := hookCall(t, false, o, payload("Bash", map[string]any{"command": c})); got != "" {
			t.Errorf("claude %q: %q, want no decision", c, got)
		}
	}
	pass := []string{
		"bash --version", "node --version", "node -v", "python3 -V", "python3 --help", "zsh -c 'echo hi'",
		"echo 'echo hi' | bash", "bash <<<'echo hi'", "python3 -m http.server", "python3 -c 'print(1)'",
		"ssh black-lan uptime", "ssh -N -L 8080:localhost:80 black-lan", "cat", "bash < /work/scripts/ok.sh",
		"tmux new -d -s x", "tmux new-session -ds x", "tmux new -s x htop", "tmux ls", "tmux kill-server",
	}
	for _, c := range pass {
		if got := hookCall(t, true, o, payload("Bash", map[string]any{"command": c})); got != "" {
			t.Errorf("codex %q: %q, want no decision", c, got)
		}
	}
}

// A shell or interpreter reading its code through "<" runs that file: the
// guard reads it like a script argument.
func TestStdinRedirectScript(t *testing.T) {
	o := e2eOptions()
	for c, want := range map[string]string{
		"bash < scripts/bad.sh":       "send text",
		"sh </work/scripts/bad.sh":    "send text",
		"zsh -s < scripts/bad":        "auth logout",
		"bash < $SCRIPT":              "",
		"python3 < scripts/bad.py":    "send text",
		"ssh black-lan < scripts/bad": "auth logout",
	} {
		r := strings.Join(CheckBash(c+" && telegram-agent-cli me", o), "; ")
		if want == "" {
			if r == "" {
				t.Errorf("%q: allowed, want ask", c)
			}
			continue
		}
		if !strings.Contains(r, want) {
			t.Errorf("%q: %q, want %q", c, r, want)
		}
		if r := strings.Join(CheckBash(c, o), "; "); !strings.Contains(r, want) {
			t.Errorf("%q alone: %q, want %q", c, r, want)
		}
	}
	if r := CheckBash("bash < scripts/ok.sh", o); len(r) != 0 {
		t.Errorf("harmless script on stdin: %v", r)
	}
}
