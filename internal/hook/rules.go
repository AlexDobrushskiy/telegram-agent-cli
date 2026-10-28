// Package hook is the Claude Code PreToolUse guard and its settings installer.
package hook

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
)

// BinaryName is the program the guard watches for.
const BinaryName = "telegram-agent-cli"

// readAllow is the read-only command allowlist: group → allowed subcommands
// (nil = the command has no subcommands). A group alone only prints help.
var readAllow = map[string][]string{
	"chats":    {"list", "show"},
	"messages": {"list", "search", "show", "context", "mentions", "wait-new"},
	"contacts": {"search"},
	"media":    {"download"},
	"auth":     {"status"},
	"watch":    {"reply", "list", "check"}, // reply is its own gate: registry, scope, rate limits
	"hook":     {"claude-pretooluse"},
	"me":       nil,
	"doctor":   nil,
	"version":  nil,
	"help":     nil,
}

// boolFlags never take a value; any other "--flag" without "=" may eat the next word.
var boolFlags = map[string]bool{
	"-h": true, "--help": true, "--json": true, "--unread": true, "--archived": true,
	"--asc": true, "--dms": true, "--groups": true, "--mentions": true,
	"--dry-run": true, "--force": true, "--no-verify": true,
}

// secretFiles are the data-dir files the Read tool may not open without asking.
var secretFiles = map[string]bool{
	"config.json": true, "session.json": true, "peers.json": true, "pending-login.json": true,
}

var (
	// TGAC_READONLY=v as a shell assignment, and "TGAC_READONLY" = v / : v / , v in code.
	shellOverrideRe = regexp.MustCompile(`TGAC_READONLY=("[^"]*"|'[^']*'|[^\s;&|)'"]*)`)
	codeOverrideRe  = regexp.MustCompile(`TGAC_READONLY(?:["'\])]+\s*|\s+)(?:=|:|,)\s*["']?([^\s"';&|)\]}]*)|TGAC_READONLY\s*[:,]\s*["']?([^\s"';&|)\]}]*)`)
	// an assignment or expansion of TGAC_HOME, in shell or code; a bare mention is fine
	homeVarRe = regexp.MustCompile(`TGAC_HOME\+?=|\$\{?TGAC_HOME\b|TGAC_HOME["'\])]+\s*(?:=|:|,)|\b(?i:environ|getenv|printenv|env)\W{0,3}TGAC_HOME\b`)
	rawLibRe  = regexp.MustCompile(`(?i)\b(telethon|gotd|pyrogram|gramjs|mtproto)\b`)
	rawSessRe = regexp.MustCompile(`(?i)session\.json|\.session\b|StringSession|TelegramClient|FileStorage|auth_key`)
	mentionRe = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(BinaryName))
)

// Options configures a check. Zero values fall back to the real environment.
type Options struct {
	DataDirs []string                          // data dirs to protect (absolute)
	UserHome string                            // for ~ and $HOME forms
	Cwd      string                            // base for relative paths
	ReadFile func(path string) ([]byte, error) // script files named in a command
	Deadline time.Duration                     // Run's time budget (DefaultDeadline if zero)
}

// DefaultOptions protects $TGAC_HOME (if set) and the default data dir.
func DefaultOptions() Options {
	var o Options
	o.UserHome, _ = os.UserHomeDir()
	if h, err := config.DefaultHome(); err == nil {
		o.DataDirs = append(o.DataDirs, h)
	}
	if o.UserHome != "" {
		def := filepath.Join(o.UserHome, ".config", BinaryName)
		if len(o.DataDirs) == 0 || o.DataDirs[0] != def {
			o.DataDirs = append(o.DataDirs, def)
		}
	}
	return o
}

const maxScriptSize = 1 << 20

