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
