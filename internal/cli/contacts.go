package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", func(*App) *cobra.Command { return group("contacts", "Find people") })
	register("contacts", (*App).contactsSearchCmd)
	register("", func(*App) *cobra.Command { return group("media", "Photos, voice notes, files") })
	register("media", (*App).mediaDownloadCmd)
}

func (a *App) contactsSearchCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "search QUERY",
		Short: "Your contacts and chats first, then Telegram's public directory",
		Args:  exactArgs(1, "QUERY"),
		RunE: func(c *cobra.Command, args []string) error {
			if limit <= 0 {
				return output.Usage("--limit must be positive")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				return t.SearchContacts(ctx, args[0], limit)
			})
		},
	}
	c.Flags().IntVar(&limit, "limit", 20, "max results")
	return c
}

func (a *App) mediaDownloadCmd() *cobra.Command {
	var (
		chat, dir string
		id        int
	)
	c := &cobra.Command{
		Use:   "download",
		Short: "Download a message's photo/file into a directory",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if id <= 0 {
				return output.Usage("--id must be positive")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				return t.DownloadMedia(ctx, p, id, dir)
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", chatFlagHelp)
	c.Flags().IntVar(&id, "id", 0, "message id")
	c.Flags().StringVar(&dir, "output", "", "directory to save into (created if missing)")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("id")
	_ = c.MarkFlagRequired("output")
	return c
}
