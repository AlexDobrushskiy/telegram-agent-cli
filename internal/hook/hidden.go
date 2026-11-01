package hook

import (
	"regexp"
	"strings"
)

// Content the guard can't see (a pipe into a shell, $(…) as code, eval or
// sh -c of an expansion, an expansion as the program, interpreter code from an
// expansion or decoded at run time, a script it can't read or that doesn't
// exist yet) only asks when it is decoded on the way in, or when the visible
// command links to the account: the binary, a TGAC_ name, the watcher
// variable, the data dir, a protected settings file or an MTProto library.
// curl … | bash on its own is ordinary work, and the binary's write gate
// stays the main barrier.

// settingsTextRe is a Claude Code settings file or a Codex hook config named in text.
var settingsTextRe = regexp.MustCompile(`(?i)\.claude[/\\]+(?:settings|settings\.local)\.json|managed-settings\.json|` +
	`\.codex[/\\]+(?:hooks\.json|config\.toml)`)

// settingsDirRes match the settings files of $CLAUDE_CONFIG_DIR and $CODEX_HOME.
func settingsDirRes(o Options) []*regexp.Regexp {
	var res []*regexp.Regexp
	add := func(dir string, names string) {
		if dir = strings.TrimRight(dir, "/"); dir != "" {
			res = append(res, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(dir)+`/+(?:`+names+`)`))
		}
	}
	add(o.ClaudeConfigDir, `settings\.json|settings\.local\.json`)
	add(o.CodexHome, `hooks\.json|config\.toml`)
	return res
}

// opaqueCodeRe runs code the guard can't read: exec/eval of something other
// than a string literal (exec(base64.b64decode(…)), eval(x), new Function(x)).
var opaqueCodeRe = regexp.MustCompile(`\b(?:exec|eval|execfile|Function)\s*\(\s*[^'"\s)]`)

type hiddenReason struct {
	reason  string
	decoded bool
}

func (s *scanner) hidden(reason string, decoded bool) {
	s.hiddenRs = append(s.hiddenRs, hiddenReason{reason, decoded})
}

const reasonMissing = "runs a script that doesn't exist yet (the guard can't see it)"

// opaque records execution the guard can't follow. In a script file it
// counts when the file mentions the binary; in the command, when the visible
// text links to the account (see related).
func (s *scanner) opaque(c ctx, reason string, decoded bool) {
	if c.file && !decoded {
		s.condAdd(c, reason)
		return
	}
	s.hidden(reason, decoded)
}

// related reports whether the visible command text links to the account or
// names a protected settings file (a word resolving to one sets s.linked).
func (s *scanner) related(text string) bool {
	if s.linked || mentionRe.MatchString(text) || strings.Contains(text, "TGAC_") || segmentRe.MatchString(text) ||
		rawLibRe.MatchString(text) || settingsTextRe.MatchString(text) {
		return true
	}
	for _, re := range settingsDirRes(s.o) {
		if re.MatchString(text) {
			return true
		}
	}
	for name := range s.env {
		if strings.Contains(text, name) {
			return true
		}
	}
	for _, re := range s.dirRes {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// decoder reports whether a command turns encoded or scrambled text back into
// something runnable: base64 -d, xxd -r, openssl enc -d, uudecode, rev, tr.
func decoder(words []token) bool {
	idx, _ := skipPrefix(words)
	if idx < 0 {
		return false
	}
	args := words[idx+1:]
	has := func(flags ...string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a.text == f {
					return true
				}
			}
		}
		return false
	}
	switch base(words[idx].text) {
	case "base64", "gbase64", "base32", "basenc":
		return has("-d", "-D", "--decode")
	case "xxd":
		return has("-r", "-revert", "-rp", "-pr")
	case "openssl":
		return has("-d", "-base64", "-a") || len(args) > 0 && (args[0].text == "base64" || args[0].text == "enc" && has("-d"))
	case "uudecode", "rev", "tr":
		return true
	}
	return false
}

// pipeDecodes reports whether a decoder feeds the pipe that ends at p.
func pipeDecodes(p *command) bool {
	for ; p != nil; p = p.prev {
		if decoder(p.words) {
			return true
		}
	}
	return false
}

// subsDecode reports whether a command substitution decodes content.
func (s *scanner) subsDecode(subs []*lexed) bool {
	for _, lx := range subs {
		cmds, err := parse(lx)
		if err != nil {
			continue
		}
		for _, c := range cmds {
			if decoder(c.words) || s.subsDecode(c.subs) {
				return true
			}
		}
	}
	return false
}
