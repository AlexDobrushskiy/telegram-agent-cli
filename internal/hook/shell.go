package hook

import (
	"errors"
	"strings"
)

// A small POSIX-shell lexer/parser: enough to find the programs a command line
// runs (quotes, escapes, operators, redirects, heredocs, $(…), `…`, <(…)),
// not a full shell grammar.

type tokKind int

const (
	tWord  tokKind = iota
	tOp            // ; & && || | |& ( ) ;; newline
	tRedir         // < > >> << <<- <<< >& <& &> &>> >| <>
)

type token struct {
	kind    tokKind
	text    string   // word value with quotes removed, or the operator
	quoted  bool     // some part of the word was quoted or escaped
	dynamic bool     // contains an expansion or substitution
	glob    bool     // unquoted glob or brace chars
	subs    []*lexed // command/process substitutions inside the word
	doc     *heredoc // for << and <<-: the attached body
}

type heredoc struct {
	delim  string
	strip  bool // <<- strips leading tabs
	quoted bool // quoted delimiter: no expansion in the body
	body   string
	start  int // absolute offset of the body, -1 if not verbatim
	end    int
	subs   []*lexed // substitutions in an unquoted body
}

// lexed is a lexed script: its tokens and where it sits in the top-level text.
type lexed struct {
	toks []token
	base int // absolute offset of src[0], -1 if src is not a substring of the top level
}

var errUnterminated = errors.New("unterminated quote or substitution")

type lexer struct {
	src     string
	i       int
	base    int
	toks    []token
	pending []*heredoc
	depth   int              // nesting of substitutions
	sb      *strings.Builder // receives double-quoted text while set
}

const maxLexDepth = 16

var operators = []string{
	"<<<", "<<-", "&>>", ";;&",
	"<<", ">>", "<&", ">&", "<>", ">|", "&>", "&&", "||", ";;", ";&", "|&",
	";", "&", "|", "(", ")", "<", ">",
}

var redirs = map[string]bool{
	"<": true, ">": true, ">>": true, "<<": true, "<<-": true, "<<<": true,
	">&": true, "<&": true, "&>": true, "&>>": true, ">|": true, "<>": true,
}

func isOpChar(c byte) bool { return strings.IndexByte(";&|()<>", c) >= 0 }
func isBlank(c byte) bool  { return c == ' ' || c == '\t' || c == '\r' }
func isNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// lex tokenizes src. base is src's offset in the top-level text (-1 if unknown).
func lex(src string, base int) (*lexed, error) {
	l := &lexer{src: src, base: base}
	if _, err := l.run(false); err != nil {
		return nil, err
	}
	return &lexed{toks: l.toks, base: base}, nil
}

func (l *lexer) abs(i int) int {
	if l.base < 0 {
		return -1
	}
	return l.base + i
}

