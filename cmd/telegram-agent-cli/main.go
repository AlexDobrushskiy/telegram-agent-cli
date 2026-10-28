// Command telegram-agent-cli gives AI agents safe, read-only-by-default access to a Telegram user account.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/cli"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app, err := cli.New()
	if err != nil {
		os.Exit(output.Failure(os.Stdout, err))
	}
	code := app.Execute(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
