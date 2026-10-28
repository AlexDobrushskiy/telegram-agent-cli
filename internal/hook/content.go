package hook

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Content the file tools write, and Claude Code settings: the guard's own
// install (hooks, env.TGAC_READONLY) lives there, so changing it asks.

const reasonSettings = "changes Claude Code settings that hold the guard (hooks, " + "TGAC_" + " variables)"

// settingsTokens in a settings edit always ask.
var settingsTokenRe = regexp.MustCompile(`(?i)\bhooks\b|PreToolUse|disableAllHooks|allowManagedHooksOnly|TGAC_|` +
	regexp.QuoteMeta(BinaryName))

// settingsCodeRe is code that names a Claude Code settings file.
var settingsCodeRe = regexp.MustCompile(`(?i)\.claude[\w-]*['"]?\s*[/,]\s*['"]?settings(?:\.local)?\.json|managed-settings\.json`)

// fileWriteRe is code that writes, moves or deletes a file.
var fileWriteRe = regexp.MustCompile(`open\([^)]*,\s*(?:mode\s*=\s*)?['"][rwax+b]*[wax+]|\.write_(?:text|bytes)\(|` +
	`writeFile(?:Sync)?\(|os\.(?:replace|rename|remove|unlink)\(|shutil\.(?:copy|move)|\.unlink\(|File\.write|` +
	`os\.WriteFile|fs\.(?:rm|unlink|rename)|sed\s+-i`)

// writesSettings reports whether code names a settings file and writes a file.
func writesSettings(code string) bool {
	return settingsCodeRe.MatchString(code) && fileWriteRe.MatchString(code)
}

var docExts = set(".md", ".markdown", ".mdx", ".txt", ".rst", ".adoc", ".org")

// claudeDirs are the directories whose settings*.json Claude Code loads, besides any .claude dir.
func claudeDirs(o Options) []string {
	var dirs []string
	if o.ClaudeConfigDir != "" {
		dirs = append(dirs, filepath.Clean(o.ClaudeConfigDir))
	}
	if o.UserHome != "" {
		dirs = append(dirs, filepath.Join(o.UserHome, ".claude"))
	}
	return dirs
}

// settingsTarget reports whether an absolute path is a Claude Code settings
// file or a directory holding them.
func settingsTarget(p string, o Options) bool {
	for _, q := range []string{p, resolvePath(p, o)} {
		q = filepath.Clean(q)
		b := strings.ToLower(filepath.Base(q))
		dir := filepath.Dir(q)
		if b == "managed-settings.json" || b == ".claude" {
			return true
		}
		isDir := func(d string) bool {
			if strings.EqualFold(filepath.Base(d), ".claude") {
				return true
			}
			for _, c := range claudeDirs(o) {
				if within(d, c) && within(c, d) {
					return true
				}
			}
			return false
		}
		if isDir(q) {
			return true
		}
		if (b == "settings.json" || b == "settings.local.json") && isDir(dir) {
			return true
		}
		// a known settings file reached through a symlink
		for _, c := range claudeDirs(o) {
			for _, n := range []string{"settings.json", "settings.local.json"} {
				if real := resolvePath(filepath.Join(c, n), o); real == q {
					return true
				}
			}
		}
	}
	return false
}

// projection is the part of a settings file the guard depends on, canonical.
func projection(text string, o Options) (string, bool) {
	if strings.TrimSpace(text) == "" {
		return "{}", true
	}
	var root map[string]any
	if json.Unmarshal([]byte(text), &root) != nil {
		return "", false
	}
	keep := map[string]any{}
	for _, k := range []string{"hooks", "disableAllHooks", "allowManagedHooksOnly"} {
		if v, ok := root[k]; ok {
			keep[k] = v
		}
	}
	if env, ok := root["env"].(map[string]any); ok {
		names := envNames(o)
		sub := map[string]any{}
		for k, v := range env {
			if _, p := names[k]; p || strings.HasPrefix(k, "TGAC_") || k == "CLAUDE_CONFIG_DIR" {
				sub[k] = v
			}
		}
		keep["env"] = sub
	} else if _, has := root["env"]; has {
		keep["env"] = root["env"]
	}
	b, _ := json.Marshal(keep)
	return string(b), true
}

// writtenStrings collects every string a file tool writes (new) and replaces (old).
func writtenStrings(ti map[string]any) (newer, older []string) {
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch t := v.(type) {
		case string:
			switch key {
			case "file_path", "notebook_path", "path":
			case "old_string":
				older = append(older, t)
			default:
				newer = append(newer, t)
			}
		case map[string]any:
			for k, x := range t {
				walk(k, x)
			}
		case []any:
			for _, x := range t {
				walk(key, x)
			}
		}
	}
	walk("", ti)
	return newer, older
}

// applyEdits returns the file after a Write, Edit or MultiEdit, ok=false if unknown.
func applyEdits(tool, before string, ti map[string]any) (string, bool) {
	edit := func(text string, e map[string]any) (string, bool) {
		old, _ := e["old_string"].(string)
		nw, _ := e["new_string"].(string)
		if old == "" || !strings.Contains(text, old) {
			return "", false
		}
		if all, _ := e["replace_all"].(bool); all {
			return strings.ReplaceAll(text, old, nw), true
		}
		return strings.Replace(text, old, nw, 1), true
	}
	switch tool {
	case "Write":
		c, ok := ti["content"].(string)
		return c, ok
	case "Edit":
		return edit(before, ti)
	case "MultiEdit":
		edits, ok := ti["edits"].([]any)
		if !ok {
			return "", false
		}
		text := before
		for _, ev := range edits {
			e, ok := ev.(map[string]any)
			if !ok {
				return "", false
			}
			if text, ok = edit(text, e); !ok {
				return "", false
			}
		}
		return text, true
	}
	return "", false
}

