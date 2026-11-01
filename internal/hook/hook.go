package hook

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
)

// Input is the part of a Claude Code or Codex PreToolUse payload the guard reads.
type Input struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Cwd       string         `json:"cwd"`
	// wrapped: tool_input was not an object (Codex sends MCP arguments it can't
	// parse as one string); it is kept under "command".
	wrapped bool
}

// parseInput reads a payload; ok is false when the guard can't tell the tool
// or its input.
func parseInput(raw []byte) (in Input, ok bool) {
	var p struct {
		ToolName  *string         `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		Cwd       string          `json:"cwd"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ToolName == nil || *p.ToolName == "" {
		return in, false
	}
	in = Input{ToolName: *p.ToolName, Cwd: p.Cwd, ToolInput: map[string]any{}}
	if len(p.ToolInput) == 0 || string(p.ToolInput) == "null" {
		return in, true
	}
	var v any
	if json.Unmarshal(p.ToolInput, &v) != nil {
		return in, false
	}
	if m, isObj := v.(map[string]any); isObj {
		in.ToolInput = m
	} else {
		in.ToolInput = map[string]any{"command": v}
		in.wrapped = true
	}
	return in, true
}

// shellTools are the tool names (any case, without a "functions." prefix)
// whose input is a shell command: Claude Code's Bash and PowerShell, and the
// shell tools of Codex versions and other harnesses (exec_command, shell, local_shell…).
var shellTools = set("bash", "powershell", "exec_command", "unified_exec", "shell", "local_shell",
	"shell_command", "container.exec", "run_shell_command", "execute_command")

func isShellTool(name string) bool {
	return shellTools[strings.ToLower(strings.TrimPrefix(name, "functions."))]
}

// commandKeys hold a shell command (string or argv) in a tool's input.
var commandKeys = []string{"command", "cmd"}

// codeKeys hold code a tool runs (node_repl's js, a browser's run-code tool).
var codeKeys = []string{"code", "script"}

// commandOf returns the shell command in a tool input. present is true when a
// command key exists; ok is false when its value isn't a string or argv.
func commandOf(ti map[string]any) (cmd string, present, ok bool) {
	for _, k := range commandKeys {
		v, has := ti[k]
		if !has {
			continue
		}
		switch x := v.(type) {
		case string:
			return x, true, true
		case []any:
			argv := make([]string, 0, len(x))
			for _, e := range x {
				s, isStr := e.(string)
				if !isStr {
					return "", true, false
				}
				argv = append(argv, s)
			}
			if len(argv) == 0 {
				return "", true, false
			}
			return shellJoin(argv), true, true
		}
		return "", true, false
	}
	return "", false, false
}

var plainWordRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin quotes an argv as one shell command line.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if plainWordRe.MatchString(a) {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}

func reasonUnreadable(what string) string {
	return "Telegram account guard: could not read " + what + "; needs the account owner's explicit OK"
}

func commandReason(reasons []string) string {
	return "Telegram account action: needs the account owner's explicit OK for this exact command (" +
		strings.Join(reasons, "; ") + ")"
}

// Decision is the hook's stdout when it asks.
type Decision struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// fileTools are the tools whose path the guard checks.
var fileTools = set("Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep")

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// Evaluate returns the ask reason for a tool call, or "" to stay silent.
func Evaluate(in Input, o Options) string {
	if o.Cwd == "" {
		o.Cwd = in.Cwd
	}
	if in.ToolInput == nil {
		in.ToolInput = map[string]any{}
	}
	switch {
	case in.ToolName == "":
		return reasonUnreadable("the tool call")
	case in.ToolName == "Monitor":
		var reasons []string
		if cmd, present, ok := commandOf(in.ToolInput); ok {
			reasons = CheckBash(cmd, o)
		} else if present || mentionsAccount(in.ToolInput, o) {
			reasons = []string{"a Monitor call without a readable command mentions " + BinaryName + " or its data dir"}
		}
		if len(reasons) == 0 {
			return ""
		}
		return commandReason(reasons)
	case isShellTool(in.ToolName):
		cmd, _, ok := commandOf(in.ToolInput)
		if !ok {
			return reasonUnreadable("the command of this " + in.ToolName + " call")
		}
		if reasons := CheckBash(cmd, o); len(reasons) > 0 {
			return commandReason(reasons)
		}
		return ""
	case in.ToolName == "apply_patch":
		return "" // Codex's patch tool: EvaluateCodex reads the files it writes
	case fileTools[in.ToolName]:
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if v, has := in.ToolInput[k]; has && v != nil {
				if _, isStr := v.(string); !isStr {
					return reasonUnreadable("the path of this " + in.ToolName + " call")
				}
			}
		}
		var rs []string
		if r := CheckFile(in.ToolName, str(in.ToolInput, "file_path", "notebook_path", "path"), o); r != "" {
			rs = append(rs, r)
		}
		if r := CheckContent(in.ToolName, in.ToolInput, o); r != "" {
			rs = append(rs, r)
		}
		if len(rs) == 0 {
			return ""
		}
		r := strings.Join(rs, "; ")
		return "Telegram account data: needs the account owner's explicit OK for this exact file access (" + r + ")"
	}
	return checkOtherTool(in, o)
}

