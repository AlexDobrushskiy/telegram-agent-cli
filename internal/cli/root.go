// Package cli holds the cobra commands. Each file registers its commands with
// register(), so new commands (phase 2: wait-new, watch, hook) need no edits here.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

// Build info, set with -ldflags "-X …".
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// App carries I/O and the Telegram connectors; tests swap them for fakes.
type App struct {
	Paths  config.Paths
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// Connect runs fn on an authorised session (tgclient.RunSession).
	Connect func(ctx context.Context, opts tgclient.Options, fn func(context.Context, tgclient.Telegram) error) error
	// Raw runs fn on a bare connection (tgclient.Run); used by auth and doctor.
	Raw func(ctx context.Context, opts tgclient.Options, fn func(context.Context, *tgclient.Conn) error) error

	// StdinIsTerminal reports whether prompts can be shown.
	StdinIsTerminal func() bool
	// ReadSecret reads a secret line without echo when on a terminal.
	ReadSecret func(prompt string) (string, error)
}

// New returns an App wired to the real terminal and Telegram.
func New() (*App, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return nil, err
	}
	a := &App{
		Paths:   config.Paths{Home: home},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		Connect: tgclient.RunSession,
		Raw:     tgclient.Run,
		StdinIsTerminal: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd()))
		},
	}
	a.ReadSecret = a.readSecret
	return a, nil
}

func (a *App) readSecret(prompt string) (string, error) {
	if a.StdinIsTerminal() {
		fmt.Fprint(a.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(a.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

type commandCtor func(a *App) *cobra.Command

// registry maps a parent path ("" = root, "messages", "auth"…) to child constructors.
var registry = map[string][]commandCtor{}

// register adds a command under parent; call it from init().
func register(parent string, ctor commandCtor) {
	registry[parent] = append(registry[parent], ctor)
}

// Root builds the command tree.
func (a *App) Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "telegram-agent-cli",
		Short: "Safe, read-only-by-default Telegram user-account access for AI agents (JSON out)",
		Long: "Read your Telegram account (chats, messages, search, mentions, media) as JSON.\n" +
			"Read-only unless TGAC_READONLY=0|false|no|off; the block is enforced on every MTProto request.\n" +
			"Data dir: $TGAC_HOME (default ~/.config/telegram-agent-cli).",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          noArgs,
		RunE:          func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	root.PersistentFlags().Bool("json", false, "no-op: output is always JSON")
	_ = root.PersistentFlags().MarkHidden("json")
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.SetIn(a.Stdin)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return output.Usage("%v", err) })
	root.CompletionOptions.DisableDefaultCmd = true
	a.attach(root, "")
	return root
}

func (a *App) attach(parent *cobra.Command, path string) {
	for _, ctor := range registry[path] {
		c := ctor(a)
		if c.RunE != nil {
			c.RunE = mapErrors(c.RunE)
		}
		parent.AddCommand(c)
		a.attach(c, strings.TrimSpace(path+" "+c.Name()))
	}
}

// mapErrors makes every command error an *output.Error (unknown ones become internal).
func mapErrors(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		if err := run(c, args); err != nil {
			return tgclient.MapError(err)
		}
		return nil
	}
}

// Execute runs args and returns the process exit code.
func (a *App) Execute(ctx context.Context, args []string) int {
	root := a.Root()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return output.ExitOK
	}
	var oe *output.Error
	if !errors.As(err, &oe) {
		// Errors cobra raises itself: unknown command, missing required flag, bad args.
		oe = output.Usage("%v", err)
	}
	return output.Failure(a.Stdout, oe)
}

func noArgs(c *cobra.Command, args []string) error {
	if len(args) > 0 {
		return output.Usage("unknown command %q for %q", args[0], c.CommandPath())
	}
	return nil
}

// group is a command that only holds subcommands.
func group(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  noArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
}

func (a *App) emit(data any) error { return output.Success(a.Stdout, data) }

func (a *App) opts() tgclient.Options {
	return tgclient.Options{Paths: a.Paths, AppVersion: "telegram-agent-cli " + Version}
}

// withSession runs fn on an authorised session and emits its result.
func (a *App) withSession(ctx context.Context, fn func(context.Context, tgclient.Telegram) (any, error)) error {
	var data any
	err := a.Connect(ctx, a.opts(), func(ctx context.Context, t tgclient.Telegram) error {
		var err error
		data, err = fn(ctx, t)
		return err
	})
	if err != nil {
		return err
	}
	return a.emit(data)
}

// requireWrite refuses write commands in read-only mode, before any connection.
func requireWrite(what string) error {
	if config.ReadOnly() {
		return output.ReadOnly("read-only mode: '%s' needs %s=0 (and the user's OK on the exact content)", what, config.EnvReadOnly)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
