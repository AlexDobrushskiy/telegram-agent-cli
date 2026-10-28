package hook

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

// Protected environment variables: TGAC_READONLY (only a non-read-only value
// matters), TGAC_HOME (any assignment or expansion) and the watcher session
// variables (any assignment or export).

type envKind int

const (
	envReadOnly envKind = iota
	envHome
	envWatcher
)

var (
	reasonReadOnly = "sets " + config.EnvReadOnly + " to a non-read-only value"
	reasonHome     = "sets or reads " + config.EnvHome
	reasonPartial  = "builds a TGAC_ variable name from parts"
)

func reasonEnv(k envKind, name string) string {
	switch k {
	case envReadOnly:
		return reasonReadOnly
	case envHome:
		return reasonHome
	}
	return "sets the watcher session variable " + name
}

// WatcherEnvsFor returns the watcher session variable names: the default plus
// watcher_session_env from each data dir's config.json.
func WatcherEnvsFor(dirs []string) []string {
	names := []string{config.DefaultWatcherSessionEnv}
	for _, d := range dirs {
		b, err := readScript(filepath.Join(d, "config.json"))
		if err != nil {
			continue
		}
		var c struct {
			Env string `json:"watcher_session_env"`
		}
		if json.Unmarshal(b, &c) == nil && isName(c.Env) && !contains1(names, c.Env) {
			names = append(names, c.Env)
		}
	}
	return names
}

