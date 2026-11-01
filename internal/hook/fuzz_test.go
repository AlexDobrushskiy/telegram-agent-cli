package hook

import "testing"

func FuzzCheckBash(f *testing.F) {
	for _, s := range []string{
		"telegram-agent-cli send text", "a <<EOF\nb\nEOF", `echo "$(x ${y} $((1+2)) $'\x41')"`,
		"bash -c 'a | b' && (c; d) || `e`", "x <(y) >(z) 2>&1 <<- E\n\tq\n\tE", "$", "\\", "'", "${", "$((", "<<",
	} {
		f.Add(s)
	}
	o := testOptions()
	f.Fuzz(func(t *testing.T, s string) {
		_ = CheckBash(s, o)
	})
}

// A payload never panics the evaluators (the hook would ask, hiding the bug).
func FuzzPayload(f *testing.F) {
	for _, s := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"ls","cmd":["a"]}}`,
		`{"tool_name":"mcp__x__exec","tool_input":"{\"code\":[1,{\"path\":\"~/.claude\"}]}","extra":[null]}`,
		`{"tool_name":"Edit","arguments":{"file_path":7}}`, `{"tool_name":"x","tool_input":"[1,"}`,
		`{"tool_name":"call_tool","tool_input":{"server":"telegram","name":"get_x"}}`,
	} {
		f.Add(s)
	}
	o := testOptions()
	f.Fuzz(func(t *testing.T, s string) {
		if in, ok := parseInput([]byte(s)); ok {
			_ = Evaluate(in, o)
			_ = EvaluateCodex(in, o)
		}
	})
}
