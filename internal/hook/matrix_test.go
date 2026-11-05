package hook

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
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

// The e2e r2 tester's whole hook matrix (578 payloads, both hooks). Every case
// the tester expects blocked gets a decision; class D and the cases marked
// allow (opaque or code-mode execution: interpreter code, decoded payloads, a
// tool's code field) stay silent, since the binary's own write gate covers
// them (docs/safety.md, Limits).
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

// Everyday calls in the envelope interactive Claude Code 2.1.307 sends (its
// scratchpad_dir and cwd name a telegram-agent-cli checkout) get main's
// decision on both hooks. Main asks on e2e commands only because it has no
// e2e: those pass (the exact allow-listed send of fix r3 included).
func TestEverydayCorpusMatchesMain(t *testing.T) {
	b, err := os.ReadFile("testdata/readonly-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus []struct{ Payload, Main string }
	if err := json.Unmarshal(b, &corpus); err != nil || len(corpus) < 60 {
		t.Fatalf("corpus: %d entries, %v", len(corpus), err)
	}
	o := matrixOptions()
	o.UserHome, o.DataDirs, o.Cwd = "/Users/u", []string{"/Users/u/.config/telegram-agent-cli"}, ""
	for _, c := range corpus {
		var in struct {
			ToolName  string         `json:"tool_name"`
			ToolInput map[string]any `json:"tool_input"`
		}
		_ = json.Unmarshal([]byte(c.Payload), &in)
		cmd, _ := in.ToolInput["command"].(string)
		want := c.Main
		if strings.HasPrefix(cmd, "telegram-agent-cli e2e ") {
			want = ""
		}
		for _, codex := range []bool{false, true} {
			w := want
			if codex && w == "ask" {
				w = "deny"
			}
			if got := hookCall(t, codex, o, c.Payload); got != w {
				t.Errorf("codex=%v %s %v: %q, want %q (main %q)", codex, in.ToolName, in.ToolInput, got, w, c.Main)
			}
		}
	}
}
