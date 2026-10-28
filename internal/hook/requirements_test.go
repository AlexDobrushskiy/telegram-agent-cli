package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reqCase is one row of the hardening requirements: a Bash command and the
// reason fragment it must ask with ("" = stay silent).
type reqCase struct {
	cmd, want string
}

func checkReqs(t *testing.T, o Options, cases []reqCase) {
	t.Helper()
	for _, c := range cases {
		r := strings.Join(CheckBash(c.cmd, o), "; ")
		switch {
		case c.want == "" && r != "":
			t.Errorf("CheckBash(%q) = %q, want silent", c.cmd, r)
		case c.want != "" && !strings.Contains(r, c.want):
			t.Errorf("CheckBash(%q) = %q, want a reason containing %q", c.cmd, r, c.want)
		}
	}
}

// Rows 3 and 4: read-only override forms and the watcher session variable.
func TestReqOverrides(t *testing.T) {
	checkReqs(t, testOptions(), []reqCase{
		// row 3
		{`export TGAC_READ''ONLY=0`, "TGAC_READONLY"},
		{`export TGAC_READONLY\=0`, "TGAC_READONLY"},
		{`read TGAC_READONLY <<< 0; export TGAC_READONLY`, "TGAC_READONLY"},
		{`printf -v TGAC_READONLY 0`, "TGAC_READONLY"},
		{`python3 -c "import os; os.environ['TGAC_READONLY']=str(0)"`, "TGAC_READONLY"},
		{`perl -e '$ENV{TGAC_READONLY}=0; system("x")'`, "TGAC_READONLY"},
		{`export TGAC_READONLY="of"f`, "TGAC_READONLY"},
		{`export $'TGAC_READ\x4fNLY=0'`, "TGAC_READONLY"},
		{`declare -x TGAC_READONLY=off`, "TGAC_READONLY"},
		{`for TGAC_READONLY in 0; do telegram-agent-cli me; done`, "TGAC_READONLY"},
		{`: ${TGAC_READONLY:=0}`, "TGAC_READONLY"},
		{`declare -n r=TGAC_READONLY; r=0`, "TGAC_READONLY"},
		{`mapfile TGAC_READONLY < f`, "TGAC_READONLY"},
		{`setenv TGAC_READONLY 0`, "TGAC_READONLY"},
		{`launchctl setenv TGAC_READONLY no`, "TGAC_READONLY"},
		{`set -gx TGAC_READONLY 0`, "TGAC_READONLY"},
		{`echo 'export TGAC_READONLY=0' >> ~/.zshrc`, "TGAC_READONLY"},
		{`python3 -c "import os; os.environ['TGAC_'+'READONLY']='0'"`, "TGAC_ variable name"},
		{`export TGAC_$X=0`, "TGAC_ variable name"},
		{`python3 -c "import os; os.environ['TGAC_READONLY'] = '1' if x else '0'"`, "TGAC_READONLY"},
		{`node -e "process.env.TGAC_READONLY = v"`, "TGAC_READONLY"},
		{`ruby -e 'ENV["TGAC_READONLY"] = "#{x}"'`, "TGAC_READONLY"},
		{`python3 -c "subprocess.run(c, env=dict(TGAC_READONLY=str(0)))"`, "TGAC_READONLY"},
		// row 4: watcher session variable (default and configured names)
		{`TGAC_WATCHER_SESSION=abc telegram-agent-cli watch reply --watcher w --chat 1 --text hi`, "TGAC_WATCHER_SESSION"},
		{`export TGAC_WATCHER_SESSION`, "TGAC_WATCHER_SESSION"},
		{`export TGAC_WATCHER_SESSION=x`, "TGAC_WATCHER_SESSION"},
		{`env TGAC_WATCHER_SESSION=x telegram-agent-cli watch reply`, "TGAC_WATCHER_SESSION"},
		{`read TGAC_WATCHER_SESSION`, "TGAC_WATCHER_SESSION"},
		{`python3 -c "import os; os.environ['TGAC_WATCHER_SESSION']='x'"`, "TGAC_WATCHER_SESSION"},
		{`MY_WATCH_SESS=1 telegram-agent-cli watch reply`, "MY_WATCH_SESS"},
		{`export MY_WATCH_SESS`, "MY_WATCH_SESS"},
		// TGAC_HOME: assignment or expansion
		{`export TGAC_HOME=/tmp/x`, "TGAC_HOME"},
		{`ls ${TGAC_HOME}/`, "TGAC_HOME"},
		{`TGAC_HO''ME=/x telegram-agent-cli me`, "TGAC_HOME"},
		{`python3 -c "import os; print(os.environ['TGAC_HOME'])"`, "TGAC_HOME"},

		// must stay silent
		{`git commit -m 'TGAC_READONLY=0 docs'`, ""},
		{`git commit -am "set TGAC_READONLY=0 in the docs"`, ""},
		{`gh pr create --title t --body '… TGAC_READONLY=0 …'`, ""},
		{`rg 'TGAC_READONLY=0' README.md`, ""},
		{`grep -rn TGAC_HOME .`, ""},
		{`git grep '.config/telegram-agent-cli'`, ""},
		{`vim ~/.config/telegram-agent-cli-notes.md`, ""},
		{`ls bin/telegram-agent-cli`, ""},
		{`make build`, ""},
		{`go test ./...`, ""},
		{`export TGAC_READONLY=1`, ""},
		{`export TGAC_READONLY`, ""},
		{`unset TGAC_READONLY TGAC_WATCHER_SESSION`, ""},
		{`TGAC_READONLY=yes telegram-agent-cli me`, ""},
		{`python3 -c "import os; os.environ['TGAC_READONLY'] = '1'"`, ""},
		{`python3 -c "import os; print(os.environ.get('TGAC_READONLY', '1'))"`, ""},
		{`[ "$TGAC_READONLY" = 0 ] && echo writes`, ""},
		{`echo "$TGAC_WATCHER_SESSION"`, ""},
		{`echo "TGAC_HOME is the data dir"`, ""},
		{`python3 -c "import os; print(os.environ['TGAC_READONLY'] == '0')"`, ""},
	})
}

func TestWatcherEnvsFromConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"api_id": 1, "watcher_session_env": "CUSTOM_WS"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := WatcherEnvsFor([]string{dir, filepath.Join(dir, "missing")})
	if strings.Join(got, ",") != "TGAC_WATCHER_SESSION,CUSTOM_WS" {
		t.Fatalf("WatcherEnvsFor = %v", got)
	}
	o := testOptions()
	o.WatcherEnvs = got
	checkReqs(t, o, []reqCase{{"CUSTOM_WS=1 telegram-agent-cli watch reply", "CUSTOM_WS"}})
}
