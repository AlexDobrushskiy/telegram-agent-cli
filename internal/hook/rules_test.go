package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testHome = "/xhome/u"

var testDir = testHome + "/.config/telegram-agent-cli"

var testFiles = map[string]string{
	"/work/scripts/ok.sh":   "#!/bin/sh\ntelegram-agent-cli me\n",
	"/work/scripts/bad.sh":  "#!/bin/sh\ntelegram-agent-cli send text --chat 1 --message hi\n",
	"/work/scripts/bad":     "#!/usr/bin/env bash\nset -e\ntelegram-agent-cli auth logout\n",
	"/work/scripts/read.py": "import subprocess\nsubprocess.run(['telegram-agent-cli', 'me'])\n",
	"/work/scripts/raw.py":  "from telethon.sync import TelegramClient\nTelegramClient('x.session', 1, 'h')\n",
	"/work/scripts/home.py": "import os\nprint(os.path.expanduser('~/.config/telegram-agent-cli/session.json'))\n",
	"/work/scripts/bin.sh":  "telegram-agent-cli send\x00 text",
}

func testOptions() Options {
	return Options{
		DataDirs: []string{testDir, "/data/tg"},
		UserHome: testHome,
		Cwd:      "/work",
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := testFiles[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("no such file")
		},
	}
}

func TestCheckBashAllows(t *testing.T) {
	cases := []string{
		// read allowlist
		"telegram-agent-cli chats list --limit 30",
		"telegram-agent-cli chats list --unread --type group",
		"telegram-agent-cli chats show --chat @someone",
		"telegram-agent-cli messages list --chat 123 --limit 50 --asc",
		`telegram-agent-cli messages search "hello world" --limit 10`,
		"telegram-agent-cli messages show --chat 1 --id 5,6",
		"telegram-agent-cli messages context --chat 1 --id 5 --around 8",
		"telegram-agent-cli messages mentions --unread",
		"telegram-agent-cli messages wait-new --state /tmp/s.json --timeout 600 --dms",
		`telegram-agent-cli contacts search "Ann"`,
		"telegram-agent-cli media download --chat 1 --id 2 --output /tmp/m",
		"telegram-agent-cli auth status",
		"telegram-agent-cli me",
		"telegram-agent-cli doctor",
		"telegram-agent-cli version",
		"telegram-agent-cli --json me",
		"telegram-agent-cli watch reply --watcher w --chat 1 --text hi --dry-run",
		"telegram-agent-cli watch list",
		"telegram-agent-cli watch check --watcher w",
		"echo '{}' | telegram-agent-cli hook claude-pretooluse",
		"telegram-agent-cli messages list --chat send",
		// help
		"telegram-agent-cli",
		"telegram-agent-cli --help",
		"telegram-agent-cli send --help",
		"telegram-agent-cli send text -h",
		"telegram-agent-cli help send",
		"telegram-agent-cli auth",
		// paths, wrappers, operators
		"./bin/telegram-agent-cli chats list",
		"~/go/bin/telegram-agent-cli me",
		"TGAC_READONLY=1 telegram-agent-cli me",
		"TGAC_READONLY=true telegram-agent-cli me",
		"telegram-agent-cli chats list 2>&1 | jq .",
		"telegram-agent-cli me 2>/dev/null",
		"timeout 30 telegram-agent-cli messages wait-new --state s.json",
		"sudo -u bob telegram-agent-cli me",
		"env -u FOO telegram-agent-cli me",
		"xargs -n1 telegram-agent-cli messages show --chat 1 --id",
		`echo "it's fine" && telegram-agent-cli me`,
		"ssh box telegram-agent-cli chats list",
		"ssh -p 2222 box 'telegram-agent-cli messages list --chat 1'",
		`bash -lc "telegram-agent-cli doctor"`,
		`sh -c 'telegram-agent-cli me && telegram-agent-cli chats list'`,
		"bash scripts/ok.sh",
		"python3 scripts/read.py",
		"go run ./cmd/telegram-agent-cli chats list",
		// the name as data, not a program
		`git commit -m "telegram-agent-cli send text --chat 1"`,
		"git switch -c feat/telegram-agent-cli",
		"git checkout -b feat/telegram-agent-cli",
		"grep telegram-agent-cli README.md",
		"grep -rn 'telegram-agent-cli send' docs/",
		"ls bin/telegram-agent-cli",
		"which telegram-agent-cli",
		"command -v telegram-agent-cli",
		`claude -p "use telegram-agent-cli send text to reply to the last message"`,
		`echo "run telegram-agent-cli auth logout later"`,
		"go build -o bin/telegram-agent-cli ./cmd/telegram-agent-cli",
		"go install github.com/AlexDobrushskiy/telegram-agent-cli/cmd/telegram-agent-cli@latest",
		"go test ./...",
		"cd ~/src/telegram-agent-cli && make test",
		"git commit -F - <<'EOF'\nAdd telegram-agent-cli send docs\n\nmentions ~/.config/telegram-agent-cli and TGAC_HOME\nEOF",
		"git commit -m \"$(cat <<'EOF'\nfeat: telegram-agent-cli send text\n\nTGAC_HOME and TGAC_READONLY=0 docs\nEOF\n)\"",
		"cat > README.md <<'EOF'\ntelegram-agent-cli send text --chat 1\nTGAC_READONLY=0 telegram-agent-cli send\nEOF",
		"cat > README.md <<EOF\ntelegram-agent-cli send text --chat 1\nEOF",
		"# telegram-agent-cli send text\nls",
		// message text and search patterns are data
		`git commit -m "docs: TGAC_HOME, TGAC_READONLY=0 and ~/.config/telegram-agent-cli"`,
		`git commit --message="mentions gotd and session.json"`,
		`gh pr create --title "TGAC_READONLY=0 docs" --body "see ~/.config/telegram-agent-cli/session.json"`,
		`grep -rn "TGAC_READONLY=0" docs/`,
		`rg -g '*.md' -e "~/.config/telegram-agent-cli" .`,
		`git -C repo grep "TGAC_READONLY=false"`,
		`grep -- "TGAC_READONLY=0" README.md`,
		// a mention of TGAC_HOME, and longer names next to the data dir
		"grep -r TGAC_HOME .",
		"echo see TGAC_HOME in the docs",
		"cat ~/.config/telegram-agent-cli-notes.md",
		"ls /xhome/u/.config/telegram-agent-cli.old",
		// unrelated
		"ls ~/.config",
		"grep -r TGAC_READONLY .",
		"unset TGAC_READONLY",
		"echo $HOME",
		"vim notes.txt",
		`echo "unterminated`,
		"$EDITOR notes.txt",
		"go get github.com/gotd/td",
		"bash scripts/missing.sh",
		"bash scripts/bin.sh",
	}
	o := testOptions()
	for _, c := range cases {
		if r := CheckBash(c, o); len(r) > 0 {
			t.Errorf("CheckBash(%q) = %v, want allow", c, r)
		}
	}
}

