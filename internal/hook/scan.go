package hook

import (
	"bytes"
	"regexp"
	"strings"
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// wrapper is a program that runs another command after its own flags.
type wrapper struct {
	values     map[string]bool // flags that take a separate value
	positional int             // operands before the command (timeout DURATION)
	cmdFlag    map[string]bool // flags whose value is a whole command string
}

var uvValues = set("--with", "--python", "-p", "--directory", "--project", "--package", "--from",
	"--env-file", "--extra", "--group", "--index", "--with-requirements", "--with-editable")

var wrappers = map[string]wrapper{
	"sudo": {values: set("-u", "-g", "-C", "-D", "-h", "-p", "-R", "-T", "-U", "-r", "-t",
		"--user", "--group", "--chdir", "--prompt", "--host", "--role", "--type", "--other-user")},
	"doas":       {values: set("-u", "-C")},
	"env":        {values: set("-u", "-C", "-P", "--unset", "--chdir"), cmdFlag: set("-S", "--split-string")},
	"exec":       {values: set("-a")},
	"command":    {},
	"builtin":    {},
	"nohup":      {},
	"time":       {},
	"caffeinate": {values: set("-t", "-w")},
	"nice":       {values: set("-n", "--adjustment")},
	"ionice":     {values: set("-c", "-n", "-p")},
	"timeout":    {values: set("-s", "-k", "--signal", "--kill-after"), positional: 1},
	"stdbuf":     {values: set("-i", "-o", "-e")},
	"xargs": {values: set("-I", "-n", "-P", "-L", "-d", "-E", "-s", "-a",
		"--max-args", "--max-procs", "--delimiter", "--arg-file", "--max-lines")},
	"watch":  {values: set("-n", "--interval")},
	"flock":  {values: set("-w", "-E", "--timeout", "--conflict-exit-code"), positional: 1, cmdFlag: set("-c", "--command")},
	"uv":     {values: uvValues},
	"uvx":    {values: uvValues},
	"run":    {values: uvValues},
	"poetry": {},
	"pipx":   {values: set("--spec", "--python")},
}

// keywords are shell words that can precede a command.
var keywords = set("!", "{", "}", "if", "then", "else", "elif", "fi", "while", "until", "do", "done", "coproc", "esac")

// noCommand words start a construct whose words are not a command.
var noCommand = set("for", "select", "case", "function", "in")

var shells = set("sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "fish")

var interpreters = set("node", "ruby", "perl", "php", "bun", "deno", "Rscript", "tclsh", "lua")

var pythonRe = regexp.MustCompile(`^python[0-9.]*$`)

// inertConsumers only read a heredoc as data.
var inertConsumers = set("cat", "tee", "head", "tail", "wc", "sort", "uniq", "git", "gh", "pbcopy",
	"less", "more", "grep", "jq", "yq", "column", "fold", "fmt", "diff")

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)

var sshValues = set("-b", "-c", "-D", "-E", "-e", "-F", "-I", "-i", "-J", "-L", "-l", "-m", "-O",
	"-o", "-p", "-Q", "-R", "-S", "-W", "-w", "-B")

var goValues = set("-C", "-o", "-p", "-asmflags", "-buildmode", "-compiler", "-gccgoflags", "-gcflags",
	"-installsuffix", "-ldflags", "-mod", "-modfile", "-overlay", "-pgo", "-pkgdir", "-tags",
	"-toolexec", "-exec", "-coverpkg", "-covermode")

const maxDepth = 8

type ctx struct {
	file     bool // inside a script file
	mentions bool // the file mentions the binary
	depth    int
}

type scanner struct {
	o       Options
	reasons reasonSet
	dirRes  []*regexp.Regexp
	inert   [][2]int          // heredoc bodies only read as data (blanked for text rules)
	written map[string]string // files written by those heredocs in this command
	cond    []string          // reasons that count only if the command mentions the binary
	files   map[string]bool
	env     map[string]envKind // protected variables
	dirs    [][]string         // data dirs as path components
	st      pathState
}

func newScanner(o Options) *scanner {
	if o.ReadFile == nil {
		o.ReadFile = readScript
	}
	return &scanner{o: o, dirRes: dirPatterns(o), written: map[string]string{}, files: map[string]bool{}, env: envNames(o),
		dirs: dataDirComps(o), st: newPathState(o)}
}

// scanTop analyses the command and returns the text the text rules should see.
func (s *scanner) scanTop(cmd string) string {
	lx, err := lex(cmd, 0)
	if err != nil {
		if mentionRe.MatchString(cmd) {
			s.reasons.add("could not parse the shell command")
		}
		return cmd
	}
	s.script(lx, ctx{})
	text := blank(cmd, s.inert)
	if mentionRe.MatchString(text) {
		for _, r := range s.cond {
			s.reasons.add(r)
		}
	}
	return text
}

func blank(s string, ranges [][2]int) string {
	if len(ranges) == 0 {
		return s
	}
	b := []byte(s)
	for _, r := range ranges {
		if r[0] < 0 || r[1] > len(b) || r[0] > r[1] {
			continue
		}
		for i := r[0]; i < r[1]; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

// abs makes p absolute against the command's current directory.
func (s *scanner) abs(p string) string {
	o := s.o
	if s.st.cwd != "" {
		o.Cwd = s.st.cwd
	}
	return absPath(p, o)
}

// condAdd records a reason that only matters when the binary is mentioned.
func (s *scanner) condAdd(c ctx, r string) {
	if c.file {
		if c.mentions {
			s.reasons.add(r)
		}
		return
	}
	s.cond = append(s.cond, r)
}

func (s *scanner) script(lx *lexed, c ctx) {
	if c.depth > maxDepth {
		s.condAdd(c, "command nests too deep to check")
		return
	}
	cmds, err := parse(lx)
	if err != nil {
		s.condAdd(c, "could not parse the shell command")
		return
	}
	for _, cmd := range cmds {
		s.command(cmd, c)
	}
}

// str analyses a command given as a string (sh -c, eval, ssh).
func (s *scanner) str(code string, c ctx) {
	c.depth++
	lx, err := lex(code, -1)
	if err != nil {
		s.condAdd(c, "could not parse the shell command")
		return
	}
	s.script(lx, c)
}

func (s *scanner) command(cmd *command, c ctx) {
	sub := c
	sub.depth++
	for _, lx := range cmd.subs {
		s.script(lx, sub)
	}
	idx, _ := skipPrefix(cmd.words)
	prog := ""
	if idx >= 0 {
		prog = base(cmd.words[idx].text)
	}
	if inertConsumers[prog] && !cmd.pipeOut && len(cmd.heredocs) > 0 {
		targets := append([]string{}, cmd.outFiles...)
		if prog == "tee" {
			for _, w := range cmd.words[idx+1:] {
				if !strings.HasPrefix(w.text, "-") {
					targets = append(targets, w.text)
				}
			}
		}
		for _, h := range cmd.heredocs {
			if h.start >= 0 && (h.quoted || len(h.subs) == 0) {
				s.inert = append(s.inert, [2]int{h.start, h.end})
			}
			for _, t := range targets {
				s.written[s.abs(t)] = h.body
			}
		}
	}
	data := s.markData(cmd.words, idx)
	for i, w := range cmd.words {
		if !data[i] {
			s.envText(w.text) // after quote removal: TGAC_READ''ONLY=0, \=, $'…'
		}
	}
	s.envTargets(cmd.words, idx)
	s.pathRules(cmd, idx, data)
	s.argv(cmd.words, cmd, c)
	s.track(cmd, idx)
}

// dataFlags carry message text (commit messages, PR titles and bodies), not code.
var dataFlags = set("-m", "--message", "--body", "--title")

// vcs take a message after a short-flag cluster ending in m.
var vcs = set("git", "hg", "jj", "sl", "svn")

var greps = set("grep", "egrep", "fgrep", "rg", "ag", "ack")

var grepValues = set("-A", "-B", "-C", "-m", "-f", "-g", "-t", "-T", "-d", "-D", "--type", "--glob",
	"--max-count", "--include", "--exclude", "--context", "--file")

// markData excludes message-text flag values and search patterns from the text
// rules and returns their indices in words. Words with expansions or
// substitutions stay checked.
func (s *scanner) markData(words []token, idx int) map[int]bool {
	data := map[int]bool{}
	if idx < 0 {
		return data
	}
	mark := func(i int) {
		w := words[i]
		if w.dynamic {
			return
		}
		data[i] = true
		if w.start >= 0 {
			s.inert = append(s.inert, [2]int{w.start, w.end})
		}
	}
	first := idx + 1
	prog := base(words[idx].text)
	for i := first; i < len(words); i++ {
		t := words[i].text
		short := vcs[prog] && len(t) > 2 && t[0] == '-' && t[1] != '-' && t[len(t)-1] == 'm' // git commit -am MSG
		if (dataFlags[t] || short || prog == "gh" && (t == "-b" || t == "-t")) && i+1 < len(words) {
			mark(i + 1)
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.HasPrefix(k, "--") && dataFlags[k] {
			mark(i)
		}
	}
	if prog == "git" {
		for i := first; i < len(words); i++ {
			t := words[i].text
			if t == "-C" || t == "-c" {
				i++
				continue
			}
			if !strings.HasPrefix(t, "-") {
				if t == "grep" {
					prog, first = "grep", i+1
				}
				break
			}
		}
	}
	if !greps[prog] {
		return data
	}
	explicit := false
	for i := first; i < len(words); i++ {
		t := words[i].text
		if (t == "-e" || t == "--regexp") && i+1 < len(words) {
			mark(i + 1)
			explicit = true
		} else if strings.HasPrefix(t, "--regexp=") {
			mark(i)
			explicit = true
		}
	}
	if explicit {
		return data
	}
	for i := first; i < len(words); i++ {
		t := words[i].text
		if t == "--" {
			if i+1 < len(words) {
				mark(i + 1)
			}
			return data
		}
		if strings.HasPrefix(t, "-") {
			if grepValues[t] {
				i++
			}
			continue
		}
		mark(i)
		return data
	}
	return data
}

// skipPrefix returns the index of the program word (-1 if none) and any
// command strings carried in wrapper flags (env -S, flock -c).
func skipPrefix(words []token) (int, []string) {
	var nested []string
	i := 0
	for i < len(words) {
		t := words[i].text
		if assignRe.MatchString(t) || keywords[t] {
			i++
			continue
		}
		if noCommand[t] {
			return -1, nested
		}
		w, ok := wrappers[base(t)]
		if !ok || words[i].dynamic {
			return i, nested
		}
		i++
		// flags, the wrapper's own operands, then flags again (flock LOCK -c CMD)
		for pass := 0; pass < 2; pass++ {
			for i < len(words) && strings.HasPrefix(words[i].text, "-") {
				f := words[i].text
				i++
				if f == "--" {
					break
				}
				if w.cmdFlag[f] && i < len(words) {
					return -1, append(nested, words[i].text)
				}
				if w.values[f] {
					i++
				}
			}
			for k := 0; pass == 0 && k < w.positional && i < len(words); k++ {
				i++
			}
		}
	}
	return -1, nested
}

// argv analyses one command's words. cmd is nil for derived argvs (find -exec).
func (s *scanner) argv(words []token, cmd *command, c ctx) {
	idx, nested := skipPrefix(words)
	for _, n := range nested {
		s.str(n, c)
	}
	if idx < 0 {
		return
	}
	prog, args := words[idx], words[idx+1:]
	if prog.dynamic || prog.glob {
		s.condAdd(c, "runs a program named by an expansion")
		return
	}
	name := base(prog.text)
	switch {
	case strings.EqualFold(name, BinaryName):
		s.reasons.add(checkInvocation(args))
	case name == "alias":
		for _, a := range args {
			if mentionRe.MatchString(a.text) {
				s.reasons.add("defines an alias for " + BinaryName)
			}
		}
	case shells[name]:
		s.shell(args, cmd, c)
	case name == "source" || name == ".":
		if len(args) > 0 {
			s.file(args[0].text, modeShell, c)
		}
	case name == "eval":
		s.str(joinWords(args), c)
	case name == "ssh":
		s.ssh(args, cmd, c)
	case name == "su":
		for i, a := range args {
			if (a.text == "-c" || a.text == "--command") && i+1 < len(args) {
				s.str(args[i+1].text, c)
			} else if v, ok := strings.CutPrefix(a.text, "--command="); ok {
				s.str(v, c)
			}
		}
	case name == "find":
		for i := 0; i < len(args); i++ {
			switch args[i].text {
			case "-exec", "-execdir", "-ok", "-okdir":
				j := i + 1
				for j < len(args) && args[j].text != ";" && args[j].text != "+" {
					j++
				}
				s.argv(args[i+1:j], nil, c)
				i = j
			}
		}
	case name == "go":
		s.goRun(args, c)
	case pythonRe.MatchString(name) || interpreters[name]:
		s.interpreter(args, c)
	case strings.Contains(prog.text, "/"):
		s.file(prog.text, modeAuto, c)
	}
}

func joinWords(ws []token) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.text
	}
	return strings.Join(parts, " ")
}

func (s *scanner) shell(args []token, cmd *command, c ctx) {
	cflag := false
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if t == "--" || t == "-" {
			continue
		}
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
			if t[0] == '-' && !strings.HasPrefix(t, "--") && strings.ContainsRune(t[1:], 'c') {
				cflag = true
			}
			switch t {
			case "-o", "+o", "-O", "+O", "--rcfile", "--init-file":
				i++
			}
			continue
		}
		if cflag {
			s.str(t, c)
		} else {
			s.file(t, modeShell, c)
		}
		return
	}
	s.stdinScript(cmd, c)
}