func contains1(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func envNames(o Options) map[string]envKind {
	m := map[string]envKind{config.EnvReadOnly: envReadOnly, config.EnvHome: envHome, config.DefaultWatcherSessionEnv: envWatcher}
	for _, w := range o.WatcherEnvs {
		if isName(w) && w != config.EnvReadOnly && w != config.EnvHome {
			m[w] = envWatcher
		}
	}
	return m
}

// envValueBad reports whether an assignment to a protected variable needs the owner.
func envValueBad(k envKind, a assign) bool {
	if k != envReadOnly {
		return true
	}
	return !a.known || strings.TrimSpace(a.value) == "" || !config.ReadOnlyFrom(a.value)
}

var tgacPrefixRe = regexp.MustCompile(`TGAC_[A-Za-z0-9_]*`)

// envText finds assignments to protected variables in shell or code text:
// NAME=v, environ['NAME'] = v, "NAME": v, setenv("NAME", v), $ENV{NAME}=v, ${NAME:=v}.
func (s *scanner) envText(text string) {
	for name, kind := range s.env {
		for i := 0; ; {
			k := strings.Index(text[i:], name)
			if k < 0 {
				break
			}
			k += i
			i = k + len(name)
			if k > 0 && isNameChar(text[k-1]) || i < len(text) && isNameChar(text[i]) {
				continue
			}
			if kind == envHome && k > 0 && text[k-1] == '$' {
				s.reasons.add(reasonHome)
				continue
			}
			if a := assignAfter(text, k, i); a.ok && envValueBad(kind, a) {
				s.reasons.add(reasonEnv(kind, name))
			}
		}
	}
	// TGAC_ + "READONLY", 'TGAC_%s' % x, "TGAC_${X}": a name the guard cannot read
	for _, m := range tgacPrefixRe.FindAllStringIndex(text, -1) {
		if m[1] >= len(text) || strings.IndexByte("'\"+%{$`", text[m[1]]) < 0 {
			continue
		}
		part := text[m[0]:m[1]]
		for name := range s.env {
			if len(part) < len(name) && strings.HasPrefix(name, part) {
				s.reasons.add(reasonPartial)
			}
		}
	}
}

// assign describes what follows a variable name: whether it is assigned and,
// when the value is a plain literal, the value.
type assign struct {
	ok, known bool
	value     string
}

// assignAfter inspects text after a name at text[k:i].
func assignAfter(text string, k, i int) assign {
	r := text[i:]
	if k >= 2 && text[k-2:k] == "${" {
		// ${NAME=v} and ${NAME:=v} assign a default
		return assign{ok: strings.HasPrefix(r, "=") || strings.HasPrefix(r, ":=")}
	}
	if k >= 1 && text[k-1] == '$' {
		return assign{} // an expansion reads the variable
	}
	if strings.HasPrefix(r, "+=") {
		return assign{ok: true}
	}
	if strings.HasPrefix(r, "=") && !strings.HasPrefix(r, "==") {
		v, known := shellValue(r[1:])
		return assign{ok: true, known: known, value: v}
	}
	// code forms: skip closing quotes/brackets and blanks, then an operator
	j := 0
	for n := 0; j < len(r) && n < 4 && strings.IndexByte("\"'`]})>", r[j]) >= 0; n++ {
		j++
	}
	for j < len(r) && (r[j] == ' ' || r[j] == '\t') {
		j++
	}
	r = r[j:]
	var op int
	switch {
	case strings.HasPrefix(r, "=>"), strings.HasPrefix(r, "?="), strings.HasPrefix(r, ":="):
		op = 2
	case strings.HasPrefix(r, "=="), strings.HasPrefix(r, "=~"), strings.HasPrefix(r, "::"):
		return assign{}
	case strings.HasPrefix(r, "+="):
		return assign{ok: true}
	case strings.HasPrefix(r, "="), strings.HasPrefix(r, ":"), strings.HasPrefix(r, ","):
		op = 1
	default:
		return assign{}
	}
	v, known := codeValue(r[op:])
	return assign{ok: true, known: known, value: v}
}

// shellValue reads the value of a NAME=value shell word. known is false when
// it has an expansion, a substitution or anything the guard cannot evaluate.
func shellValue(r string) (string, bool) {
	if r != "" && (r[0] == '\'' || r[0] == '"') {
		end := strings.IndexByte(r[1:], r[0])
		if end < 0 {
			return "", false
		}
		v := r[1 : 1+end]
		return v, r[0] != '"' || !strings.ContainsAny(v, "$`\\")
	}
	n := 0
	for n < len(r) && strings.IndexByte(" \t\r\n;&|)<>'\"", r[n]) < 0 {
		n++
	}
	v := r[:n]
	return v, !strings.ContainsAny(v, "$`(\\{")
}

// codeValue reads a literal after an assignment operator in code.
func codeValue(r string) (string, bool) {
	r = strings.TrimLeft(r, " \t")
	p := 0
	for p < 2 && p < len(r) && strings.IndexByte("rRbBuUfF", r[p]) >= 0 {
		p++
	}
	if p < len(r) && strings.IndexByte("'\"`", r[p]) >= 0 {
		q := r[p]
		prefix := strings.ToLower(r[:p])
		delim := string(q)
		if strings.HasPrefix(r[p:], strings.Repeat(delim, 3)) {
			delim = strings.Repeat(delim, 3)
		}
		body := r[p+len(delim):]
		end := -1
		for i := 0; i+len(delim) <= len(body); i++ {
			if body[i] == '\\' {
				i++
				continue
			}
			if strings.HasPrefix(body[i:], delim) {
				end = i
				break
			}
		}
		if end < 0 {
			return "", false
		}
		v := body[:end]
		interp := strings.Contains(prefix, "f") && strings.Contains(v, "{") ||
			q == '`' && strings.Contains(v, "${") ||
			q == '"' && strings.ContainsAny(v, "$#\\") ||
			strings.Contains(v, "\\")
		return v, !interp && terminated(body[end+len(delim):])
	}
	n := 0
	for n < len(r) && (isNameChar(r[n]) || strings.IndexByte(".+-", r[n]) >= 0) {
		n++
	}
	v := r[:n]
	if v == "" {
		return "", false
	}
	literal := allDigits(v)
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off":
		literal = true
	}
	return v, literal && terminated(r[n:])
}

// terminated reports whether a literal ends its expression.
func terminated(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	return rest == "" || strings.IndexByte("\n\r;,)]}#&|'\"", rest[0]) >= 0 || strings.HasPrefix(rest, "//")
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

// envTargets checks builtins and loops that assign protected variables by name.
func (s *scanner) envTargets(words []token, idx int) {
	// for NAME in …; select NAME in …
	for i := 0; i+1 < len(words); i++ {
		t := words[i].text
		if keywords[t] {
			continue
		}
		if t == "for" || t == "select" {
			if k, ok := s.env[words[i+1].text]; ok {
				s.reasons.add(reasonEnv(k, words[i+1].text))
			}
		}
		break
	}
	if idx < 0 {
		return
	}
	for _, t := range nameTargets(base(words[idx].text), words[idx+1:]) {
		k, ok := s.env[t.name]
		if !ok {
			continue
		}
		if !t.value.ok {
			// export NAME without a value: only the watcher variable counts
			if k == envWatcher {
				s.reasons.add(reasonEnv(k, t.name))
			}
			continue
		}
		if envValueBad(k, t.value) {
			s.reasons.add(reasonEnv(k, t.name))
		}
	}
}
