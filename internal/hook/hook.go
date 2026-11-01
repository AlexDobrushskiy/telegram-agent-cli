package hook

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
)

// Input is the part of the Claude Code PreToolUse payload the guard reads.
type Input struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Cwd       string         `json:"cwd"`
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
	switch {
	case in.ToolName == "Bash" || in.ToolName == "Monitor":
		var reasons []string
		if cmd, ok := in.ToolInput["command"].(string); ok {
			reasons = CheckBash(cmd, o)
		} else if in.ToolName == "Monitor" && mentionsAccount(in.ToolInput, o) {
			reasons = []string{"a Monitor call without a command field mentions " + BinaryName + " or its data dir"}
		}
		if len(reasons) == 0 {
			return ""
		}
		return "Telegram account action: needs the account owner's explicit OK for this exact command (" +
			strings.Join(reasons, "; ") + ")"
	case fileTools[in.ToolName]:
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
	return ""
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
// the call needs the owner's OK, or when the check does not finish within the
// deadline. Bad input produces no output.
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

// EvaluateCodex is Evaluate plus a check of tools Claude Code doesn't have.
func EvaluateCodex(in Input, o Options) string {
	if r := Evaluate(in, o); r != "" {
		return r
	}
	if in.ToolName == "Bash" || in.ToolName == "Monitor" || fileTools[in.ToolName] {
		return ""
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
	if codexHookRe.MatchString(text) || settingsCodeRe.MatchString(text) {
		return "Telegram account guard: " + in.ToolName + " touches agent settings or hook config"
	}
	return ""
}

// codexHookRe names a Codex hook config file.
var codexHookRe = regexp.MustCompile(`(?i)\.codex[/\\]+(?:hooks\.json|config\.toml)`)

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

// evaluateRaw reads and checks one payload; bad input gives "".
func evaluateRaw(r io.Reader, o Options, eval func(Input, Options) string) (reason string) {
	raw, err := io.ReadAll(io.LimitReader(r, maxInput))
	if err != nil {
		return ""
	}
	defer func() {
		// A guard bug must not crash the hook; ask only if the call looks related.
		if recover() != nil {
			low := strings.ToLower(string(raw))
			if strings.Contains(low, BinaryName) || strings.Contains(low, "tgac_") {
				reason = "Telegram account action: the guard could not check this call; needs the account owner's explicit OK"
			}
		}
	}()
	var in Input
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	return eval(in, o)
}
