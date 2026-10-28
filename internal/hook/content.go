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
var settingsCodeRe = regexp.MustCompile(`(?i)\.claude['"]?\s*[/,]\s*['"]?settings(?:\.local)?\.json|managed-settings\.json|CLAUDE_CONFIG_DIR`)

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
		if settingsCodeRe.MatchString(t) {
			s.reasons.add("writes code that touches Claude Code settings")
		}
	}
	return strings.Join(s.reasons.list, "; ")
}

// settingsReaders only read the files they are given.
var settingsReaders = set("cat", "less", "more", "head", "tail", "bat", "jq", "grep", "egrep", "fgrep", "rg",
	"ag", "ack", "wc", "diff", "cmp", "ls", "stat", "file", "md5", "md5sum", "shasum", "sha256sum", "echo",
	"printf", "code", "open", "subl", "realpath", "readlink", "dirname", "basename", "test", "[", "du",
	"tree", "hexdump", "xxd", "od", "strings", "column", "sort", "uniq")

var gitReads = set("diff", "show", "log", "status", "blame", "grep", "ls-files", "cat-file", "check-ignore")

var copiers = set("cp", "install", "rsync", "ditto", "scp", "ln")

// settingsWrites reports whether a command may change a Claude Code settings file.
func (s *scanner) settingsWrites(cmd *command, idx int, data map[int]bool) {
	target := func(w token) bool {
		p, ok := s.expandPat(w.pat)
		if !ok {
			return false
		}
		for _, alt := range expandBraces(p) {
			if !hasGlob(alt) {
				if settingsTarget(unescape(alt), s.o) {
					return true
				}
				continue
			}
			c := splitPat(alt)
			if len(c) == 0 {
				continue
			}
			last := c[len(c)-1]
			if compMatch(last, ".claude") || len(c) > 1 && compMatch(c[len(c)-2], ".claude") &&
				(compMatch(last, "settings.json") || compMatch(last, "settings.local.json")) {
				return true
			}
		}
		return false
	}
	for _, w := range cmd.outRedirs {
		if target(w) {
			s.reasons.add(reasonSettings)
		}
	}
	if idx < 0 {
		return
	}
	prog := base(cmd.words[idx].text)
	args := cmd.words[idx+1:]
	inPlace := false
	for _, a := range args {
		t := a.text
		if t == "--in-place" || strings.HasPrefix(t, "--in-place=") || len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.Contains(t, "i") {
			inPlace = true
		}
	}
	var operands []int
	for i, a := range args {
		if !data[idx+1+i] && !strings.HasPrefix(a.text, "-") {
			operands = append(operands, i)
		}
	}
	for n, i := range operands {
		if !target(args[i]) {
			continue
		}
		write := true
		switch {
		case prog == "git":
			write = len(operands) > 0 && !gitReads[args[operands[0]].text]
		case copiers[prog]:
			write = prog == "ln" || n == len(operands)-1
		case prog == "sed" || prog == "perl" || prog == "ruby" || prog == "yq":
			write = inPlace
		case prog == "find":
			write = false
			for _, a := range args {
				switch a.text {
				case "-delete", "-exec", "-execdir", "-ok", "-okdir":
					write = true
				}
			}
		case settingsReaders[prog]:
			write = false
		}
		if write {
			s.reasons.add(reasonSettings)
		}
	}
}
