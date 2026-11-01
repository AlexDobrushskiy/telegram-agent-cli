package cli

import (
	"io/fs"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/e2e"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/hook"
)

// e2eWriteCommands are the e2e commands that write to the bot chat.
var e2eWriteCommands = map[string]bool{
	"e2e send text": true, "e2e send photo": true, "e2e send voice": true, "e2e send file": true,
	"e2e send location": true, "e2e react": true, "e2e press": true, "e2e web-app-data": true,
	"e2e webapp-url": true, "e2e stop": true,
}

// When the guard hook stays silent on a command line, the command cobra really
// runs is a read, or an e2e write to an allow-listed bot. Generated argv mix
// words, subcommands and flags the hook could misread.
func TestHookSilentMeansReadOrAllowListedE2E(t *testing.T) {
	h := newHarness(t)
	o := hook.Options{
		DataDirs: []string{"/d/tg"}, UserHome: "/h", Cwd: "/w", E2EAllowed: []string{"@my_test_bot"},
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
	}
	tokens := []string{
		"me", "e2e", "send", "messages", "hi", "1", "@my_test_bot", "@other_bot",
		"--all", "--remove", "--keyboard", "--menu", "--main", "--chat", "--chat=@other_bot",
		"--chat=@my_test_bot", "--json", "--limit", "--msg", "--emoji", "--button", "--", "--dry-run",
	}
	var paths [][]string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		for _, s := range c.Commands() {
			p := append(append([]string{}, path...), s.Name())
			if s.HasSubCommands() {
				walk(s, p)
			} else {
				paths = append(paths, p)
			}
		}
	}
	walk(h.app.Root(), nil)
	pick := func(r *rand.Rand, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = tokens[r.IntN(len(tokens))]
		}
		return out
	}
	var lines [][]string
	// every flag + word before or inside every path, aimed at another bot
	for _, f := range tokens {
		for _, w := range tokens {
			if !strings.HasPrefix(f, "-") || strings.HasPrefix(w, "-") {
				continue
			}
			for _, p := range paths {
				lines = append(lines, append(append([]string{f, w}, p...), "--chat", "@other_bot"))
				lines = append(lines, append(append(append([]string{p[0]}, f, w), p[1:]...), "--chat=@other_bot"))
			}
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 30000 {
		var args []string
		args = append(args, pick(r, r.IntN(3))...)
		for _, w := range paths[r.IntN(len(paths))] {
			args = append(args, w)
			if r.IntN(6) == 0 {
				args = append(args, pick(r, 1)...)
			}
		}
		lines = append(lines, append(args, pick(r, r.IntN(5))...))
	}
	checked := 0
	for _, args := range lines {
		line := hook.BinaryName + " " + strings.Join(args, " ")
		if len(hook.CheckBash(line, o)) > 0 {
			continue // asks
		}
		root := h.app.Root()
		c, rest, err := root.Find(args)
		if err != nil || c.ParseFlags(rest) != nil || c.ValidateArgs(c.Flags().Args()) != nil {
			continue // cobra refuses it: nothing runs
		}
		path := strings.TrimPrefix(c.CommandPath(), root.Name()+" ")
		if c == root || c.HasSubCommands() {
			continue // prints help
		}
		checked++
		if e2eWriteCommands[path] {
			chat, _ := c.Flags().GetString("chat")
			if chat != "" && !e2e.SameRef(chat, "@my_test_bot") {
				t.Errorf("%q: silent, runs %s to %s", line, path, chat)
			}
			continue
		}
		if !isRead(path, c) {
			t.Errorf("%q: silent, runs the write %s", line, path)
		}
	}
	if checked < 500 {
		t.Fatalf("only %d silent runnable lines; the generator needs work", checked)
	}
}