func TestCheckBashAsks(t *testing.T) {
	cases := map[string]string{
		// off the allowlist
		"telegram-agent-cli send text --chat 1 --message hi":            "send text",
		"./bin/telegram-agent-cli send text --chat 1 --message hi":      "send text",
		"/usr/local/bin/telegram-agent-cli send file --chat 1 --path x": "send file",
		"telegram-agent-cli auth logout":                                "auth logout",
		"telegram-agent-cli auth qr":                                    "auth qr",
		"telegram-agent-cli auth init --api-id 1 --phone +100":          "auth init",
		"telegram-agent-cli auth request-code":                          "auth request-code",
		"telegram-agent-cli auth verify --code 1":                       "auth verify",
		"telegram-agent-cli auth import-telethon --session x":           "auth import-telethon",
		"telegram-agent-cli hook install":                               "hook install",
		"telegram-agent-cli hook uninstall --settings s.json":           "hook uninstall",
		"telegram-agent-cli send":                                       "send is not",
		"telegram-agent-cli foo":                                        "foo is not",
		"telegram-agent-cli chats delete":                               "chats delete",
		"telegram-agent-cli watch register x":                           "watch register",
		"telegram-agent-cli 'send' text":                                "send text",
		`tele"gram-agent-cli" send text`:                                "send text",
		"telegram-agent-cli --json send text":                           "send text",
		"telegram-agent-cli send text -- --help":                        "send text",
		"telegram-agent-cli messages --chat 1 delete":                   "not on the read-only allowlist",
		"telegram-agent-cli $CMD":                                       "expansion",
		"telegram-agent-cli chats $SUB":                                 "expansion",
		"telegram-agent-cli s*":                                         "expansion",
		// TGAC_READONLY overrides
		"TGAC_READONLY=0 telegram-agent-cli me":                                 "TGAC_READONLY",
		"TGAC_READONLY=false telegram-agent-cli send text":                      "TGAC_READONLY",
		"TGAC_READONLY=OFF telegram-agent-cli me":                               "TGAC_READONLY",
		"TGAC_READONLY=no telegram-agent-cli me":                                "TGAC_READONLY",
		"TGAC_READONLY= telegram-agent-cli me":                                  "TGAC_READONLY",
		`TGAC_READONLY="" telegram-agent-cli me`:                                "TGAC_READONLY",
		"TGAC_READONLY='' telegram-agent-cli me":                                "TGAC_READONLY",
		"TGAC_READONLY=$X telegram-agent-cli me":                                "TGAC_READONLY",
		"export TGAC_READONLY=0":                                                "TGAC_READONLY",
		"env TGAC_READONLY=0 python3 x.py":                                      "TGAC_READONLY",
		`python3 -c "import os; os.environ['TGAC_READONLY']='0'"`:               "TGAC_READONLY",
		`python3 -c "import os; os.environ.setdefault('TGAC_READONLY', 'off')"`: "TGAC_READONLY",
		"TGAC_READONLY=0\ntelegram-agent-cli send text":                         "TGAC_READONLY",
		// data dir and TGAC_HOME
		"TGAC_HOME=/tmp/x telegram-agent-cli me":                            "TGAC_HOME",
		"echo $TGAC_HOME":                                                   "TGAC_HOME",
		"cat ~/.config/telegram-agent-cli/session.json":                     "data dir",
		"cat /xhome/u/.config/telegram-agent-cli/config.json":               "data dir",
		"cp -r $HOME/.config/telegram-agent-cli /tmp/x":                     "data dir",
		`ls "${HOME}/.config/telegram-agent-cli"`:                           "data dir",
		"cd ~ && tar czf x.tgz .config/telegram-agent-cli":                  "data dir",
		"ls $XDG_CONFIG_HOME/telegram-agent-cli":                            "data dir",
		"cat /data/tg/session.json":                                         "data dir",
		"cat ~/.Config/Telegram-Agent-CLI/session.json":                     "data dir",
		"echo hi > ~/.config/telegram-agent-cli/watchers.json":              "data dir",
		"cat > ~/.config/telegram-agent-cli/watchers.json <<'EOF'\n{}\nEOF": "data dir",
		"python3 scripts/home.py":                                           "data dir",
		// raw MTProto
		`python3 -c "from telethon import TelegramClient; TelegramClient('x', 1, 'h')"`:                             "raw MTProto",
		"python3 - <<'EOF'\nfrom telethon.sync import TelegramClient\nc = TelegramClient('a.session', 1, 'h')\nEOF": "raw MTProto",
		"cat > x.go <<'EOF'\nimport \"github.com/gotd/td/telegram\"\n// load session.json\nEOF\ngo run x.go":        "raw MTProto",
		"python3 scripts/raw.py": "raw MTProto",
		// wrappers
		"sudo telegram-agent-cli send text":                              "send text",
		"sudo -u bob telegram-agent-cli send text":                       "send text",
		"env A=1 telegram-agent-cli send text":                           "send text",
		"env -u FOO telegram-agent-cli send text":                        "send text",
		`env -S "telegram-agent-cli send text"`:                          "send text",
		"timeout 10 telegram-agent-cli send text":                        "send text",
		"timeout -s KILL 10 telegram-agent-cli send text":                "send text",
		"nohup telegram-agent-cli send text &":                           "send text",
		"nice -n 5 telegram-agent-cli send text":                         "send text",
		"xargs -I{} telegram-agent-cli send text --chat {} --message hi": "send text",
		"uv run telegram-agent-cli send text":                            "send text",
		"exec telegram-agent-cli send text":                              "send text",
		"command telegram-agent-cli send text":                           "send text",
		`flock /tmp/l -c "telegram-agent-cli send text"`:                 "send text",
		// nesting
		`sh -c "telegram-agent-cli send text"`:                            "send text",
		"bash -lc 'echo hi && telegram-agent-cli send text'":              "send text",
		`bash -c "bash -c 'telegram-agent-cli send text'"`:                "send text",
		"eval telegram-agent-cli send text":                               "send text",
		`eval "telegram-agent-cli send text"`:                             "send text",
		"ssh box 'telegram-agent-cli send text --chat 1'":                 "send text",
		"ssh box telegram-agent-cli send text":                            "send text",
		"ssh -p 22 -i key box telegram-agent-cli auth logout":             "auth logout",
		"ssh box <<'EOF'\ntelegram-agent-cli send text\nEOF":              "send text",
		"bash <<'EOF'\ntelegram-agent-cli send text\nEOF":                 "send text",
		`bash <<< "telegram-agent-cli send text"`:                         "send text",
		`echo "telegram-agent-cli send text" | bash`:                      "pipes a script",
		"su -c 'telegram-agent-cli send text' bob":                        "send text",
		"find . -name x -exec telegram-agent-cli send file --path {} \\;": "send file",
		// operators and substitutions
		"echo foo | telegram-agent-cli send text":                          "send text",
		"ls && telegram-agent-cli send text":                               "send text",
		"ls; telegram-agent-cli send text":                                 "send text",
		"ls || telegram-agent-cli send text":                               "send text",
		"(telegram-agent-cli send text)":                                   "send text",
		"{ telegram-agent-cli send text; }":                                "send text",
		"! telegram-agent-cli send text":                                   "send text",
		"if true; then telegram-agent-cli send text; fi":                   "send text",
		"for i in 1 2; do telegram-agent-cli send text; done":              "send text",
		"while true; do telegram-agent-cli send text; done":                "send text",
		"echo $(telegram-agent-cli send text)":                             "send text",
		`echo "$(telegram-agent-cli send text)"`:                           "send text",
		"echo `telegram-agent-cli send text`":                              "send text",
		"diff <(telegram-agent-cli send text) x":                           "send text",
		"cat <<EOF\n$(telegram-agent-cli send text)\nEOF":                  "send text",
		"telegram-agent-cli me\ntelegram-agent-cli send text":              "send text",
		"telegram-agent-cli me && \\\n  telegram-agent-cli send text":      "send text",
		"telegram-agent-cli me > out.json; telegram-agent-cli auth logout": "auth logout",
		"TELEGRAM-AGENT-CLI send text":                                     "send text",
		// go run
		"go run ./cmd/telegram-agent-cli send text":                                                    "send text",
		"go run -tags e2e ./cmd/telegram-agent-cli/ auth logout":                                       "auth logout",
		"go run github.com/AlexDobrushskiy/telegram-agent-cli/cmd/telegram-agent-cli@latest send text": "send text",
		// indirection
		"BIN=./bin/telegram-agent-cli; $BIN send text": "expansion",
		`"$(which telegram-agent-cli)" send text`:      "expansion",
		"alias t='telegram-agent-cli send text'":       "alias",
		"bash scripts/bad.sh":                          "send text",
		"./scripts/bad":                                "auth logout",
		"source scripts/bad.sh":                        "send text",
		". scripts/bad.sh":                             "send text",
		"cat > /tmp/r.sh <<'EOF'\ntelegram-agent-cli send text\nEOF\nbash /tmp/r.sh": "send text",
		"cat <<'EOF' | bash\ntelegram-agent-cli send text\nEOF":                      "pipes a script",
		// unparseable
		`telegram-agent-cli me "unterminated`: "could not parse",
	}
	o := testOptions()
	for c, want := range cases {
		r := CheckBash(c, o)
		if len(r) == 0 {
			t.Errorf("CheckBash(%q) allowed, want ask (%s)", c, want)
			continue
		}
		if !strings.Contains(strings.Join(r, "; "), want) {
			t.Errorf("CheckBash(%q) = %v, want a reason containing %q", c, r, want)
		}
	}
}

