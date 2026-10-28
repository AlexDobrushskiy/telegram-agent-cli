package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

// HookArgs is what Claude Code runs after the binary path.
const HookArgs = "hook claude-pretooluse"

// Matchers are the PreToolUse matchers the guard is installed under.
var Matchers = []string{"Bash|Monitor", "Read|Edit|Write|MultiEdit|NotebookEdit|Grep"}

// SettingsPath is $CLAUDE_CONFIG_DIR/settings.json or ~/.claude/settings.json.
func SettingsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// HookCommand is the hook command line for a binary path.
func HookCommand(binary string) string { return shellQuote(binary) + " " + HookArgs }

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isOurs reports whether a hook entry runs this guard (any binary path).
func isOurs(v any) bool {
	h, ok := v.(*object)
	if !ok || h.str("type") != "command" {
		return false
	}
	cmd := strings.TrimSpace(h.str("command"))
	bin, ok := strings.CutSuffix(cmd, " "+HookArgs)
	if !ok {
		return false
	}
	lx, err := lex(strings.TrimSpace(bin), -1)
	if err != nil || len(lx.toks) != 1 {
		return false
	}
	return base(lx.toks[0].text) == BinaryName
}

// Result reports an install/uninstall.
type Result struct {
	Settings string          `json:"settings"`
	DryRun   bool            `json:"dry_run"`
	Changed  bool            `json:"changed"`
	Actions  []string        `json:"actions"`
	Backup   string          `json:"backup,omitempty"`
	Diff     string          `json:"diff"`
	Result   json.RawMessage `json:"result,omitempty"` // the new settings (dry run only)
}

type settingsFile struct {
	path    string // the file written (symlinks resolved)
	exists  bool
	mode    os.FileMode
	text    string
	root    *object
	indent  string
	newline bool
}

func loadSettings(path string) (*settingsFile, error) {
	f := &settingsFile{path: path, mode: 0o644, indent: "  ", newline: true, root: &object{}}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		f.path = real
	}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(f.path)
	if err != nil {
		return nil, err
	}
	f.exists, f.mode, f.text = true, st.Mode().Perm(), string(b)
	if strings.TrimSpace(f.text) == "" {
		return f, nil
	}
	v, err := decodeJSON(b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	root, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("%s: top level is not a JSON object", path)
	}
	f.root = root
	f.indent = detectIndent(f.text)
	f.newline = strings.HasSuffix(f.text, "\n")
	return f, nil
}

func (f *settingsFile) render() string {
	s := string(encodeJSON(f.root, f.indent))
	if f.newline {
		s += "\n"
	}
	return s
}

// childObject returns parent[key] as an object, creating it when create is set.
func childObject(parent *object, key string, create bool) (*object, error) {
	v, ok := parent.get(key)
	if !ok || v == nil {
		if !create {
			return nil, nil
		}
		o := &object{}
		parent.set(key, o)
		return o, nil
	}
	o, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("settings %q is not an object", key)
	}
	return o, nil
}

func preToolUse(root *object, create bool) (*object, []any, error) {
	hooks, err := childObject(root, "hooks", create)
	if err != nil || hooks == nil {
		return nil, nil, err
	}
	v, ok := hooks.get("PreToolUse")
	if !ok || v == nil {
		if create {
			hooks.set("PreToolUse", []any{})
		}
		return hooks, []any{}, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, nil, errors.New(`settings "hooks.PreToolUse" is not an array`)
	}
	return hooks, arr, nil
}

func entryHooks(e *object) ([]any, error) {
	v, ok := e.get("hooks")
	if !ok || v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New(`a PreToolUse entry's "hooks" is not an array`)
	}
	return arr, nil
}

// Install adds TGAC_READONLY=1 and the guard hooks to the settings at path.
func Install(path, binary string, dryRun bool, now time.Time) (*Result, error) {
	f, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	var actions []string
	env, err := childObject(f.root, "env", true)
	if err != nil {
		return nil, err
	}
	if v, ok := env.get(config.EnvReadOnly); !ok || v != "1" {
		env.set(config.EnvReadOnly, "1")
		actions = append(actions, "set env."+config.EnvReadOnly+"=1")
	}
	hooks, entries, err := preToolUse(f.root, true)
	if err != nil {
		return nil, err
	}
	want := HookCommand(binary)
	wanted := map[string]bool{}
	for _, m := range Matchers {
		wanted[m] = true
	}
	var kept []any
	for _, ev := range entries {
		e, ok := ev.(*object)
		if !ok {
			kept = append(kept, ev)
			continue
		}
		hs, err := entryHooks(e)
		if err != nil {
			return nil, err
		}
		matcher := e.str("matcher")
		var out []any
		removed := false
		for _, h := range hs {
			if !isOurs(h) {
				out = append(out, h)
				continue
			}
			if !wanted[matcher] {
				removed = true
				actions = append(actions, fmt.Sprintf("removed the guard from matcher %q", matcher))
				continue
			}
			if wanted[matcher] && contains(out, isOurs) {
				removed = true
				actions = append(actions, fmt.Sprintf("removed a duplicate guard from matcher %q", matcher))
				continue
			}
			if setGuardFields(h.(*object), want) {
				actions = append(actions, fmt.Sprintf("updated the guard entry for matcher %q", matcher))
			}
			out = append(out, h)
		}
		if removed {
			if len(out) == 0 {
				continue // the entry only held stale guards
			}
			e.set("hooks", out)
		}
		kept = append(kept, e)
	}
	for _, m := range Matchers {
		if hasGuard(kept, m) {
			continue
		}
		hook := &object{members: []member{{"type", "command"}}}
		setGuardFields(hook, want)
		if e := findEntry(kept, m); e != nil {
			hs, _ := entryHooks(e)
			e.set("hooks", append(hs, hook))
			actions = append(actions, fmt.Sprintf("added the guard to the existing %q entry", m))
			continue
		}
		kept = append(kept, &object{members: []member{{"matcher", m}, {"hooks", []any{hook}}}})
		actions = append(actions, fmt.Sprintf("added a PreToolUse %q entry", m))
	}
	if kept == nil {
		kept = []any{}
	}
	hooks.set("PreToolUse", kept)
	return f.finish(path, actions, dryRun, now)
}

