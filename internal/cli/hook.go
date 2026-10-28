package cli

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/hook"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

func init() {
	register("", func(*App) *cobra.Command {
		return group("hook", "Claude Code guard hook: ask the owner before Telegram writes")
	})
	register("hook", (*App).hookPreToolUseCmd)
	register("hook", (*App).hookInstallCmd)
	register("hook", (*App).hookUninstallCmd)
}

// executable is the running binary's path (swapped in tests).
var executable = os.Executable

func (a *App) hookOptions() hook.Options {
	o := hook.DefaultOptions()
	dirs := []string{a.Paths.Home}
	for _, d := range o.DataDirs {
		if d != a.Paths.Home {
			dirs = append(dirs, d)
		}
	}
	o.DataDirs = dirs
	return o
}

func (a *App) hookPreToolUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claude-pretooluse",
		Short: "PreToolUse hook: reads the hook JSON on stdin, prints an \"ask\" decision or nothing",
		Long: "Claude Code PreToolUse hook. Reads the tool call as JSON on stdin. If it would write to\n" +
			"Telegram, change the read-only switch, or touch the data dir, prints a permissionDecision\n" +
			"\"ask\" so the account owner approves that exact call; otherwise prints nothing. Always exits 0.",
		// Never fail: a hook error would block every tool call.
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			_ = hook.Run(a.Stdin, a.Stdout, a.hookOptions())
			return nil
		},
	}
}

func settingsFlag(c *cobra.Command, path *string, dryRun *bool) {
	c.Flags().StringVar(path, "settings", "", "Claude Code settings file (default $CLAUDE_CONFIG_DIR/settings.json or ~/.claude/settings.json)")
	c.Flags().BoolVar(dryRun, "dry-run", false, "show the change without writing")
}

func resolveSettings(path string) (string, error) {
	if path == "" {
		return hook.SettingsPath()
	}
	return filepath.Abs(path)
}

// hookBinary is the absolute path the hook command will run.
func hookBinary(flag string) (string, error) {
	if flag != "" {
		p, err := filepath.Abs(flag)
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", output.Usage("--binary %s is not a file", p)
		}
		return p, nil
	}
	p, err := executable()
	if err != nil {
		return "", err
	}
	if p, err = filepath.Abs(p); err != nil {
		return "", err
	}
	if strings.Contains(p, string(filepath.Separator)+"go-build") {
		return "", output.Usage("running from a temporary 'go run' build (%s); install the binary first or pass --binary", p)
	}
	return p, nil
}

func (a *App) hookInstallCmd() *cobra.Command {
	var path, binary string
	var dryRun bool
	c := &cobra.Command{
		Use:   "install",
		Short: "Add TGAC_READONLY=1 and the guard hooks to Claude Code settings (idempotent, backs up)",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := resolveSettings(path)
			if err != nil {
				return err
			}
			bin, err := hookBinary(binary)
			if err != nil {
				return err
			}
			r, err := hook.Install(p, bin, dryRun, time.Now())
			if err != nil {
				return output.Usage("%v", err)
			}
			return a.emit(r)
		},
	}
	settingsFlag(c, &path, &dryRun)
	c.Flags().StringVar(&binary, "binary", "", "binary path the hook runs (default: this binary)")
	return c
}

func (a *App) hookUninstallCmd() *cobra.Command {
	var path string
	var dryRun bool
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the guard hooks and TGAC_READONLY=1 from Claude Code settings (backs up)",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := resolveSettings(path)
			if err != nil {
				return err
			}
			r, err := hook.Uninstall(p, dryRun, time.Now())
			if err != nil {
				return output.Usage("%v", err)
			}
			return a.emit(r)
		},
	}
	settingsFlag(c, &path, &dryRun)
	return c
}
