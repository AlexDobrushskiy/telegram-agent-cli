package hook

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Word paths: each shell word is normalised the way the shell would see it
// (quote removal, ~, $HOME/${HOME}/$XDG_CONFIG_HOME and tracked variables,
// cwd and cd, Clean, globs and braces) and matched against the data dirs.

// segmentRe is a telegram-agent-cli path segment followed by a data-dir file:
// whatever the prefix, that is the data dir.
var segmentRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(BinaryName) +
	`(?:/+|/+(?:\./+)+|['"]\s*,\s*['"])` + dataFileAlt + `(?:$|[^A-Za-z0-9_.-])`)

const dataFileAlt = `(?:config\.json|session\.json|peers\.json|pending-login\.json|watchers\.json|answered\.jsonl|PAUSE-ALL)`

const (
	reasonDataDir   = "touches the " + BinaryName + " data dir (session keys, config, watcher registry)"
	reasonParentDir = "reads a directory tree that contains the " + BinaryName + " data dir"
)

// pathState is what the shell knows while the command runs: cwd ("" = unknown) and variables.
type pathState struct {
	cwd  string
	vars map[string]string
}

func newPathState(o Options) pathState {
	cwd := o.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	st := pathState{cwd: cwd, vars: map[string]string{}}
	if o.UserHome != "" {
		st.vars["HOME"] = o.UserHome
		st.vars["XDG_CONFIG_HOME"] = filepath.Join(o.UserHome, ".config")
	}
	if o.XDGConfigHome != "" {
		st.vars["XDG_CONFIG_HOME"] = o.XDGConfigHome
	}
	if cwd != "" {
		st.vars["PWD"] = cwd
	}
	return st
}

// expandPat substitutes known variables in a word pattern. Unknown ones stay
// markUnknown. ok is false when the word starts with an unknown expansion.
func (s *scanner) expandPat(pat string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(pat) {
				i++
				b.WriteByte(pat[i])
			}
		case markVar:
			j := strings.IndexByte(pat[i+1:], markVar)
			if j < 0 {
				return "", false
			}
			name := pat[i+1 : i+1+j]
			i += j + 1
			if v, ok := s.st.vars[name]; ok {
				b.WriteString(escMeta(v))
			} else {
				b.WriteByte(markUnknown)
			}
		default:
			b.WriteByte(c)
		}
	}
	p := b.String()
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = escMeta(s.o.UserHome) + p[1:]
	}
	if strings.HasPrefix(p, string(rune(markUnknown))) {
		return "", false
	}
	if !strings.HasPrefix(p, "/") {
		if s.st.cwd == "" {
			return "", false
		}
		p = escMeta(s.st.cwd) + "/" + p
	}
	return p, true
}

// splitPat splits an absolute pattern into cleaned components; an unknown
// expansion matches within one component, like *.
func splitPat(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "" || c == ".":
		case c == "..":
			if len(out) > 0 && out[len(out)-1] != "**" {
				out = out[:len(out)-1]
			}
		case strings.ContainsRune(c, markUnknown):
			out = append(out, strings.ReplaceAll(c, string(rune(markUnknown)), "*"))
		default:
			out = append(out, c)
		}
	}
	return out
}

// expandBraces expands unescaped {a,b} groups (bounded).
func expandBraces(p string) []string {
	const limit = 64
	open, depth := -1, 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++
		case '{':
			if depth == 0 {
				open = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth > 0 {
				continue
			}
			var alts []string
			last, d := open+1, 0
			for j := open + 1; j < i; j++ {
				switch p[j] {
				case '\\':
					j++
				case '{':
					d++
				case '}':
					d--
				case ',':
					if d == 0 {
						alts = append(alts, p[last:j])
						last = j + 1
					}
				}
			}
			if alts == nil {
				open = -1
				continue // {x} is literal
			}
			alts = append(alts, p[last:i])
			var out []string
			for _, a := range alts {
				for _, r := range expandBraces(p[:open] + a + p[i+1:]) {
					if len(out) >= limit {
						return out
					}
					out = append(out, r)
				}
			}
			return out
		}
	}
	return []string{p}
}

