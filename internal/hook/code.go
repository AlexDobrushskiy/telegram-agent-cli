package hook

import (
	"errors"
	"io/fs"
	"regexp"
	"strings"
)

// Code analysis for interpreter code (python -c, node -e, heredocs, script
// files): the binary named in a string literal is followed to its argv.
// ['telegram-agent-cli', 'auth', 'logout'] is an argv list; a literal holding a
// whole command line is parsed as shell. Anything else asks.

const reasonUnresolved = "runs " + BinaryName + " from code with arguments the guard can't resolve"

type lit struct {
	val        string
	start, end int  // offsets of the opening and after the closing quote
	interp     bool // has interpolation (f-string, template, #{}, $var)
}

// literals tokenizes string literals in code, skipping # and // line comments.
// It also returns the spans of the comments.
func literals(code string) (lits []lit, comments [][2]int) {
	lineStart := func(i int) bool {
		for j := i - 1; j >= 0; j-- {
			if code[j] == '\n' {
				return true
			}
			if code[j] != ' ' && code[j] != '\t' {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '#' && (i == 0 || strings.IndexByte(" \t\n;", code[i-1]) >= 0),
			c == '/' && strings.HasPrefix(code[i:], "//") && lineStart(i):
			j := strings.IndexByte(code[i:], '\n')
			if j < 0 {
				j = len(code) - i
			}
			comments = append(comments, [2]int{i, i + j})
			i += j
		case c == '\'' || c == '"' || c == '`':
			// prefix letters (r, b, f, u) directly before the quote
			p := i
			for p > 0 && i-p < 2 && strings.IndexByte("rRbBuUfF", code[p-1]) >= 0 {
				p--
			}
			if p > 0 && isNameChar(code[p-1]) {
				p = i
			}
			prefix := strings.ToLower(code[p:i])
			delim := string(c)
			if strings.HasPrefix(code[i:], strings.Repeat(delim, 3)) {
				delim = strings.Repeat(delim, 3)
			}
			body := i + len(delim)
			end := -1
			for j := body; j+len(delim) <= len(code); j++ {
				if code[j] == '\\' && c != '`' {
					j++
					continue
				}
				if len(delim) == 1 && code[j] == '\n' && c != '`' {
					break // unterminated on this line
				}
				if strings.HasPrefix(code[j:], delim) {
					end = j
					break
				}
			}
			if end < 0 {
				continue // a stray quote (apostrophe in text): not a literal
			}
			v := code[body:end]
			interp := strings.Contains(prefix, "f") && strings.Contains(v, "{") ||
				c == '`' && strings.Contains(v, "${") ||
				c == '"' && (strings.Contains(v, "#{") || dollarVarRe.MatchString(v))
			lits = append(lits, lit{val: v, start: p, end: end + len(delim), interp: interp})
			i = end + len(delim) - 1
		}
	}
	return lits, comments
}

var (
	dollarVarRe = regexp.MustCompile(`\$[A-Za-z_{(]`)
	// interpolations turned into a shell expansion before parsing a command line
	interpRe = regexp.MustCompile(`#\{[^}]*\}|\{[^{}]*\}|%\([A-Za-z_]+\)[sd]|%[sdrif]`)
)

// code analyses interpreter code.
func (s *scanner) code(text string, c ctx) {
	if settingsCodeRe.MatchString(text) {
		s.reasons.add("runs code that touches Claude Code settings")
	}
	if c.depth > maxDepth || !mentionRe.MatchString(text) {
		return
	}
	lits, comments := literals(text)
	inSpan := func(k int) bool {
		for _, cm := range comments {
			if k >= cm[0] && k < cm[1] {
				return true
			}
		}
		return false
	}
	for _, m := range mentionRe.FindAllStringIndex(text, -1) {
		found := false
		for _, l := range lits {
			if m[0] >= l.start && m[1] <= l.end {
				found = true
				break
			}
		}
		if !found && !inSpan(m[0]) {
			s.reasons.add(reasonUnresolved)
		}
	}
	for i := 0; i < len(lits); i++ {
		l := lits[i]
		if !mentionRe.MatchString(l.val) {
			continue
		}
		v := strings.TrimSpace(l.val)
		if !strings.ContainsAny(v, " \t\n") {
			if !strings.EqualFold(base(v), BinaryName) {
				continue // a path such as ~/.config/telegram-agent-cli/x: the path rules see it
			}
			s.argvList(text, lits, i)
			continue
		}
		// a command line in one string
		before := strings.TrimRight(text[:l.start], " \t")
		after := strings.TrimLeft(text[l.end:], " \t")
		if strings.HasSuffix(before, "+") || strings.HasSuffix(before, ".") ||
			strings.HasPrefix(after, "+") || strings.HasPrefix(after, "%") ||
			strings.HasPrefix(after, ".") && !strings.HasPrefix(after, ".split") {
			s.reasons.add(reasonUnresolved)
			continue
		}
		cmd := l.val
		if l.interp {
			cmd = interpRe.ReplaceAllLiteralString(cmd, "$__X")
		}
		s.str(cmd, c)
	}
}

// argvList checks an argv list whose first element is lits[i] (the binary).
func (s *scanner) argvList(text string, lits []lit, i int) {
	var args []token
	pos := lits[i].end
	for k := i + 1; ; k++ {
		rest := strings.TrimLeft(text[pos:], " \t\r\n\\")
		if rest == "" {
			s.reasons.add(reasonUnresolved)
			return
		}
		switch rest[0] {
		case ']', ')', '}':
			s.reasons.add(checkInvocation(args))
			return
		case ',':
			rest = strings.TrimLeft(rest[1:], " \t\r\n\\")
			if rest != "" && strings.IndexByte("])}", rest[0]) >= 0 {
				s.reasons.add(checkInvocation(args))
				return
			}
			if k >= len(lits) || lits[k].start != len(text)-len(rest) {
				s.reasons.add(reasonUnresolved) // the next element is not a literal
				return
			}
			args = append(args, token{kind: tWord, text: lits[k].val, pat: escMeta(lits[k].val), dynamic: lits[k].interp})
			pos = lits[k].end
		default:
			s.reasons.add(reasonUnresolved) // BIN = 'telegram-agent-cli', 'x' + …
			return
		}
	}
}

// codeFlags are each interpreter's inline-code flags.
var codeFlags = map[string]map[string]bool{
	"python":    set("-c"),
	"node":      set("-e", "--eval", "-p", "--print"),
	"bun":       set("-e", "--eval", "-p", "--print"),
	"ruby":      set("-e"),
	"perl":      set("-e", "-E"),
	"php":       set("-r"),
	"lua":       set("-e"),
	"Rscript":   set("-e"),
	"osascript": set("-e"),
}

// interpValues are flags that take a separate value (not code).
var interpValues = set("-W", "-X", "-r", "--require", "-I", "-M", "-m", "-C", "-l", "-x", "--input-type",
	"--import", "--loader", "-f", "-F", "-v")

// interpreter analyses an interpreter run: inline code, a script file, or code on stdin.
func (s *scanner) interpreter(name string, args []token, cmd *command, c ctx) {
	key := name
	if pythonRe.MatchString(name) {
		key = "python"
	}
	flags := codeFlags[key]
	sawCode, sawScript := false, false
	runCode := func(t token) {
		sawCode = true
		if len(t.subs) > 0 {
			s.reasons.add("runs interpreter code built at run time")
		}
		s.code(t.text, c)
	}
	awk := strings.HasSuffix(name, "awk")
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch {
		case flags[t]:
			if i+1 < len(args) {
				runCode(args[i+1])
				i++
			}
			if key == "python" {
				return // the rest is argv
			}
			continue
		case key == "python" && t == "-m", key == "perl" && t == "-M":
			return // a module
		case name == "deno" && t == "eval":
			if i+1 < len(args) {
				runCode(args[i+1])
			}
			return
		case t == "run" && (name == "deno" || name == "bun"):
			continue
		case t == "-":
			s.stdinCode(cmd, c, false) // code on stdin, the rest is argv
			return
		case awk && t == "-f":
			if i+1 < len(args) {
				s.file(args[i+1].text, modeText, c)
				sawScript = true
				i++
			}
			continue
		case interpValues[t]:
			i++
			continue
		case strings.HasPrefix(t, "-") && len(t) > 1:
			// a cluster such as python -Ic CODE or -cCODE
			if key == "python" && t[1] != '-' {
				if k := strings.IndexByte(t, 'c'); k > 0 {
					if k == len(t)-1 && i+1 < len(args) {
						runCode(args[i+1])
					} else if k < len(t)-1 {
						runCode(token{text: t[k+1:]})
					}
					return
				}
			}
			continue
		}
		if sawCode {
			if key == "python" {
				return
			}
			continue
		}
		if awk {
			runCode(args[i])
			return
		}
		s.file(t, modeText, c)
		return
	}
	if !sawCode && !sawScript {
		s.stdinCode(cmd, c, false)
	}
}

// stdinCode analyses what a shell (asShell) or interpreter reads on stdin.
func (s *scanner) stdinCode(cmd *command, c ctx, asShell bool) {
	if cmd == nil {
		return
	}
	run := func(text string) {
		if asShell {
			s.str(text, c)
		} else {
			s.code(text, c)
		}
	}
	for _, h := range cmd.heredocs {
		run(h.body)
	}
	for _, hs := range cmd.herestrings {
		run(hs.text)
	}
	if len(cmd.heredocs) == 0 && len(cmd.herestrings) == 0 && cmd.pipeIn {
		s.pipedIn(cmd, c, run)
	}
}

func errorsIsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
