package hook

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

// The one protected variable is TGAC_READONLY: setting it to a value that
// turns read-only off is what lets the binary write. Only real assignments
// count (NAME=v before a command, env, export/declare, read/printf -v, for);
// the name inside data or code text is not an assignment.

var reasonReadOnly = "sets " + config.EnvReadOnly + " to a non-read-only value"

// E2EAllowedFor returns the e2e.allowed_chats entries of each data dir's config.json.
func E2EAllowedFor(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		b, err := readScript(filepath.Join(d, "config.json"))
		if err != nil {
			continue
		}
		var c struct {
			E2E struct {
				Allowed []string `json:"allowed_chats"`
			} `json:"e2e"`
		}
		if json.Unmarshal(b, &c) == nil {
			out = append(out, c.E2E.Allowed...)
		}
	}
	return out
}

// readOnlyOff reports whether an assignment of TGAC_READONLY needs the owner:
// a value the guard can't read, or one that turns read-only off.
func readOnlyOff(a assign) bool {
	return !a.known || !config.ReadOnlyFrom(a.value)
}

// assign describes what follows a variable name: whether it is assigned and,
// when the value is a plain literal, the value.
type assign struct {
	ok, known bool
	value     string
}

// assignments checks NAME=value words: the prefix of a command (including
// env's), a line of bare assignments, and export/declare operands.
func (s *scanner) assignments(words []token, idx int) {
	end := idx
	if idx < 0 {
		end = len(words)
	}
	check := func(w token) {
		name, v, ok := strings.Cut(w.text, "=")
		if ok && strings.TrimSuffix(name, "+") == config.EnvReadOnly &&
			readOnlyOff(assign{ok: true, known: !w.dynamic && !strings.HasSuffix(name, "+"), value: v}) {
			s.reasons.add(reasonReadOnly)
		}
	}
	for _, w := range words[:end] {
		if assignRe.MatchString(w.text) {
			check(w)
		}
	}
	if idx >= 0 {
		switch base(words[idx].text) {
		case "export", "declare", "typeset", "local", "readonly":
			for _, w := range words[idx+1:] {
				if assignRe.MatchString(w.text) {
					check(w)
				}
			}
		}
	}
}

// fallback*Re find visible invocations and assignments in a command the lexer can't read.
var (
	fallbackCallRe = regexp.MustCompile("(?:^|[\\s;&|(`])(?:\\S*/)?" + regexp.QuoteMeta(BinaryName) +
		"((?:[ \\t]+[^\\s;&|)`]+)*)")
	fallbackAssignRe = regexp.MustCompile(`(?:^|[\s;&|(])(?:export[ \t]+)?` + config.EnvReadOnly + `=([^\s;&|)]*)`)
)

// fallback checks a command the lexer can't read (bash can't run it either):
// each visible invocation of the binary and each TGAC_READONLY= assignment.
func (s *scanner) fallback(text string) {
	for _, m := range fallbackCallRe.FindAllStringSubmatch(text, -1) {
		var args []token
		for _, f := range strings.Fields(m[1]) {
			f = strings.Trim(f, `'"`)
			args = append(args, token{kind: tWord, text: f, dynamic: strings.ContainsAny(f, "$`")})
		}
		s.reasons.add(checkInvocation(args, s.o))
	}
	for _, m := range fallbackAssignRe.FindAllStringSubmatch(text, -1) {
		v := strings.Trim(m[1], `'"`)
		if readOnlyOff(assign{ok: true, known: !strings.ContainsAny(v, "$`"), value: v}) {
			s.reasons.add(reasonReadOnly)
		}
	}
}

// nameTargets returns variables a builtin assigns by name, and for each
// whether the assigned value is a plain literal ("" + false when unseen).
func nameTargets(prog string, args []token) []struct {
	name  string
	value assign
} {
	type target = struct {
		name  string
		value assign
	}
	var out []target
	add := func(n string, a assign) { out = append(out, target{n, a}) }
	unseen := assign{ok: true}
	operands := func(values map[string]bool) []token {
		var ops []token
		for i := 0; i < len(args); i++ {
			t := args[i].text
			if t == "--" {
				return append(ops, args[i+1:]...)
			}
			if strings.HasPrefix(t, "-") && len(t) > 1 {
				if values[t] {
					i++
				}
				continue
			}
			ops = append(ops, args[i])
		}
		return ops
	}
	switch prog {
	case "read":
		for i, a := range args {
			if a.text == "-a" && i+1 < len(args) {
				add(args[i+1].text, unseen)
			}
		}
		for _, a := range operands(set("-a", "-d", "-i", "-n", "-N", "-p", "-t", "-u")) {
			add(a.text, unseen)
		}
	case "printf":
		for i, a := range args {
			if a.text == "-v" && i+1 < len(args) {
				add(args[i+1].text, unseen)
			} else if v, ok := strings.CutPrefix(a.text, "-v"); ok && v != "" {
				add(v, unseen)
			}
		}
	case "mapfile", "readarray":
		for _, a := range operands(set("-d", "-n", "-O", "-s", "-u", "-C", "-c")) {
			add(a.text, unseen)
		}
	case "getopts":
		if len(args) > 1 {
			add(args[1].text, unseen)
		}
	case "export", "declare", "typeset", "local", "readonly":
		nameref := false
		for _, a := range args {
			if strings.HasPrefix(a.text, "-") && strings.Contains(a.text, "n") {
				nameref = true
			}
		}
		for _, a := range operands(nil) {
			n, v, hasValue := strings.Cut(a.text, "=")
			switch {
			case nameref && hasValue:
				add(v, unseen) // declare -n REF=NAME: writes to REF go to NAME
			case !hasValue:
				add(n, assign{}) // export NAME: exports the current value
			}
		}
	case "setenv": // csh
		if len(args) > 0 {
			v := ""
			if len(args) > 1 {
				v = args[1].text
			}
			add(args[0].text, literalWord(v, len(args) > 1 && !args[1].dynamic))
		}
	case "launchctl":
		if len(args) > 1 && args[0].text == "setenv" {
			v := ""
			if len(args) > 2 {
				v = args[2].text
			}
			add(args[1].text, literalWord(v, len(args) > 2 && !args[2].dynamic))
		}
	case "set": // fish: set -gx NAME VALUE
		ops := operands(nil)
		if len(ops) > 0 && len(ops) < len(args) {
			v := ""
			if len(ops) > 1 {
				v = ops[1].text
			}
			add(ops[0].text, literalWord(v, len(ops) > 1 && !ops[1].dynamic))
		}
	}
	return out
}

func literalWord(v string, known bool) assign { return assign{ok: true, known: known, value: v} }

// envTargets checks builtins and loops that assign TGAC_READONLY by name.
func (s *scanner) envTargets(words []token, idx int) {
	// for NAME in …; select NAME in …
	for i := 0; i+1 < len(words); i++ {
		t := words[i].text
		if keywords[t] {
			continue
		}
		if (t == "for" || t == "select") && words[i+1].text == config.EnvReadOnly {
			s.reasons.add(reasonReadOnly)
		}
		break
	}
	if idx < 0 {
		return
	}
	for _, t := range nameTargets(base(words[idx].text), words[idx+1:]) {
		if t.name == config.EnvReadOnly && t.value.ok && readOnlyOff(t.value) {
			s.reasons.add(reasonReadOnly)
		}
	}
}
