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
//
// The guard asks only about what it can see the call do: run the binary with
// a write subcommand, set TGAC_READONLY to a value that turns read-only off,
// name a path inside the data dir, or call a Telegram tool that may write.
// Code it can't see (a script that doesn't exist yet, a program named by an
// expansion, interpreter code) passes: the binary's own write gate refuses
// every write while read-only is on.
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
	ti := in.ToolInput
	switch {
	case in.ToolName == "Monitor" || isShellTool(in.ToolName):
		cmds, present, ok := commandsOf(ti)
		if !ok && (present || in.ToolName != "Monitor") {
			return reasonUnreadable("the command of this " + in.ToolName + " call")
		}
		if wd := str(ti, "workdir", "cwd"); wd != "" {
			o.Cwd = absPath(wd, o)
		}
		var rs reasonSet
		for _, c := range cmds {
			for _, r := range CheckBash(c, o) {
				rs.add(r)
			}
		}
		if len(rs.list) > 0 {
			return commandReason(rs.list)
		}
		return ""
	case strings.TrimPrefix(in.ToolName, "functions.") == "apply_patch":
		// Codex's patch tool: the files it adds, updates, deletes or moves to.
		return fieldScan{tool: in.ToolName, o: o}.scan(ti)
	case fileTools[in.ToolName]:
		return checkFileTool(in, o)
	}
	return checkOtherTool(in, o)
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
	r := CheckFile(in.ToolName, p, o)
	if r == "" {
		return ""
	}
	return "Telegram account data: needs the account owner's explicit OK for this exact file access (" + r + ")"
}

// checkOtherTool checks a tool the guard doesn't know (MCP and the like): a
// shell command gets the Bash checks, a Telegram tool asks unless it reads,
// and the rest is scanned by role (fieldScan).
func checkOtherTool(in Input, o Options) string {
	var rs reasonSet
	cmds, present, ok := commandsOf(in.ToolInput)
	unreadable := reasonUnreadable("the command of this " + in.ToolName + " call")
	if present && !ok {
		return unreadable
	}
	if !present && isRunner(in.ToolName) {
		// a runner without a command field must carry code (a REPL, a run-code tool)
		code, hasCode := in.ToolInput["code"].(string)
		script, hasScript := in.ToolInput["script"].(string)
		if !hasCode && !hasScript || code == "" && script == "" {
			return unreadable
		}
	}
	for _, c := range cmds {
		for _, r := range CheckBash(c, o) {
			rs.add(r)
		}
	}
	rs.add(telegramTool(in))
	if len(rs.list) > 0 {
		return "Telegram account action: needs the account owner's explicit OK for this " + in.ToolName + " call (" +
			strings.Join(rs.list, "; ") + ")"
	}
	return fieldScan{tool: in.ToolName, o: o}.scan(in.ToolInput)
}

const maxInput = 16 << 20

// DefaultDeadline bounds one check; past it the hook asks instead of waiting.
// It stays under the 10 s hook timeout the installer writes, with room for a
// loaded machine: a check that runs out of time stops the agent for an answer.
const DefaultDeadline = 8 * time.Second

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

// EvaluateCodex is Evaluate for Codex, which reports every exec_command as
// Bash {command}.
func EvaluateCodex(in Input, o Options) string {
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
