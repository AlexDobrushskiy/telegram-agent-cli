package cli

import (
	"context"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", func(*App) *cobra.Command {
		return group("send", "Send as the account owner (needs TGAC_READONLY=0 and the owner's OK)")
	})
	register("send", (*App).sendTextCmd)
	register("send", (*App).sendFileCmd)
}

func (a *App) sendTextCmd() *cobra.Command {
	var (
		chat, text string
		reply      int
	)
	c := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireWrite("send text"); err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return output.Usage("--message is empty")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				m, err := t.SendText(ctx, p, text, reply)
				if err != nil {
					return nil, err
				}
				return map[string]any{"sent": m}, nil
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", "prefer the numeric id from chats list; "+chatFlagHelp)
	c.Flags().StringVar(&text, "message", "", "text to send")
	c.Flags().IntVar(&reply, "reply-to", 0, "message id to reply to")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("message")
	return c
}

func (a *App) sendFileCmd() *cobra.Command {
	var (
		chat, path, caption string
		reply               int
	)
	c := &cobra.Command{
		Use:   "file",
		Short: "Send a file (jpg/png go as photos)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireWrite("send file"); err != nil {
				return err
			}
			if st, err := os.Stat(path); err != nil || st.IsDir() {
				return output.Usage("--path %q is not a readable file", path)
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				m, err := t.SendFile(ctx, p, path, caption, reply)
				if err != nil {
					return nil, err
				}
				return map[string]any{"sent": m}, nil
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", "prefer the numeric id from chats list; "+chatFlagHelp)
	c.Flags().StringVar(&path, "path", "", "file to send")
	c.Flags().StringVar(&caption, "caption", "", "caption")
	c.Flags().IntVar(&reply, "reply-to", 0, "message id to reply to")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("path")
	return c
}
