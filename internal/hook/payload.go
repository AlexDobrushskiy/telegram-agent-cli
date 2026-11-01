package hook

import (
	"encoding/json"
	"regexp"
	"strings"
)

// metaKeys are the payload fields that describe a call (Claude Code and
// Codex), not what it does. Every other top-level field is scanned.
var metaKeys = set("hook_event_name", "tool_name", "tool_input", "cwd", "session_id", "transcript_path",
	"permission_mode", "tool_use_id", "model", "turn_id", "effort", "prompt_id", "agent_id", "agent_type")

// parseInput reads a payload; ok is false when the guard can't tell the tool
// or its input.
func parseInput(raw []byte) (in Input, ok bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || json.Unmarshal(p["tool_name"], &in.ToolName) != nil || in.ToolName == "" {
		return in, false
	}
	if c, has := p["cwd"]; has && string(c) != "null" && json.Unmarshal(c, &in.Cwd) != nil {
		return in, false
	}
	if in.ToolInput, in.wrapped, ok = decodeToolInput(p["tool_input"]); !ok {
		return in, false
	}
	for k, v := range p {
		if metaKeys[k] {
			continue
		}
		var x any
		_ = json.Unmarshal(v, &x)
		if in.extra == nil {
			in.extra = map[string]any{}
		}
		in.extra[k] = x
	}
	return in, true
}

// decodeToolInput reads tool_input: an object, or a JSON string holding one.
// Any other string is kept under "command" (wrapped): Codex passes MCP
// arguments it can't parse that way. Numbers, arrays and JSON-looking text
// that isn't one object are unreadable.
func decodeToolInput(raw json.RawMessage) (m map[string]any, wrapped, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, false, true
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false, false
	}
	switch x := v.(type) {
	case map[string]any:
		return x, false, true
	case string:
		if obj, isJSON := jsonObject(x); isJSON {
			return obj, false, obj != nil
		}
		return map[string]any{"command": x}, true, true
	}
	return nil, false, false
}

// jsonObject decodes s when it looks like JSON (starts with { or [); obj is
// nil when it isn't exactly one object.
func jsonObject(s string) (obj map[string]any, isJSON bool) {
	t := strings.TrimSpace(s)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return nil, false
	}
	var v any
	if json.Unmarshal([]byte(t), &v) != nil {
		return nil, true
	}
	obj, _ = v.(map[string]any)
	return obj, true
}

// commandsOf returns every shell command in a tool input (command and cmd
// may both be present and differ). present is true when a command key
// exists; ok is false when none does or one isn't a string or argv.
func commandsOf(ti map[string]any) (cmds []string, present, ok bool) {
	ok = true
	for _, k := range commandKeys {
		v, has := ti[k]
		if !has {
			continue
		}
		present = true
		switch x := v.(type) {
		case string:
			cmds = append(cmds, x)
			continue
		case []any:
			argv := make([]string, 0, len(x))
			for _, e := range x {
				if s, isStr := e.(string); isStr {
					argv = append(argv, s)
				}
			}
			if len(argv) > 0 && len(argv) == len(x) {
				cmds = append(cmds, shellJoin(argv))
				continue
			}
		}
		ok = false
	}
	return cmds, present, ok && present
}

// noMention are tools whose text goes to an agent that runs under the same
// hooks, to the user, or to the web: naming the binary there is not a call.
var noMention = set("spawn_agent", "send_input", "send_message", "followup_task", "update_plan", "request_user_input",
	"Agent", "Task", "SendMessage", "TodoWrite", "AskUserQuestion", "ExitPlanMode", "EnterPlanMode",
	"WebSearch", "WebFetch", "ToolSearch", "Skill", "Glob")

// pathKeys hold a file or directory path in a tool's input (MCP filesystem
// servers, desktop-commander, unknown file tools).
var pathKeys = set("path", "paths", "file_path", "filepath", "file", "filename", "notebook_path", "target",
	"target_path", "destination", "dest", "source", "src", "directory", "dir", "new_path", "old_path")

// fieldScan checks the strings of a tool input the tool-specific checks did
// not decide: skip keys are not read at all, analysed keys only get the
// path checks.
type fieldScan struct {
	tool     string
	o        Options
	mention  bool
	skip     map[string]bool
	analysed map[string]bool
	dirRes   []*regexp.Regexp
}