// CheckContent returns why a file tool's written content needs the owner's OK.
func CheckContent(tool string, ti map[string]any, o Options) string {
	if tool == "Read" || tool == "Grep" {
		return ""
	}
	p := str(ti, "file_path", "notebook_path")
	if p == "" {
		return ""
	}
	full := absPath(p, o)
	newer, older := writtenStrings(ti)
	if settingsTarget(full, o) {
		if strings.EqualFold(filepath.Base(full), "managed-settings.json") {
			return reasonSettings
		}
		for _, t := range append(append([]string{}, newer...), older...) {
			if settingsTokenRe.MatchString(t) {
				return reasonSettings
			}
		}
		read := o.ReadFile
		if read == nil {
			read = readScript
		}
		before := ""
		if b, err := read(full); err == nil {
			before = string(b)
		} else if !errorsIsNotExist(err) {
			return reasonSettings + "; the current file can't be read"
		}
		after, ok := applyEdits(tool, before, ti)
		if !ok {
			return reasonSettings + "; the guard can't work out the result"
		}
		pb, okb := projection(before, o)
		pa, oka := projection(after, o)
		if !okb || !oka || pb != pa {
			return reasonSettings
		}
		return ""
	}
	if docExts[strings.ToLower(filepath.Ext(full))] {
		return ""
	}
	s := newScanner(o)
	for _, t := range newer {
		s.rawRules(t)
		if segmentRe.MatchString(t) {
			s.reasons.add(reasonDataDir)
		}
		if writesSettings(t) {
			s.reasons.add("writes code that touches Claude Code settings")
		}
	}
	return strings.Join(s.reasons.list, "; ")
}

// Bash writes to a settings file: a redirect onto it, or one of these programs
// with it in a writing position. Reading or mentioning it is fine.
var (
	settingsMovers  = set("mv", "rm", "unlink", "truncate", "shred", "touch", "chmod", "chown", "sponge", "tee")
	settingsCopiers = set("cp", "install", "rsync", "ditto", "scp", "ln", "gcp")
	settingsInPlace = set("sed", "gsed", "perl", "ruby", "yq")
	gitWrites       = set("checkout", "restore", "rm", "mv", "stash", "reset")
)

// settingsPath reports whether a word names a settings file (or a .claude dir
// when dirs is set) as a concrete path, or a glob whose directory is a literal
// .claude dir and whose name matches settings*.json.
func (s *scanner) settingsPath(w token, dirs bool) bool {
	p, ok := s.expandPat(w.pat)
	if !ok {
		return false
	}
	for _, alt := range expandBraces(p) {
		if !hasGlob(alt) {
			full := unescape(alt)
			if settingsTarget(full, s.o) && (dirs || !isClaudeDir(full, s.o)) {
				return true
			}
			continue
		}
		c := splitPat(alt)
		if n := len(c); n > 1 && !hasGlob(c[n-2]) && isClaudeDir("/"+strings.Join(c[:n-1], "/"), s.o) &&
			(compMatch(c[n-1], "settings.json") || compMatch(c[n-1], "settings.local.json")) {
			return true
		}
	}
	return false
}

func isClaudeDir(p string, o Options) bool {
	if strings.EqualFold(filepath.Base(p), ".claude") {
		return true
	}
	for _, c := range claudeDirs(o) {
		if within(p, c) && within(c, p) {
			return true
		}
	}
	return false
}

// settingsWrites asks when a command writes a Claude Code settings file.
func (s *scanner) settingsWrites(cmd *command, idx int, data map[int]bool) {
	for _, w := range cmd.outRedirs {
		if s.settingsPath(w, false) {
			s.reasons.add(reasonSettings)
		}
	}
	if idx < 0 {
		return
	}
	prog := base(cmd.words[idx].text)
	args := cmd.words[idx+1:]
	var ops []token
	inPlace := false
	for i, a := range args {
		t := a.text
		if t == "--in-place" || strings.HasPrefix(t, "--in-place=") || len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.Contains(t, "i") {
			inPlace = true
		}
		if !data[idx+1+i] && !strings.HasPrefix(t, "-") {
			ops = append(ops, a)
		}
	}
	var targets []token
	dirs := false
	switch {
	case settingsMovers[prog]:
		targets, dirs = ops, prog == "mv" || prog == "rm"
	case settingsCopiers[prog] && len(ops) > 1:
		targets, dirs = ops[len(ops)-1:], true
	case settingsInPlace[prog] && inPlace:
		targets = ops
	case prog == "dd":
		for _, a := range args {
			if v, ok := strings.CutPrefix(a.text, "of="); ok {
				targets = append(targets, token{text: v, pat: a.pat[3:]})
			}
		}
	case prog == "git" && len(ops) > 0 && gitWrites[ops[0].text]:
		targets = ops[1:]
	}
	for _, t := range targets {
		if s.settingsPath(t, dirs) {
			s.reasons.add(reasonSettings)
		}
	}
}