// checkOtherTool checks a tool the guard doesn't know (MCP and the like) by
// what its input carries: a shell command gets the Bash checks, code the
// interpreter-code checks.
func checkOtherTool(in Input, o Options) string {
	var reasons []string
	if cmd, _, ok := commandOf(in.ToolInput); ok {
		reasons = append(reasons, CheckBash(cmd, o)...)
	}
	for _, k := range codeKeys {
		if code, ok := in.ToolInput[k].(string); ok {
			reasons = append(reasons, CheckCode(code, o)...)
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	var rs reasonSet
	for _, r := range reasons {
		rs.add(r)
	}
	return "Telegram account action: needs the account owner's explicit OK for this " + in.ToolName + " call (" +
		strings.Join(rs.list, "; ") + ")"
}

// analysedKey reports whether checkOtherTool read ti[k] as a command or code.
func analysedKey(ti map[string]any, k string) bool {
	for _, c := range codeKeys {
		if _, ok := ti[c].(string); ok && k == c {
			return true
		}
	}
	if _, _, ok := commandOf(ti); !ok {
		return false
	}
	for _, c := range commandKeys {
		if _, has := ti[c]; has {
			return k == c // commandOf read the first present key
		}
	}
	return false
}

// mentionsAccount reports whether any part of a tool input names the binary or a data dir.
func mentionsAccount(ti map[string]any, o Options) bool {
	b, err := json.Marshal(ti)
	if err != nil {
		return true
	}
	text := string(b)
	if mentionRe.MatchString(text) {
		return true
	}
	for _, re := range dirPatterns(o) {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

const maxInput = 16 << 20

// DefaultDeadline bounds one check; past it the hook asks instead of waiting.
const DefaultDeadline = 3 * time.Second

// Run reads one PreToolUse payload from r and writes an "ask" decision to w when
// the call needs the owner's OK, when the payload can't be read, or when the
// check does not finish within the deadline.
func Run(r io.Reader, w io.Writer, o Options) error {
	return runHook(r, w, o, "ask", Evaluate)
}

// RunCodex is Run for Codex's PreToolUse hook. Codex has no "ask" (it treats it
// as a hook failure and runs the call), so every call Run would ask about is
// denied instead, with the reason. Codex's other tools (apply_patch, MCP calls)
// are denied when their input names a data dir or a settings/hook file.
func RunCodex(r io.Reader, w io.Writer, o Options) error {
	return runHook(r, w, o, "deny", EvaluateCodex)
}

// agentTools pass text to an agent that runs under the same hooks (or to the user).
var agentTools = set("spawn_agent", "send_input", "send_message", "followup_task", "update_plan", "request_user_input")

// EvaluateCodex is Evaluate plus Codex's tools. Codex (0.165) reports every
// exec_command as Bash {command}; write_stdin sends no PreToolUse, so a shell
// or REPL on the terminal is denied. A tool input the guard can't analyse that
// names the binary or a TGAC_ variable is denied: no way to tell what the tool
// does with it.
func EvaluateCodex(in Input, o Options) string {
	if o.Cwd == "" {
		o.Cwd = in.Cwd
	}
	o.TerminalStdin = true
	if r := Evaluate(in, o); r != "" {
		return r
	}
	if in.ToolName == "Monitor" || isShellTool(in.ToolName) || fileTools[in.ToolName] {
		return ""
	}
	if !agentTools[in.ToolName] && in.ToolName != "apply_patch" {
		for k, v := range in.ToolInput {
			if analysedKey(in.ToolInput, k) && !in.wrapped {
				continue
			}
			for _, s := range strValues(v) {
				if mentionRe.MatchString(s) || strings.Contains(s, "TGAC_") {
					return "Telegram account action: this " + in.ToolName + " call names " + BinaryName +
						" or a TGAC_ variable in input the guard can't analyse"
				}
			}
		}
	}
	b, err := json.Marshal(in.ToolInput)
	if err != nil {
		return "Telegram account data: the guard could not read this " + in.ToolName + " call"
	}
	text := string(b)
	for _, re := range dirPatterns(o) {
		if re.MatchString(text) {
			return "Telegram account data: " + in.ToolName + " " + reasonDataDir
		}
	}
	settings := "Telegram account guard: " + in.ToolName + " touches agent settings or hook config"
	if namesCodexHook(text, o) || settingsCodeRe.MatchString(text) {
		return settings
	}
	// apply_patch: the files a patch adds, updates, deletes or moves to.
	for _, v := range strValues(in.ToolInput) {
		for _, m := range patchFileRe.FindAllStringSubmatch(v, -1) {
			p := absPath(m[1], o)
			if settingsTarget(p, o) {
				return settings
			}
			if r := CheckFile("Write", p, o); r != "" {
				return "Telegram account data: " + in.ToolName + " " + r
			}
		}
	}
	return ""
}

// patchFileRe is a file line of an apply_patch body.
var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+?)[ \t\r]*$`)

// strValues returns every string inside v.
func strValues(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, strValues(e)...)
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, strValues(e)...)
		}
		return out
	}
	return nil
}

func runHook(r io.Reader, w io.Writer, o Options, decision string, eval func(Input, Options) string) error {
	deadline := o.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	done := make(chan string, 1)
	go func() { done <- evaluateRaw(r, o, eval) }()
	var reason string
	select {
	case reason = <-done:
	case <-time.After(deadline):
		reason = "Telegram account action: the guard did not finish checking this call in time; needs the account owner's explicit OK"
	}
	if reason == "" {
		return nil
	}
	var d Decision
	d.HookSpecificOutput.HookEventName = "PreToolUse"
	d.HookSpecificOutput.PermissionDecision = decision
	d.HookSpecificOutput.PermissionDecisionReason = reason
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(d)
}

// evaluateRaw reads and checks one payload. A payload it can't read, or a
// guard bug, gets a decision: the call might run a command or write a file.
func evaluateRaw(r io.Reader, o Options, eval func(Input, Options) string) (reason string) {
	raw, err := io.ReadAll(io.LimitReader(r, maxInput))
	if err != nil {
		return reasonUnreadable("the hook payload (" + err.Error() + ")")
	}
	defer func() {
		// A guard bug must not crash the hook, nor let the call through.
		if recover() != nil {
			reason = "Telegram account action: the guard could not check this call; needs the account owner's explicit OK"
		}
	}()
	in, ok := parseInput(raw)
	if !ok {
		return reasonUnreadable("the hook payload")
	}
	return eval(in, o)
}
