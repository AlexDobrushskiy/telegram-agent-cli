package hook

import (
	"encoding/json"
	"io"
	"strings"
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
		r := CheckFile(in.ToolName, str(in.ToolInput, "file_path", "notebook_path", "path"), o)
		if r == "" {
			return ""
		}
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

// Run reads one PreToolUse payload from r and writes an "ask" decision to w when
// the call needs the owner's OK. Bad input produces no output.
func Run(r io.Reader, w io.Writer, o Options) (err error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxInput))
	if err != nil {
		return nil
	}
	var reason string
	func() {
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
			return
		}
		reason = Evaluate(in, o)
	}()
	if reason == "" {
		return nil
	}
	var d Decision
	d.HookSpecificOutput.HookEventName = "PreToolUse"
	d.HookSpecificOutput.PermissionDecision = "ask"
	d.HookSpecificOutput.PermissionDecisionReason = reason
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(d)
}