// stdinScript analyses what a shell reads on stdin: heredocs, herestrings or a pipe.
func (s *scanner) stdinScript(cmd *command, c ctx) {
	if cmd == nil {
		return
	}
	for _, h := range cmd.heredocs {
		s.str(h.body, c)
	}
	for _, hs := range cmd.herestrings {
		s.str(hs.text, c)
	}
	if len(cmd.heredocs) == 0 && len(cmd.herestrings) == 0 && cmd.pipeIn {
		s.condAdd(c, "pipes a script into a shell")
	}
}

func (s *scanner) ssh(args []token, cmd *command, c ctx) {
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if strings.HasPrefix(t, "-") {
			if sshValues[t] {
				i++
			}
			continue
		}
		if rest := args[i+1:]; len(rest) > 0 {
			s.str(joinWords(rest), c)
		} else {
			s.stdinScript(cmd, c)
		}
		return
	}
}

func (s *scanner) goRun(args []token, c ctx) {
	if len(args) == 0 || args[0].text != "run" {
		return
	}
	for j := 1; j < len(args); j++ {
		t := args[j].text
		if strings.HasPrefix(t, "-") {
			if goValues[t] {
				j++
			}
			continue
		}
		pkg := t
		if k := strings.LastIndex(pkg, "@"); k > 0 {
			pkg = pkg[:k]
		}
		pkg = strings.TrimSuffix(pkg, "/")
		if base(pkg) == BinaryName || (strings.HasPrefix(pkg, ".") && base(absPath(pkg, s.o)) == BinaryName) {
			s.reasons.add(checkInvocation(args[j+1:]))
			return
		}
		for ; j < len(args) && strings.HasSuffix(args[j].text, ".go"); j++ {
			s.file(args[j].text, modeText, c)
		}
		return
	}
}

