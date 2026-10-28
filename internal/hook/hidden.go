package hook

import (
	"strings"
)

// Content the guard can't see (a pipe into a shell, $(…) as code, a script it
// can't read) only asks when it is decoded on the way in, or when the visible
// command links to the account: the binary, a TGAC_ name, the watcher
// variable, the data dir or an MTProto library. curl … | bash on its own is
// ordinary work, and the binary's write gate stays the main barrier.

type hiddenReason struct {
	reason  string
	decoded bool
}

func (s *scanner) hidden(reason string, decoded bool) {
	s.hiddenRs = append(s.hiddenRs, hiddenReason{reason, decoded})
}

// related reports whether the visible command text links to the account.
func (s *scanner) related(text string) bool {
	if mentionRe.MatchString(text) || strings.Contains(text, "TGAC_") || segmentRe.MatchString(text) ||
		rawLibRe.MatchString(text) {
		return true
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