// Uninstall removes the guard hooks and env.TGAC_READONLY="1".
func Uninstall(path string, dryRun bool, now time.Time) (*Result, error) {
	f, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	var actions []string
	if env, err := childObject(f.root, "env", false); err != nil {
		return nil, err
	} else if env != nil {
		if v, ok := env.get(config.EnvReadOnly); ok && v == "1" {
			env.del(config.EnvReadOnly)
			actions = append(actions, "removed env."+config.EnvReadOnly)
			if len(env.members) == 0 {
				f.root.del("env")
			}
		}
	}
	hooks, entries, err := preToolUse(f.root, false)
	if err != nil {
		return nil, err
	}
	if hooks != nil && len(entries) > 0 {
		var kept []any
		for _, ev := range entries {
			e, ok := ev.(*object)
			if !ok {
				kept = append(kept, ev)
				continue
			}
			hs, err := entryHooks(e)
			if err != nil {
				return nil, err
			}
			var out []any
			for _, h := range hs {
				if isOurs(h) {
					actions = append(actions, fmt.Sprintf("removed the guard from matcher %q", e.str("matcher")))
					continue
				}
				out = append(out, h)
			}
			if len(out) == len(hs) {
				kept = append(kept, e)
				continue
			}
			if len(out) > 0 {
				e.set("hooks", out)
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			hooks.del("PreToolUse")
			if len(hooks.members) == 0 {
				f.root.del("hooks")
			}
		} else {
			hooks.set("PreToolUse", kept)
		}
	}
	return f.finish(path, actions, dryRun, now)
}

// HookTimeout is the per-call limit written on the guard entries (seconds).
const HookTimeout = 10

// setGuardFields sets the guard's command, timeout and fail-closed onFailure;
// it reports whether anything changed.
func setGuardFields(h *object, command string) bool {
	changed := false
	if h.str("command") != command {
		h.set("command", command)
		changed = true
	}
	if v, ok := h.get("timeout"); !ok || fmt.Sprint(v) != fmt.Sprint(HookTimeout) {
		h.set("timeout", json.Number(fmt.Sprint(HookTimeout)))
		changed = true
	}
	if h.str("onFailure") != "block" {
		h.set("onFailure", "block")
		changed = true
	}
	return changed
}

func contains(xs []any, pred func(any) bool) bool {
	for _, x := range xs {
		if pred(x) {
			return true
		}
	}
	return false
}

func findEntry(entries []any, matcher string) *object {
	for _, ev := range entries {
		if e, ok := ev.(*object); ok && e.str("matcher") == matcher {
			return e
		}
	}
	return nil
}

func hasGuard(entries []any, matcher string) bool {
	for _, ev := range entries {
		e, ok := ev.(*object)
		if !ok || e.str("matcher") != matcher {
			continue
		}
		hs, _ := entryHooks(e)
		if contains(hs, isOurs) {
			return true
		}
	}
	return false
}

// finish diffs, and unless dryRun backs up and atomically writes the file.
func (f *settingsFile) finish(path string, actions []string, dryRun bool, now time.Time) (*Result, error) {
	newText := f.render()
	r := &Result{Settings: path, DryRun: dryRun, Actions: actions}
	if r.Actions == nil {
		r.Actions = []string{}
	}
	if len(actions) == 0 {
		return r, nil
	}
	r.Changed = true
	r.Diff = unifiedDiff(filepath.Base(path), f.text, newText)
	if dryRun {
		r.Result = json.RawMessage(encodeJSON(f.root, "  "))
		return r, nil
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return nil, err
	}
	if f.exists {
		r.Backup = f.path + ".bak-" + now.Format("20060102-150405")
		for n := 1; ; n++ {
			if _, err := os.Lstat(r.Backup); errors.Is(err, os.ErrNotExist) {
				break
			}
			r.Backup = fmt.Sprintf("%s.bak-%s-%d", f.path, now.Format("20060102-150405"), n)
		}
		if err := os.WriteFile(r.Backup, []byte(f.text), f.mode); err != nil {
			return nil, fmt.Errorf("backup: %w", err)
		}
	}
	if err := config.WriteFileAtomic(f.path, []byte(newText), f.mode); err != nil {
		return nil, err
	}
	return r, nil
}