func TestCheckFile(t *testing.T) {
	o := testOptions()
	o.Cwd = testHome
	cases := []struct {
		tool, path string
		ask        bool
	}{
		{"Read", testDir + "/session.json", true},
		{"Read", testDir + "/config.json", true},
		{"Read", testDir + "/peers.json", true},
		{"Read", testDir + "/pending-login.json", true},
		{"Read", "~/.config/telegram-agent-cli/config.json", true},
		{"Read", ".config/telegram-agent-cli/session.json", true},
		{"Read", testDir + "/../telegram-agent-cli/session.json", true},
		{"Read", "/data/tg/session.json", true},
		{"Read", testDir + "/watchers.json", false},
		{"Read", testDir + "/.lock", false},
		{"Read", testHome + "/notes/session.json", false},
		{"Read", "", false},
		{"Write", testDir + "/watchers.json", true},
		{"Write", testDir + "/PAUSE-ALL", true},
		{"Edit", testDir + "/config.json", true},
		{"MultiEdit", testDir + "/watchers.json", true},
		{"NotebookEdit", testDir + "/x.ipynb", true},
		{"Write", testDir, true},
		{"Write", testHome + "/.config/telegram-agent-cli-other/x", false},
		{"Write", "/work/README.md", false},
		{"Grep", testDir, true},
		{"Grep", testDir + "/session.json", true},
		{"Grep", "/work", false},
	}
	for _, c := range cases {
		got := CheckFile(c.tool, c.path, o)
		if (got != "") != c.ask {
			t.Errorf("CheckFile(%s, %q) = %q, want ask=%v", c.tool, c.path, got, c.ask)
		}
	}
	if runtime.GOOS == "darwin" {
		if CheckFile("Read", testHome+"/.Config/Telegram-Agent-CLI/session.json", o) == "" {
			t.Error("case-insensitive path not matched on darwin")
		}
	}
}

