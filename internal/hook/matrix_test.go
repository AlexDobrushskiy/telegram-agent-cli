package hook

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"testing"
)

// matrixOptions is the e2e r2 tester's environment (home and bot renamed):
// the default data dir and one allow-listed dev bot. No script the payloads
// name exists.
func matrixOptions() Options {
	return Options{
		DataDirs:   []string{"/Users/u/.config/telegram-agent-cli"},
		UserHome:   "/Users/u",
		Cwd:        "/work",
		E2EAllowed: []string{"@my_dev_bot"},
		ReadFile:   func(string) ([]byte, error) { return nil, fs.ErrNotExist },
	}
}

type matrixCase struct {
	Group    string `json:"group"`
	Case     string `json:"case"`
	Stdin    string `json:"stdin"`
	Expected string `json:"expected"` // block, allow, observe
	Claude   struct {
		Class string `json:"class"`
	} `json:"claude"`
	Codex struct {
		Class string `json:"class"`
	} `json:"codex"`
}

// The e2e r2 tester's whole hook matrix (578 payloads, both hooks). Class N
// (phase 01 surface) and every case the tester expects blocked get a decision;
// class D (opaque execution whose text names nothing the guard knows, also
// silent on main) stays silent: docs/safety.md states that limit.
func TestE2ER2Matrix(t *testing.T) {
	f, err := os.Open("testdata/e2e-r2/matrix.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	o := matrixOptions()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var c matrixCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		n++
		for _, codex := range []bool{false, true} {
			class, block := c.Claude.Class, "ask"
			if codex {
				class, block = c.Codex.Class, "deny"
			}
			want := block
			switch {
			case c.Expected == "observe":
				continue
			case c.Expected == "allow", class == "D":
				want = ""
			}
			if got := hookCall(t, codex, o, c.Stdin); got != want {
				t.Errorf("%s %s codex=%v class %s: %q, want %q\n%s", c.Group, c.Case, codex, class, got, want, c.Stdin)
			}
		}
	}
	if err := sc.Err(); err != nil || n != 578 {
		t.Fatalf("read %d cases: %v", n, err)
	}
}