// run lexes until EOF, or until an unmatched ")" when inParen (returns its index).
func (l *lexer) run(inParen bool) (int, error) {
	parens := 0
	wantDelim := -1 // index of a << token waiting for its delimiter word
	for {
		for l.i < len(l.src) && isBlank(l.src[l.i]) {
			l.i++
		}
		if l.i >= len(l.src) {
			if inParen {
				return 0, errUnterminated
			}
			l.readHeredocs()
			return l.i, nil
		}
		c := l.src[l.i]
		switch {
		case c == '\\' && l.i+1 < len(l.src) && l.src[l.i+1] == '\n':
			l.i += 2
			continue
		case c == '\n':
			l.i++
			l.toks = append(l.toks, token{kind: tOp, text: "\n"})
			l.readHeredocs()
			continue
		case c == '#':
			for l.i < len(l.src) && l.src[l.i] != '\n' {
				l.i++
			}
			continue
		case (c == '<' || c == '>') && l.i+1 < len(l.src) && l.src[l.i+1] == '(':
			// process substitution: a word
		case isOpChar(c):
			op := ""
			for _, o := range operators {
				if strings.HasPrefix(l.src[l.i:], o) {
					op = o
					break
				}
			}
			if inParen && op == ")" && parens == 0 {
				end := l.i
				l.i++
				return end, nil
			}
			switch op {
			case "(":
				parens++
			case ")":
				parens--
			}
			l.i += len(op)
			kind := tOp
			if redirs[op] {
				kind = tRedir
			}
			l.toks = append(l.toks, token{kind: kind, text: op})
			if op == "<<" || op == "<<-" {
				wantDelim = len(l.toks) - 1
			}
			continue
		}
		start := l.i
		w, err := l.word()
		if err != nil {
			return 0, err
		}
		// "2>&1": a bare fd number glued to a redirect is not a word.
		if !w.quoted && l.i < len(l.src) && (l.src[l.i] == '<' || l.src[l.i] == '>') && allDigits(l.src[start:l.i]) {
			continue
		}
		l.toks = append(l.toks, w)
		if wantDelim >= 0 {
			op := l.toks[wantDelim].text
			h := &heredoc{delim: w.text, strip: op == "<<-", quoted: w.quoted}
			l.toks[wantDelim].doc = h
			l.pending = append(l.pending, h)
			wantDelim = -1
		}
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readHeredocs consumes the bodies of heredocs started on the line just ended.
func (l *lexer) readHeredocs() {
	for _, h := range l.pending {
		start := l.i
		h.start = l.abs(start)
		for {
			if l.i >= len(l.src) {
				h.body = l.src[start:]
				h.end = l.abs(len(l.src))
				break
			}
			j := strings.IndexByte(l.src[l.i:], '\n')
			lineEnd := len(l.src)
			if j >= 0 {
				lineEnd = l.i + j
			}
			line := l.src[l.i:lineEnd]
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				h.body = l.src[start:l.i]
				h.end = l.abs(l.i)
				l.i = lineEnd
				if l.i < len(l.src) {
					l.i++
				}
				break
			}
			l.i = lineEnd
			if l.i < len(l.src) {
				l.i++
			}
		}
		if !h.quoted {
			base := -1
			if h.start >= 0 {
				base = h.start
			}
			sub := &lexer{src: h.body, base: base, depth: l.depth + 1}
			var tok token
			// Errors in a body are not fatal: bash would just print the text.
			_ = sub.dquote(&tok, false)
			h.subs = tok.subs
		}
	}
	l.pending = nil
}

// word lexes one shell word starting at l.i.
func (l *lexer) word() (token, error) {
	var b strings.Builder
	tok := token{kind: tWord}
	braces := false
	for l.i < len(l.src) {
		c := l.src[l.i]
		if isBlank(c) || c == '\n' {
			break
		}
		procSub := (c == '<' || c == '>') && l.i+1 < len(l.src) && l.src[l.i+1] == '(' && b.Len() == 0
		if isOpChar(c) && !procSub {
			break
		}
		switch c {
		case '\\':
			if l.i+1 < len(l.src) {
				if l.src[l.i+1] != '\n' {
					b.WriteByte(l.src[l.i+1])
					tok.quoted = true
				}
				l.i += 2
			} else {
				b.WriteByte(c)
				l.i++
			}
		case '\'':
			j := strings.IndexByte(l.src[l.i+1:], '\'')
			if j < 0 {
				return tok, errUnterminated
			}
			b.WriteString(l.src[l.i+1 : l.i+1+j])
			tok.quoted = true
			l.i += j + 2
		case '"':
			l.i++
			s, err := l.dquoteText(&tok)
			if err != nil {
				return tok, err
			}
			b.WriteString(s)
			tok.quoted = true
		case '`':
			if err := l.backtick(&tok); err != nil {
				return tok, err
			}
			b.WriteString("`…`")
		case '$':
			s, err := l.dollar(&tok, true)
			if err != nil {
				return tok, err
			}
			b.WriteString(s)
		case '<', '>':
			// process substitution at word start
			l.i++
			if err := l.subst(&tok); err != nil {
				return tok, err
			}
			b.WriteString("<(…)")
		default:
			if c == '*' || c == '?' || c == '[' {
				tok.glob = true
			}
			if c == '{' || c == ',' {
				braces = braces || c == '{'
				if braces && c == ',' {
					tok.glob = true
				}
			}
			b.WriteByte(c)
			l.i++
		}
	}
	tok.text = b.String()
	return tok, nil
}

// dquoteText lexes the rest of a "…" string (l.i is past the opening quote).
func (l *lexer) dquoteText(tok *token) (string, error) {
	var b strings.Builder
	l.sb = &b
	err := l.dquote(tok, true)
	l.sb = nil
	return b.String(), err
}

// dquote walks double-quoted text (or an unquoted heredoc body when !closing),
// collecting substitutions. Text goes to l.sb when set.
func (l *lexer) dquote(tok *token, closing bool) error {
	write := func(s string) {
		if l.sb != nil {
			l.sb.WriteString(s)
		}
	}
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch c {
		case '"':
			if closing {
				l.i++
				return nil
			}
			write(`"`)
			l.i++
		case '\\':
			if l.i+1 < len(l.src) && strings.IndexByte("$`\"\\\n", l.src[l.i+1]) >= 0 {
				if l.src[l.i+1] != '\n' {
					write(l.src[l.i+1 : l.i+2])
				}
				l.i += 2
			} else {
				write(`\`)
				l.i++
			}
		case '`':
			sb := l.sb
			l.sb = nil
			err := l.backtick(tok)
			l.sb = sb
			if err != nil {
				return err
			}
			write("`…`")
		case '$':
			sb := l.sb
			l.sb = nil
			s, err := l.dollar(tok, false)
			l.sb = sb
			if err != nil {
				return err
			}
			write(s)
		default:
			write(l.src[l.i : l.i+1])
			l.i++
		}
	}
	if closing {
		return errUnterminated
	}
	return nil
}

// dollar lexes $… at l.i and returns the text to keep in the word.
func (l *lexer) dollar(tok *token, unquoted bool) (string, error) {
	rest := l.src[l.i:]
	switch {
	case strings.HasPrefix(rest, "$(("):
		end := matchParens(l.src, l.i+1)
		if end < 0 {
			return "", errUnterminated
		}
		tok.dynamic = true
		s := l.src[l.i:end]
		l.i = end
		return s, nil
	case strings.HasPrefix(rest, "$("):
		l.i++
		if err := l.subst(tok); err != nil {
			return "", err
		}
		return "$(…)", nil
	case strings.HasPrefix(rest, "${"):
		end := matchBraces(l.src, l.i+1)
		if end < 0 {
			return "", errUnterminated
		}
		tok.dynamic = true
		s := l.src[l.i:end]
		l.i = end
		return s, nil
	case unquoted && strings.HasPrefix(rest, "$'"):
		// ANSI-C quoting: escapes can spell any name, so treat as dynamic when used.
		var b strings.Builder
		j := l.i + 2
		for j < len(l.src) && l.src[j] != '\'' {
			if l.src[j] == '\\' && j+1 < len(l.src) {
				tok.dynamic = true
				b.WriteByte(l.src[j+1])
				j += 2
				continue
			}
			b.WriteByte(l.src[j])
			j++
		}
		if j >= len(l.src) {
			return "", errUnterminated
		}
		tok.quoted = true
		l.i = j + 1
		return b.String(), nil
	case len(rest) > 1 && (isNameChar(rest[1]) || strings.IndexByte("@*#?$!-", rest[1]) >= 0):
		j := 2
		if isNameChar(rest[1]) && (rest[1] < '0' || rest[1] > '9') {
			for j < len(rest) && isNameChar(rest[j]) {
				j++
			}
		}
		tok.dynamic = true
		l.i += j
		return rest[:j], nil
	}
	l.i++
	return "$", nil
}

// subst lexes a (…) substitution body; l.i is at the "(".
func (l *lexer) subst(tok *token) error {
	if l.depth >= maxLexDepth {
		return errUnterminated
	}
	sub := &lexer{src: l.src, i: l.i + 1, base: l.base, depth: l.depth + 1}
	if _, err := sub.run(true); err != nil {
		return err
	}
	tok.subs = append(tok.subs, &lexed{toks: sub.toks, base: l.base})
	tok.dynamic = true
	l.i = sub.i
	return nil
}

// backtick lexes a `…` substitution; l.i is at the opening backtick.
func (l *lexer) backtick(tok *token) error {
	var b strings.Builder
	j := l.i + 1
	for ; j < len(l.src) && l.src[j] != '`'; j++ {
		if l.src[j] == '\\' && j+1 < len(l.src) && strings.IndexByte("$`\\", l.src[j+1]) >= 0 {
			j++
		}
		b.WriteByte(l.src[j])
	}
	if j >= len(l.src) || l.depth >= maxLexDepth {
		return errUnterminated
	}
	sub := &lexer{src: b.String(), base: -1, depth: l.depth + 1}
	if _, err := sub.run(false); err != nil {
		return err
	}
	tok.subs = append(tok.subs, &lexed{toks: sub.toks, base: -1})
	tok.dynamic = true
	l.i = j + 1
	return nil
}

// matchParens returns the index after the ")" matching the "(" at i.
func matchParens(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// matchBraces returns the index after the "}" matching the "{" at i.
func matchBraces(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// command is one simple command of a pipeline/list.
type command struct {
	words       []token
	heredocs    []*heredoc
	herestrings []token
	outFiles    []string // > / >> targets
	pipeIn      bool
	pipeOut     bool
	subs        []*lexed // every substitution in the command, any position
}

// parse splits lexed tokens into simple commands.
func parse(lx *lexed) ([]*command, error) {
	var cmds []*command
	cur := &command{}
	flush := func(pipe bool) {
		if len(cur.words) > 0 || len(cur.heredocs) > 0 || len(cur.herestrings) > 0 || len(cur.subs) > 0 {
			cur.pipeOut = pipe
			cmds = append(cmds, cur)
		}
		cur = &command{pipeIn: pipe}
	}
	toks := lx.toks
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.kind {
		case tOp:
			flush(t.text == "|" || t.text == "|&")
		case tRedir:
			if i+1 >= len(toks) || toks[i+1].kind != tWord {
				return nil, errors.New("redirect without a target")
			}
			target := toks[i+1]
			i++
			cur.subs = append(cur.subs, target.subs...)
			switch t.text {
			case "<<", "<<-":
				if t.doc != nil {
					cur.heredocs = append(cur.heredocs, t.doc)
					cur.subs = append(cur.subs, t.doc.subs...)
				}
			case "<<<":
				cur.herestrings = append(cur.herestrings, target)
			case ">", ">>", ">|", "&>", "&>>":
				cur.outFiles = append(cur.outFiles, target.text)
			}
		default:
			cur.words = append(cur.words, t)
			cur.subs = append(cur.subs, t.subs...)
		}
	}
	flush(false)
	return cmds, nil
}
