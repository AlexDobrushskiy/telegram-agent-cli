package hook

import (
	"regexp"
	"strings"
)

const (
	reasonBuilt  = "runs a command string built at run time (the guard can't see it)"
	reasonUnseen = "runs a script the guard can't see"
)

// gluedValue returns the value of a command flag written as --flag=V or -fV.
func gluedValue(f string, flags map[string]bool) (string, bool) {
	for k := range flags {
		if strings.HasPrefix(k, "--") {
			if v, ok := strings.CutPrefix(f, k+"="); ok {
				return v, true
			}
		} else if len(k) == 2 && len(f) > 2 && strings.HasPrefix(f, k) && f[1] != '-' {
			return f[2:], true
		}
	}
	return "", false
}

// cmdString analyses a word holding a shell command (sh -c, watch, hyperfine).
func (s *scanner) cmdString(w token, c ctx) {
	if len(w.subs) > 0 {
		s.hidden(reasonBuilt, s.subsDecode(w.subs))
	}
	s.str(w.text, c)
}

// scriptArg analyses a script named by a word (bash FILE, source FILE).
func (s *scanner) scriptArg(w token, c ctx) {
	if w.dynamic || w.glob {
		s.hidden(reasonUnseen+" ("+w.text+")", s.subsDecode(w.subs))
		return
	}
	s.file(w.text, modeShell, c)
}

var xargsValues = set("-n", "-P", "-L", "-d", "-E", "-s", "-a", "-l",
	"--max-args", "--max-procs", "--delimiter", "--arg-file", "--max-lines", "--eof")

// xargsArgs rewrites args for a program run by xargs (found in the wrapper
// words before it): with -I/-J/-i/--replace the replace string is an
// expansion; without, xargs appends words, so an expansion is added at the end.
func xargsArgs(prefix, args []token) []token {
	at := -1
	for i, w := range prefix {
		if base(w.text) == "xargs" && !w.dynamic {
			at = i
		}
	}
	if at < 0 {
		return args
	}
	repl := ""
	for i := at + 1; i < len(prefix); i++ {
		t := prefix[i].text
		switch {
		case (t == "-I" || t == "-J") && i+1 < len(prefix):
			repl = prefix[i+1].text
			i++
		case strings.HasPrefix(t, "-I") && len(t) > 2, strings.HasPrefix(t, "-J") && len(t) > 2:
			repl = t[2:]
		case t == "-i" || t == "--replace":
			repl = "{}"
		case strings.HasPrefix(t, "-i") && len(t) > 2:
			repl = t[2:]
		case strings.HasPrefix(t, "--replace="):
			repl = strings.TrimPrefix(t, "--replace=")
		case xargsValues[t]:
			i++
		}
	}
	out := make([]token, len(args))
	copy(out, args)
	if repl == "" {
		return append(out, token{kind: tWord, text: "$XARGS", pat: string(rune(markUnknown)), dynamic: true})
	}
	for i := range out {
		if strings.Contains(out[i].text, repl) {
			out[i].dynamic = true
		}
	}
	return out
}

var parallelValues = set("-j", "--jobs", "-S", "--sshlogin", "-a", "--arg-file", "--colsep", "-C", "-N",
	"-n", "-L", "--delay", "--timeout", "--joblog", "--results", "--tmpdir", "-I", "--replace",
	"--basefile", "--env", "-E", "--load", "--memfree", "--retries", "--sshloginfile", "--workdir", "--wd",
	"--tag-string", "--rpl", "--block", "--max-args", "--max-lines", "--halt")

// placeholderPat matches GNU parallel replacement strings: {}, {.}, {/}, {1}, {#}…
var placeholderPat = regexp.MustCompile(`\{[0-9]*[./#%^]*[./]*\}`)