// readScript reads a script a command runs. Only regular files are opened
// (non-blocking), so a FIFO or device named as a script cannot stall the hook.
func readScript(p string) ([]byte, error) {
	st, err := os.Lstat(p)
	if err == nil && st.Mode()&os.ModeSymlink != 0 {
		var real string
		if real, err = filepath.EvalSymlinks(p); err == nil {
			p = real
			st, err = os.Lstat(p)
		}
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxScriptSize {
		return nil, fmt.Errorf("not a script: %s", p)
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if st, err = f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a script: %s", p)
	}
	return io.ReadAll(io.LimitReader(f, maxScriptSize))
}

// reasonSet keeps unique reasons in order.
type reasonSet struct {
	list []string
	seen map[string]bool
}

func (r *reasonSet) add(s string) {
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if s != "" && !r.seen[s] {
		r.seen[s] = true
		r.list = append(r.list, s)
	}
}

// CheckBash returns why a Bash command needs the owner's OK (nil = let it run).
func CheckBash(cmd string, o Options) []string {
	s := newScanner(o)
	text := s.scanTop(cmd)
	s.rawRules(text)
	return s.reasons.list
}

// rawRules are text checks that apply anywhere, code or shell.
func (s *scanner) rawRules(text string) {
	var vals []string
	for _, m := range shellOverrideRe.FindAllStringSubmatch(text, -1) {
		vals = append(vals, strings.Trim(m[1], `"'`))
	}
	for _, m := range codeOverrideRe.FindAllStringSubmatch(text, -1) {
		vals = append(vals, m[1]+m[2])
	}
	for _, v := range vals {
		if v == "" || strings.ContainsAny(v, "$`") || !config.ReadOnlyFrom(v) {
			s.reasons.add("sets " + config.EnvReadOnly + " to a non-read-only value")
			break
		}
	}
	if homeVarRe.MatchString(text) {
		s.reasons.add("sets or reads " + config.EnvHome)
	}
	for _, re := range s.dirRes {
		if re.MatchString(text) {
			s.reasons.add("touches the " + BinaryName + " data dir (session keys, config, watcher registry)")
			break
		}
	}
	if rawLibRe.MatchString(text) && rawSessRe.MatchString(text) {
		s.reasons.add("raw MTProto client code next to a session")
	}
}

// dirPatterns matches the data dirs in absolute, ~, $HOME and ${HOME} forms, plus
// the default location in any form. A longer name (telegram-agent-cli-notes) is not the dir.
func dirPatterns(o Options) []*regexp.Regexp {
	forms := []string{"/.config/" + BinaryName, "XDG_CONFIG_HOME}/" + BinaryName, "XDG_CONFIG_HOME/" + BinaryName}
	for _, d := range o.DataDirs {
		d = filepath.Clean(d)
		forms = append(forms, d)
		if real, err := filepath.EvalSymlinks(d); err == nil && real != d {
			forms = append(forms, real)
		}
		if o.UserHome != "" {
			if rel, ok := strings.CutPrefix(d, filepath.Clean(o.UserHome)+"/"); ok {
				forms = append(forms, "~/"+rel, "$HOME/"+rel, "${HOME}/"+rel)
			}
		}
	}
	const right = `(?:$|[^A-Za-z0-9_.-])`
	var res []*regexp.Regexp
	for _, f := range forms {
		left := `(?:^|[^A-Za-z0-9_-])`
		if strings.HasPrefix(f, "/") {
			left = ""
		}
		res = append(res, regexp.MustCompile(`(?i)`+left+regexp.QuoteMeta(f)+right))
	}
	// relative ".config/<name>" at a word start
	res = append(res, regexp.MustCompile(`(?i)(?:^|[\s"'=:(])\.config/`+regexp.QuoteMeta(BinaryName)+right))
	return res
}

// CheckFile returns why a file tool call needs the owner's OK ("" = allow).
func CheckFile(tool, p string, o Options) string {
	if p == "" {
		return ""
	}
	full := resolvePath(p, o)
	for _, d := range o.DataDirs {
		dir := resolvePath(d, o)
		if !within(full, dir) {
			continue
		}
		switch tool {
		case "Read":
			if secretFiles[strings.ToLower(filepath.Base(full))] {
				return "reads " + filepath.Base(full) + " in the " + BinaryName + " data dir (account secrets)"
			}
			return ""
		case "Grep":
			return "searches the " + BinaryName + " data dir (account secrets)"
		default:
			return "writes under the " + BinaryName + " data dir (session, config, watcher registry)"
		}
	}
	return ""
}

// within reports whether p is dir or inside it (case-insensitive on macOS).
func within(p, dir string) bool {
	if runtime.GOOS == "darwin" {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// absPath expands ~ and makes p absolute against the call's cwd.
func absPath(p string, o Options) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(o.UserHome, p[1:])
	}
	if !filepath.IsAbs(p) {
		base := o.Cwd
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// resolvePath is absPath with symlinks of its longest existing prefix resolved.
func resolvePath(p string, o Options) string {
	p = absPath(p, o)
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// checkInvocation returns why a run of the binary with args is not a read.
func checkInvocation(args []token) string {
	var naive, cobra []token
	help := false
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if t == "--" {
			naive = append(naive, args[i+1:]...)
			break
		}
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			help = help || t == "-h" || t == "--help"
			continue
		}
		naive = append(naive, args[i])
	}
	// cobra's view: a non-bool flag without "=" takes the next word as its value.
	for i := 0; i < len(args); i++ {
		t := args[i].text
		if t == "--" {
			cobra = append(cobra, args[i+1:]...)
			break
		}
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			if !strings.Contains(t, "=") && !boolFlags[t] {
				i++
			}
			continue
		}
		cobra = append(cobra, args[i])
	}
	if help {
		return ""
	}
	if r := checkPath(naive); r != "" {
		return r
	}
	return checkPath(cobra)
}

func checkPath(p []token) string {
	if len(p) == 0 {
		return ""
	}
	g := p[0]
	if g.dynamic || g.glob {
		return BinaryName + " subcommand comes from an expansion"
	}
	subs, ok := readAllow[g.text]
	if !ok {
		name := g.text
		if len(p) > 1 && !p[1].dynamic && !p[1].glob {
			name += " " + p[1].text
		}
		return BinaryName + " " + name + " is not on the read-only allowlist"
	}
	if subs == nil || len(p) < 2 {
		return ""
	}
	s := p[1]
	if s.dynamic || s.glob {
		return BinaryName + " " + g.text + " subcommand comes from an expansion"
	}
	for _, a := range subs {
		if a == s.text {
			return ""
		}
	}
	return BinaryName + " " + g.text + " " + s.text + " is not on the read-only allowlist"
}

// base is the program name of a path-like word.
func base(w string) string { return path.Base(w) }
