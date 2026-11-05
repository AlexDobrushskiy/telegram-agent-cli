package hook

import "strings"

// stdinShell checks what a shell reads on stdin: heredocs, here-strings, the
// literal text piped in by echo/printf/cat, and redirected script files.
func (s *scanner) stdinShell(cmd *command, c ctx) {
	s.stdinText(cmd, c)
	s.stdinFiles(cmd, c)
}

// stdinText checks heredocs and here-strings fed to a shell.
func (s *scanner) stdinText(cmd *command, c ctx) {
	if cmd == nil {
		return
	}
	for _, h := range cmd.heredocs {
		s.str(h.body, c)
	}
	for _, hs := range cmd.herestrings {
		s.str(hs.text, c)
	}
}

// stdinFiles checks a pipe or redirected script fed to a shell.
func (s *scanner) stdinFiles(cmd *command, c ctx) {
	if cmd == nil {
		return
	}
	if len(cmd.heredocs) == 0 && len(cmd.herestrings) == 0 && cmd.pipeIn {
		s.pipedIn(cmd, c)
	}
	for _, r := range cmd.inRedirs {
		if r.text != "-" && !allDigits(r.text) && !r.dynamic && !r.glob {
			s.file(r.text, modeShell, c)
		}
	}
}

// pipedIn checks the literal text of echo/printf and the heredoc or files of
// cat piped into a shell. Other pipes are not visible and pass.
func (s *scanner) pipedIn(cmd *command, c ctx) {
	p := cmd.prev
	if p == nil {
		return
	}
	idx, _ := skipPrefix(p.words)
	if idx < 0 || len(p.subs) > 0 {
		return
	}
	args := p.words[idx+1:]
	literal := true
	for _, a := range args {
		literal = literal && !a.dynamic
	}
	switch base(p.words[idx].text) {
	case "echo", "printf":
		if literal {
			for len(args) > 0 && strings.HasPrefix(args[0].text, "-") {
				args = args[1:]
			}
			s.str(joinWords(args), c)
		}
	case "cat":
		for _, h := range p.heredocs {
			s.str(h.body, c)
		}
		for _, h := range p.herestrings {
			s.str(h.text, c)
		}
		if literal && s.remote == 0 {
			for _, a := range args {
				s.file(a.text, modeShell, c)
			}
		}
	}
}
