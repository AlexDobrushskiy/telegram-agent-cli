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
	// wrapped: tool_input was a plain string (Codex sends MCP arguments it
	// can't parse that way); it is kept under "command".
	wrapped bool
	// extra: top-level payload fields that aren't call metadata.
	extra map[string]any
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

// shellProse are shell-tool fields that describe the command to a person.
var shellProse = set("description", "justification")

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

// Evaluate returns the ask reason for a tool call, or "" to stay silent. Every
// string the payload carries is decided: the tool's own checks, then the
// fields they don't read (unknown tools, extra fields beside tool_input).
func Evaluate(in Input, o Options) string {
	if o.Cwd == "" {
		o.Cwd = in.Cwd
	}
	if in.ToolInput == nil {
		in.ToolInput = map[string]any{}
	}
	if in.ToolName == "" {
		return reasonUnreadable("the tool call")
	}
	if r := evaluateTool(in, o); r != "" {
		return r
	}
	return fieldScan{tool: in.ToolName, o: o, mention: true}.scan(in.extra)
}

func evaluateTool(in Input, o Options) string {
	ti := in.ToolInput
	switch {
	case in.ToolName == "Monitor":
		var reasons []string
		if cmds, present, ok := commandsOf(ti); ok {
			for _, c := range cmds {
				reasons = append(reasons, CheckBash(c, o)...)
			}
		} else if present || mentionsAccount(ti, o) {
			reasons = []string{"a Monitor call without a readable command mentions " + BinaryName + " or its data dir"}
		}
		if len(reasons) > 0 {
			return commandReason(reasons)
		}
		return shellFields(in, o)
	case isShellTool(in.ToolName):
		cmds, _, ok := commandsOf(ti)
		if !ok {
			return reasonUnreadable("the command of this " + in.ToolName + " call")
		}
		if wd := str(ti, "workdir", "cwd"); wd != "" {
			o.Cwd = absPath(wd, o)
		}
		var reasons []string
		for _, c := range cmds {
			reasons = append(reasons, CheckBash(c, o)...)
		}
		if len(reasons) > 0 {
			var rs reasonSet
			for _, r := range reasons {
				rs.add(r)
			}
			return commandReason(rs.list)
		}
		return shellFields(in, o)
	case strings.TrimPrefix(in.ToolName, "functions.") == "apply_patch":
		// Codex's patch tool: the files it adds, updates, deletes or moves to.
		// Its text is file content, so naming the binary is fine.
		return fieldScan{tool: in.ToolName, o: o}.scan(ti)
	case fileTools[in.ToolName]:
		return checkFileTool(in, o)
	}
	return checkOtherTool(in, o)
}

// shellFields scans a shell tool's other fields (not its command, cwd or prose).
func shellFields(in Input, o Options) string {
	skip := set("workdir", "cwd")
	for _, k := range commandKeys {
		skip[k] = true
	}
	for k := range shellProse {
		skip[k] = true
	}
	return fieldScan{tool: in.ToolName, o: o, mention: true, skip: skip}.scan(in.ToolInput)
}

func checkFileTool(in Input, o Options) string {
	ti := in.ToolInput
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if v, has := ti[k]; has && v != nil {
			if _, isStr := v.(string); !isStr {
				return reasonUnreadable("the path of this " + in.ToolName + " call")
			}
		}
	}
	p := str(ti, "file_path", "notebook_path", "path")
	if p == "" && in.ToolName != "Grep" {
		return reasonUnreadable("the path of this " + in.ToolName + " call")
	}
	var rs []string
	if r := CheckFile(in.ToolName, p, o); r != "" {
		rs = append(rs, r)
	}
	if r := CheckContent(in.ToolName, ti, o); r != "" {
		rs = append(rs, r)
	}
	if len(rs) == 0 {
		return ""
	}
	return "Telegram account data: needs the account owner's explicit OK for this exact file access (" +
		strings.Join(rs, "; ") + ")"
}

// checkOtherTool checks a tool the guard doesn't know (MCP and the like): a
// shell command gets the Bash checks, code the interpreter-code checks, a
// Telegram tool asks unless it reads, and every other string is scanned. A
// runner (exec, shell…) whose command the guard can't read is asked about.
func checkOtherTool(in Input, o Options) string {
	ti := in.ToolInput
	unreadable := reasonUnreadable("the command of this " + in.ToolName + " call")
	analysed := map[string]bool{}
	var rs reasonSet
	cmds, present, ok := commandsOf(ti)
	if present && !ok {
		return unreadable
	}
	for _, c := range cmds {
		for _, r := range CheckBash(c, o) {
			rs.add(r)
		}
	}
	for _, k := range commandKeys {
		_, has := ti[k]
		analysed[k] = has && !in.wrapped // a raw string may not be a command at all
	}
	if in.wrapped {
		for _, r := range CheckCode(cmds[0], o) {
			rs.add(r)
		}
	}
	for _, k := range codeKeys {
		v, has := ti[k]
		if !has {
			continue
		}
		code, isStr := v.(string)
		if !isStr {
			return unreadable
		}
		analysed[k] = true
		for _, r := range CheckCode(code, o) {
			rs.add(r)
		}
	}
	if len(cmds) == 0 && !anyTrue(analysed) && isRunner(in.ToolName) {
		return unreadable
	}
	rs.add(telegramTool(in))
	if len(rs.list) > 0 {
		return "Telegram account action: needs the account owner's explicit OK for this " + in.ToolName + " call (" +
			strings.Join(rs.list, "; ") + ")"
	}
	return fieldScan{tool: in.ToolName, o: o, mention: !noMention[in.ToolName], analysed: analysed}.scan(ti)
}

func anyTrue(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
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
// denied instead, with the reason.
func RunCodex(r io.Reader, w io.Writer, o Options) error {
	return runHook(r, w, o, "deny", EvaluateCodex)
}

// EvaluateCodex is Evaluate for Codex. Codex (0.165) reports every
// exec_command as Bash {command}; write_stdin sends no PreToolUse, so a shell
// or REPL on the terminal is denied as well.
func EvaluateCodex(in Input, o Options) string {
	o.TerminalStdin = true
	return Evaluate(in, o)
}

// patchFileRe is a file line of an apply_patch body.
var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+?)[ \t\r]*$`)

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