func TestCheckFileSymlink(t *testing.T) {
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "session.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}
	o := Options{DataDirs: []string{data}, UserHome: tmp}
	if CheckFile("Read", filepath.Join(link, "session.json"), o) == "" {
		t.Error("read through a symlink not caught")
	}
	if CheckFile("Write", filepath.Join(link, "new.json"), o) == "" {
		t.Error("write of a new file through a symlink not caught")
	}
	o = Options{DataDirs: []string{link}, UserHome: tmp}
	if CheckFile("Read", filepath.Join(data, "session.json"), o) == "" {
		t.Error("data dir given as a symlink not resolved")
	}
	real, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	if r := CheckBash("cat "+filepath.Join(real, "session.json"), o); len(r) == 0 {
		t.Error("bash access to the symlink target not caught")
	}
}

func run(t *testing.T, payload string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(strings.NewReader(payload), &out, testOptions()); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestRun(t *testing.T) {
	out := run(t, `{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli send text --chat 1 --message hi"},"cwd":"/work"}`)
	var d Decision
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	h := d.HookSpecificOutput
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "ask" ||
		!strings.Contains(h.PermissionDecisionReason, "explicit OK for this exact command") ||
		!strings.Contains(h.PermissionDecisionReason, "send text") {
		t.Errorf("decision = %+v", h)
	}

	out = run(t, `{"tool_name":"Read","tool_input":{"file_path":"`+testDir+`/session.json"}}`)
	if !strings.Contains(out, `"permissionDecision":"ask"`) {
		t.Errorf("read secret: %q", out)
	}

	for _, p := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"telegram-agent-cli chats list"}}`,
		`{"tool_name":"Bash","tool_input":{"command":42}}`,
		`{"tool_name":"Bash","tool_input":null}`,
		`{"tool_name":"Bash"}`,
		`{"tool_name":"WebFetch","tool_input":{"url":"https://example.com"}}`,
		`{"tool_name":"Read","tool_input":{"file_path":["x"]}}`,
		`not json`,
		``,
		`[]`,
		`{"tool_name":"Bash","tool_input":{"command":"` + strings.Repeat("$(", 5000) + `"}}`,
	} {
		if out := run(t, p); out != "" {
			t.Errorf("Run(%.60q) = %q, want silence", p, out)
		}
	}
}
