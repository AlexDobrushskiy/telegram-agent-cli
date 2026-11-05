package hook

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

// inputKeys are envelopes that carry a tool's input under another name; a
// payload with one is a shape the guard can't read. Other top-level fields
// are call metadata (session_id, scratchpad_dir…, a set that grows with each
// harness version) and are not read.
var inputKeys = set("arguments", "input", "parameters", "params", "args", "tool_args", "tool_arguments")

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
	for k := range p {
		if inputKeys[k] {
			return in, false
		}
	}
	if in.ToolInput, in.wrapped, ok = decodeToolInput(p["tool_input"]); !ok {
		return in, false
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

// pathKeys hold a file or directory path in a tool's input (MCP filesystem
// servers, desktop-commander, unknown file tools).
var pathKeys = set("path", "paths", "file_path", "filepath", "file", "filename", "notebook_path", "target",
	"target_path", "destination", "dest", "source", "src", "directory", "dir", "new_path", "old_path")

// terminalKeys hold text typed into a running shell or REPL (write_stdin).
var terminalKeys = set("chars", "stdin")

// fieldScan checks an unknown tool's input at any depth, also inside
// JSON-encoded strings: a nested command field (parallel tool uses, a
// dispatch) and terminal input get the Bash checks, a TGAC_READONLY env key
// that turns read-only off asks, and path fields and patch file lines get the
// data-dir write check. Other text is data.
type fieldScan struct {
	tool string
	o    Options
}

// scan returns why the fields need the owner's OK ("" = nothing found).
// The top level's command fields are checkOtherTool's.
func (f fieldScan) scan(fields map[string]any) string {
	return f.object(fields, true)
}

func (f fieldScan) object(m map[string]any, top bool) string {
	if !top {
		if r := f.calls(m); r != "" {
			return r
		}
	}
	for k, v := range m {
		if v, isStr := v.(string); isStr && k == config.EnvReadOnly && readOnlyOff(assign{ok: true, known: true, value: v}) {
			return "Telegram account action: this " + f.tool + " call " + reasonReadOnly
		}
		if r := f.value(k, v); r != "" {
			return r
		}
	}
	return ""
}

// calls checks the command fields of a nested object.
func (f fieldScan) calls(m map[string]any) string {
	var rs reasonSet
	cmds, present, ok := commandsOf(m)
	if present && !ok {
		return ""
	}
	for _, c := range cmds {
		for _, r := range CheckBash(c, f.o) {
			rs.add(r)
		}
	}
	if len(rs.list) == 0 {
		return ""
	}
	return "Telegram account action: needs the account owner's explicit OK for a call inside this " + f.tool +
		" call (" + strings.Join(rs.list, "; ") + ")"
}

func (f fieldScan) value(key string, v any) string {
	switch x := v.(type) {
	case map[string]any:
		return f.object(x, false)
	case []any:
		for _, e := range x {
			if r := f.value(key, e); r != "" {
				return r
			}
		}
	case string:
		if r := f.text(key, x); r != "" {
			return r
		}
		if obj, isJSON := jsonObject(x); isJSON {
			if obj == nil {
				var arr any
				if json.Unmarshal([]byte(strings.TrimSpace(x)), &arr) != nil {
					return "" // not JSON after all: read as text above
				}
				return f.value(key, arr)
			}
			return f.object(obj, false)
		}
	}
	return ""
}

func (f fieldScan) text(key, s string) string {
	if terminalKeys[strings.ToLower(key)] {
		if rs := CheckBash(s, f.o); len(rs) > 0 {
			return "Telegram account action: this " + f.tool + " call types a command that needs the account owner's explicit OK (" +
				strings.Join(rs, "; ") + ")"
		}
	}
	var targets []string
	for _, m := range patchFileRe.FindAllStringSubmatch(s, -1) {
		targets = append(targets, m[1])
	}
	if pathKeys[strings.ToLower(key)] && strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\n\x00") {
		targets = append(targets, s)
	}
	for _, t := range targets {
		if r := CheckFile("Write", absPath(t, f.o), f.o); r != "" {
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