func (s *scanner) interpreter(args []token, c ctx) {
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch t {
		case "-c", "-e", "--eval", "-m", "-p", "--print":
			return // inline code: the text rules see it
		case "-W", "-X", "-r", "--require", "-I":
			i++
			continue
		case "run":
			continue // deno run / bun run
		}
		if strings.HasPrefix(t, "-") {
			continue
		}
		s.file(t, modeText, c)
		return
	}
}

type fileMode int

const (
	modeAuto fileMode = iota
	modeShell
	modeText
)

// file analyses a script the command runs: shell scripts fully, others by the text rules.
func (s *scanner) file(p string, mode fileMode, c ctx) {
	if c.depth > maxDepth {
		return
	}
	full := s.abs(p)
	if s.files[full] {
		return
	}
	s.files[full] = true
	content, ok := s.written[full]
	if !ok {
		b, err := s.o.ReadFile(full)
		if err != nil {
			return
		}
		if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
			return // a binary
		}
		content = string(b)
	}
	if mode == modeAuto && isShellScript(full, content) {
		mode = modeShell
	}
	if mode == modeShell {
		fc := ctx{file: true, mentions: mentionRe.MatchString(content), depth: c.depth + 1}
		if lx, err := lex(content, -1); err != nil {
			s.condAdd(fc, "could not parse "+base(full))
		} else {
			s.script(lx, fc)
		}
	}
	s.rawRules(content)
}

func isShellScript(p, content string) bool {
	for _, ext := range []string{".sh", ".bash", ".zsh"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	if !strings.HasPrefix(content, "#!") {
		return false
	}
	line, _, _ := strings.Cut(content, "\n")
	f := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(f) == 0 {
		return false
	}
	interp := base(f[0])
	if interp == "env" && len(f) > 1 {
		interp = f[1]
	}
	return shells[interp]
}