// compMatch matches one pattern component against a path component, case-insensitively.
func compMatch(pat, name string) bool {
	pat, name = strings.ToLower(pat), strings.ToLower(name)
	if ok, err := path.Match(pat, name); err == nil {
		return ok
	}
	return strings.ReplaceAll(pat, `\`, "") == name
}

// relation reports whether a pattern can name dir or something inside it, or
// only a directory above it.
func relation(pat, dir []string) (inside, above bool) {
	var walk func(i, j int)
	walk = func(i, j int) {
		if inside && above {
			return
		}
		if j == len(dir) {
			inside = true
			return
		}
		if i == len(pat) {
			above = true
			return
		}
		if pat[i] == "**" {
			walk(i+1, j)
			walk(i, j+1)
			return
		}
		if compMatch(pat[i], dir[j]) {
			walk(i+1, j+1)
		}
	}
	walk(0, 0)
	return inside, above
}

// dataDirComps are the data dirs (and their symlink targets) as components.
func dataDirComps(o Options) [][]string {
	var out [][]string
	seen := map[string]bool{}
	for _, d := range o.DataDirs {
		d = filepath.Clean(d)
		forms := []string{d}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			forms = append(forms, real)
		}
		for _, f := range forms {
			if !seen[f] && filepath.IsAbs(f) {
				seen[f] = true
				out = append(out, strings.Split(strings.Trim(f, "/"), "/"))
			}
		}
	}
	return out
}

// unescape removes pattern escapes from a pattern with no glob characters.
func unescape(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func hasGlob(p string) bool {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++
		case '*', '?', '[', markUnknown:
			return true
		}
	}
	return false
}

// pathRelation reports how a word relates to the data dirs.
func (s *scanner) pathRelation(w token) (inside, above bool) {
	cands := []string{w.pat}
	// --flag=PATH, NAME=PATH, of=PATH
	if k := strings.IndexByte(w.pat, '='); k > 0 {
		cands = append(cands, w.pat[k+1:])
	}
	for _, c := range cands {
		p, ok := s.expandPat(c)
		if !ok {
			continue
		}
		for _, alt := range expandBraces(p) {
			comps := splitPat(alt)
			if !hasGlob(alt) {
				// a concrete path: also resolve symlinks
				real := resolvePath(unescape(alt), s.o)
				comps2 := splitPat(escMeta(real))
				for _, d := range s.dirs {
					in, ab := relation(comps2, d)
					inside, above = inside || in, above || ab
				}
			}
			for _, d := range s.dirs {
				in, ab := relation(comps, d)
				inside, above = inside || in, above || ab
			}
		}
	}
	return inside, above
}

// recursiveReader returns the operands a program reads as whole directory
// trees (nil if it does not), and whether hidden directories are skipped.
func recursiveReader(prog string, args []token, data map[int]bool, off int, next *command) (ops []token, skipsHidden bool) {
	nonFlag := func(values map[string]bool) []token {
		var out []token
		for i := 0; i < len(args); i++ {
			t := args[i].text
			if data[off+i] {
				continue
			}
			if t == "--" {
				for k := i + 1; k < len(args); k++ {
					if !data[off+k] {
						out = append(out, args[k])
					}
				}
				break
			}
			if strings.HasPrefix(t, "-") && len(t) > 1 {
				if values[t] {
					i++
				}
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	cwd := token{text: ".", pat: "."}
	orCwd := func(ops []token) []token {
		if len(ops) == 0 {
			return []token{cwd}
		}
		return ops
	}
	hasFlag := func(pred func(string) bool) bool {
		for _, a := range args {
			if pred(a.text) {
				return true
			}
		}
		return false
	}
	cluster := func(letters string) func(string) bool {
		return func(t string) bool {
			return len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.ContainsAny(t[1:], letters)
		}
	}
	switch prog {
	case "grep", "egrep", "fgrep", "ggrep", "zgrep":
		rec := hasFlag(func(t string) bool {
			return t == "--recursive" || t == "--dereference-recursive" || t == "--directories=recurse" ||
				cluster("rR")(t)
		})
		for i, a := range args {
			if a.text == "-d" && i+1 < len(args) && args[i+1].text == "recurse" {
				rec = true
			}
		}
		if rec {
			return orCwd(nonFlag(grepValues)), false
		}
	case "rg", "ag", "ack", "ugrep", "pt", "sift":
		hidden := prog == "ack" || prog == "ugrep" || hasFlag(func(t string) bool {
			return t == "--hidden" || t == "-." || t == "-uu" || t == "-uuu" || t == "-u" && prog == "ag" || t == "--unrestricted"
		})
		return orCwd(nonFlag(grepValues)), !hidden
	case "find":
		act := hasFlag(func(t string) bool {
			switch t {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				return true
			}
			return false
		})
		if next != nil {
			for _, w := range next.words {
				if assignRe.MatchString(w.text) {
					continue
				}
				switch base(w.text) {
				case "xargs", "parallel", "cpio", "tar", "zip", "rsync", "sh", "bash", "zsh", "while":
					act = true
				}
				break
			}
		}
		if !act {
			return nil, false
		}
		var out []token
		for _, a := range args {
			if strings.HasPrefix(a.text, "-") || a.text == "(" || a.text == "!" {
				break
			}
			out = append(out, a)
		}
		return orCwd(out), false
	case "cp", "scp":
		if hasFlag(cluster("rRa")) || hasFlag(func(t string) bool { return t == "--recursive" || t == "--archive" }) {
			return nonFlag(nil), false
		}
	case "tar", "bsdtar", "gtar", "zip", "7z", "7za", "7zz", "rsync", "ditto", "pax", "cpio", "rclone",
		"restic", "borg", "duplicity", "gsutil", "rar":
		return nonFlag(nil), false
	case "aws":
		return nonFlag(nil), false
	}
	return nil, false
}

// hiddenBetween reports whether the data dir sits under a hidden directory
// below the operand (rg and ag skip those).
func hiddenBetween(op []string, dir []string) bool {
	for j := len(op); j < len(dir); j++ {
		if strings.HasPrefix(dir[j], ".") {
			return true
		}
	}
	return false
}

// pathRules checks a command's words, redirect targets and recursive reads.
func (s *scanner) pathRules(cmd *command, idx int, data map[int]bool) {
	for i, w := range cmd.words {
		if data[i] {
			continue
		}
		if segmentRe.MatchString(w.text) {
			s.reasons.add(reasonDataDir)
		}
		if in, _ := s.pathRelation(w); in {
			s.reasons.add(reasonDataDir)
		}
	}
	for _, w := range cmd.redirs {
		if in, _ := s.pathRelation(w); in || segmentRe.MatchString(w.text) {
			s.reasons.add(reasonDataDir)
		}
	}
	if idx < 0 {
		return
	}
	ops, skipsHidden := recursiveReader(base(cmd.words[idx].text), cmd.words[idx+1:], data, idx+1, cmd.next)
	for _, op := range ops {
		p, ok := s.expandPat(op.pat)
		if !ok {
			continue
		}
		for _, alt := range expandBraces(p) {
			comps := splitPat(alt)
			for _, d := range s.dirs {
				if _, above := relation(comps, d); above && (!skipsHidden || !hiddenBetween(comps, d)) {
					s.reasons.add(reasonParentDir)
				}
			}
		}
	}
}

// track updates cwd and variables after a command runs.
func (s *scanner) track(cmd *command, idx int) {
	words := cmd.words
	setVar := func(w token) {
		k := strings.IndexByte(w.text, '=')
		pk := strings.IndexByte(w.pat, '=')
		if k <= 0 || pk <= 0 || !isName(strings.TrimSuffix(w.text[:k], "+")) {
			return
		}
		name := strings.TrimSuffix(w.text[:k], "+")
		v, ok := s.expandValue(w.pat[pk+1:])
		if ok {
			s.st.vars[name] = v
		} else {
			delete(s.st.vars, name)
		}
	}
	if idx < 0 {
		for _, w := range words {
			if assignRe.MatchString(w.text) {
				setVar(w)
			}
		}
		return
	}
	switch base(words[idx].text) {
	case "export", "declare", "typeset", "local", "readonly":
		for _, w := range words[idx+1:] {
			if assignRe.MatchString(w.text) {
				setVar(w)
			}
		}
	case "cd", "pushd":
		var dir *token
		for i := idx + 1; i < len(words); i++ {
			if !strings.HasPrefix(words[i].text, "-") || words[i].text == "-" {
				dir = &words[i]
				break
			}
		}
		if dir == nil {
			s.st.cwd = s.o.UserHome
			return
		}
		p, ok := s.expandPat(dir.pat)
		if !ok || dir.text == "-" || hasGlob(p) {
			s.st.cwd = ""
			return
		}
		s.st.cwd = "/" + strings.Join(splitPat(unescape(p)), "/")
	}
}

// expandValue expands a variable's value pattern to a concrete string.
func (s *scanner) expandValue(pat string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '\\':
			if i+1 < len(pat) {
				i++
				b.WriteByte(pat[i])
			}
		case markUnknown:
			return "", false
		case markVar:
			j := strings.IndexByte(pat[i+1:], markVar)
			if j < 0 {
				return "", false
			}
			v, ok := s.st.vars[pat[i+1:i+1+j]]
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i += j + 1
		default:
			b.WriteByte(c)
		}
	}
	v := b.String()
	if v == "~" || strings.HasPrefix(v, "~/") {
		v = s.o.UserHome + v[1:]
	}
	return v, true
}
