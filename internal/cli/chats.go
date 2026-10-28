package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", func(*App) *cobra.Command { return group("chats", "Dialogs: private chats, groups, channels") })
	register("chats", (*App).chatsListCmd)
	register("chats", (*App).chatsShowCmd)
}

var chatTypes = []string{"user", "bot", "group", "supergroup", "channel", "forum"}

const chatFlagHelp = "marked id, @username, t.me link, +phone, or dialog name"

func (a *App) chatsListCmd() *cobra.Command {
	var (
		limit           int
		unread, archive bool
		ctype, query    string
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "List dialogs (pinned first, then most recent)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if ctype != "" && !contains(chatTypes, ctype) {
				return output.Usage("--type must be one of %s", strings.Join(chatTypes, ", "))
			}
			if limit <= 0 {
				return output.Usage("--limit must be positive")
			}
			folder := tgclient.FolderMain
			if archive {
				folder = tgclient.FolderArchive
			}
			q := strings.ToLower(query)
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				out := []tgclient.Dialog{}
				err := t.IterDialogs(ctx, folder, func(d tgclient.Dialog) bool {
					if (unread && d.Unread == 0) || (ctype != "" && d.Type != ctype) ||
						(q != "" && !strings.Contains(strings.ToLower(d.Name), q)) {
						return true
					}
					out = append(out, d)
					return len(out) < limit
				})
				return map[string]any{"chats": out}, err
			})
		},
	}
	f := c.Flags()
	f.IntVar(&limit, "limit", 30, "max chats")
	f.BoolVar(&unread, "unread", false, "only chats with unread messages")
	f.BoolVar(&archive, "archived", false, "the archive folder instead of the main list")
	f.StringVar(&ctype, "type", "", "only this type: "+strings.Join(chatTypes, "|"))
	f.StringVar(&query, "query", "", "name substring")
	return c
}

func (a *App) chatsShowCmd() *cobra.Command {
	var chat string
	c := &cobra.Command{
		Use:   "show",
		Short: "Details of one chat or user (about, members, phone)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				return t.PeerInfo(ctx, p)
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", chatFlagHelp)
	_ = c.MarkFlagRequired("chat")
	return c
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