// scan returns why the fields need the owner's OK ("" = nothing found).
func (f fieldScan) scan(fields map[string]any) string {
	f.dirRes = dirPatterns(f.o)
	for k, v := range fields {
		if f.skip[k] {
			continue
		}
		if r := f.value(k, v, !f.analysed[k]); r != "" {
			return r
		}
	}
	return ""
}

func (f fieldScan) value(key string, v any, mention bool) string {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if r := f.value(k, e, mention); r != "" {
				return r
			}
		}
	case []any:
		for _, e := range x {
			if r := f.value(key, e, mention); r != "" {
				return r
			}
		}
	case string:
		if r := f.text(key, x, mention); r != "" {
			return r
		}
		if obj, isJSON := jsonObject(x); isJSON {
			if obj == nil {
				var arr any
				if json.Unmarshal([]byte(strings.TrimSpace(x)), &arr) != nil {
					return "" // not JSON after all: read as text above
				}
				return f.value(key, arr, mention)
			}
			return f.value(key, obj, mention)
		}
	}
	return ""
}

func (f fieldScan) text(key, s string, mention bool) string {
	if mention && f.mention && (mentionRe.MatchString(s) || strings.Contains(s, "TGAC_")) {
		return "Telegram account action: this " + f.tool + " call names " + BinaryName +
			" or a TGAC_ variable in input the guard can't analyse"
	}
	for _, re := range f.dirRes {
		if re.MatchString(s) {
			return "Telegram account data: " + f.tool + " " + reasonDataDir
		}
	}
	settings := "Telegram account guard: " + f.tool + " touches agent settings or hook config"
	if namesCodexHook(s, f.o) || settingsCodeRe.MatchString(s) {
		return settings
	}
	var targets []string
	for _, m := range patchFileRe.FindAllStringSubmatch(s, -1) {
		targets = append(targets, m[1])
	}
	if pathKeys[strings.ToLower(key)] && strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\n\x00") {
		targets = append(targets, s)
	}
	for _, t := range targets {
		p := absPath(t, f.o)
		if settingsTarget(p, f.o) {
			return settings
		}
		if r := CheckFile("Write", p, f.o); r != "" {
			return "Telegram account data: " + f.tool + " " + r
		}
	}
	return ""
}

// lastSegment is a tool name's own part: mcp__server__tool → tool,
// functions.exec → exec.
func lastSegment(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

var wordSplitRe = regexp.MustCompile(`[^a-z0-9]+`)

func words(s string) []string { return wordSplitRe.Split(strings.ToLower(s), -1) }

// runnerWords in a tool's own name mean it runs a command or code.
var runnerWords = set("exec", "execute", "shell", "bash", "sh", "zsh", "cmd", "command", "eval", "repl", "powershell")

// isRunner reports whether a tool's name says it runs commands or code.
func isRunner(name string) bool {
	for _, w := range words(lastSegment(name)) {
		if runnerWords[w] {
			return true
		}
	}
	return false
}

var (
	telegramRe = regexp.MustCompile(`(?i)telegram|tgac`)
	// identRe is an identifier (a server or tool name), not prose.
	identRe = regexp.MustCompile(`^[\w.:/@-]+$`)
)

// identKeys name the server or tool a dispatching tool calls.
var identKeys = set("server", "server_name", "mcp_server", "name", "tool", "tool_name", "recipient_name", "method")

// readVerbs start a Telegram tool name that only reads.
var readVerbs = set("get", "list", "read", "search", "find", "fetch", "show", "view", "status", "resolve",
	"whoami", "info", "describe", "download", "count", "check")

// telegramTool returns why a call to a Telegram tool (an MCP server or a
// dispatch naming one) needs the owner's OK: anything but a read may act on
// the account, and the only unprompted write path is "telegram-agent-cli e2e".
func telegramTool(in Input) string {
	named := telegramRe.MatchString(in.ToolName)
	if !named && !dispatchesTelegram(in.ToolInput) {
		return ""
	}
	if named && readVerbs[words(lastSegment(in.ToolName))[0]] {
		return ""
	}
	return "calls a Telegram tool (" + in.ToolName + "), which may act on the account"
}

func dispatchesTelegram(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && identKeys[k] && identRe.MatchString(s) && telegramRe.MatchString(s) {
				return true
			}
			if dispatchesTelegram(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if dispatchesTelegram(e) {
				return true
			}
		}
	}
	return false
}