// parallel analyses GNU parallel: the command words are joined and run by a
// shell, with {…} replaced by the input; without a placeholder the input is appended.
func (s *scanner) parallel(args []token, c ctx) {
	var cmd []string
	var inputs []token
	repl := "{}"
	pipe := false
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i].text, "-"); i++ {
		t := args[i].text
		if t == "--pipe" || t == "--pipepart" {
			pipe = true
		}
		if t == "-I" && i+1 < len(args) {
			repl = args[i+1].text
		}
		if parallelValues[t] {
			i++
		}
	}
	for ; i < len(args); i++ {
		t := args[i].text
		if strings.HasPrefix(t, ":::") || t == "::::" {
			inputs = args[i+1:]
			break
		}
		cmd = append(cmd, t)
	}
	if len(cmd) == 0 {
		// no command: each input is a command line
		for _, in := range inputs {
			if !strings.HasPrefix(in.text, ":::") {
				s.cmdString(in, c)
			}
		}
		return
	}
	line := strings.Join(cmd, " ")
	placed := strings.Contains(line, repl) || placeholderPat.MatchString(line)
	line = strings.ReplaceAll(line, repl, "$P")
	line = placeholderPat.ReplaceAllLiteralString(line, "$P")
	if !placed && !pipe {
		line += " $P"
	}
	s.str(line, c)
}

// watch runs its arguments joined, through sh -c (or as argv with -x).
func (s *scanner) watch(args []token, c ctx) {
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if t == "-x" || t == "--exec" {
			s.argv(args[i+1:], nil, c)
			return
		}
		if strings.HasPrefix(t, "-") {
			if t == "-n" || t == "--interval" {
				i++
			}
			continue
		}
		rest := args[i:]
		for _, a := range rest {
			if len(a.subs) > 0 {
				s.hidden(reasonBuilt, s.subsDecode(a.subs))
			}
		}
		s.str(joinWords(rest), c)
		return
	}
}

var hyperfineValues = set("-w", "--warmup", "-m", "--min-runs", "-M", "--max-runs", "-r", "--runs",
	"-s", "--setup", "-p", "--prepare", "-c", "--cleanup", "-P", "--parameter-scan", "-D",
	"--parameter-step-size", "-L", "--parameter-list", "-S", "--shell", "--style", "--export-json",
	"--export-markdown", "--export-csv", "-n", "--command-name", "-u", "--time-unit", "--output", "-i")

// hyperfine runs each operand and its --setup/--prepare/--cleanup values as shell commands.
func (s *scanner) hyperfine(args []token, c ctx) {
	cmdValues := set("-s", "--setup", "-p", "--prepare", "-c", "--cleanup")
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if strings.HasPrefix(t, "-") {
			if hyperfineValues[t] && i+1 < len(args) {
				if cmdValues[t] {
					s.cmdString(args[i+1], c)
				}
				i++
			}
			continue
		}
		s.cmdString(args[i], c)
	}
}

var tmuxValues = set("-t", "-s", "-n", "-c", "-F", "-x", "-y", "-e", "-l", "-L", "-S", "-f", "-N")

// tmux analyses commands tmux runs or types into a pane.
func (s *scanner) tmux(args []token, c ctx) {
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i].text, "-"); i++ {
		if tmuxValues[args[i].text] {
			i++
		}
	}
	if i >= len(args) {
		return
	}
	sub := args[i].text
	var rest []token
	for i++; i < len(args); i++ {
		t := args[i].text
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			if tmuxValues[t] {
				i++
			}
			continue
		}
		rest = append(rest, args[i])
	}
	switch sub {
	case "new-session", "new", "new-window", "neww", "split-window", "splitw", "respawn-pane", "respawnp",
		"respawn-window", "respawnw", "run-shell", "run", "pipe-pane", "pipep", "display-popup", "popup":
		if len(rest) > 0 {
			s.str(joinWords(rest), c)
		}
	case "send-keys", "send":
		var typed []string
		for _, w := range rest {
			switch w.text {
			case "Enter", "C-m", "KPEnter", "C-j", "Space", "Tab":
				typed = append(typed, "\n")
			default:
				typed = append(typed, w.text)
			}
		}
		s.str(strings.Join(typed, " "), c)
	}
}
