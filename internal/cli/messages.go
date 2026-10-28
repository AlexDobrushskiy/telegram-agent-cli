package cli

import (
	"context"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", func(*App) *cobra.Command {
		return group("messages", "Read and search messages (reading never marks anything read)")
	})
	register("messages", (*App).messagesListCmd)
	register("messages", (*App).messagesSearchCmd)
	register("messages", (*App).messagesShowCmd)
	register("messages", (*App).messagesContextCmd)
	register("messages", (*App).messagesMentionsCmd)
}

func (a *App) messagesListCmd() *cobra.Command {
	var (
		chat, after, before         string
		limit, minID, maxID, offset int
		topic                       int
		asc                         bool
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "Messages of one chat, newest first",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if limit <= 0 {
				return output.Usage("--limit must be positive")
			}
			lo, err := parseDate("after", after)
			if err != nil {
				return err
			}
			hi, err := parseDate("before", before)
			if err != nil {
				return err
			}
			q := tgclient.HistoryQuery{Limit: limit, OffsetID: offset, MinID: minID, MaxID: maxID, Topic: topic, OffsetDate: hi}
			if !lo.IsZero() {
				q.Reverse, q.OffsetDate = true, lo
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				msgs, err := t.History(ctx, p, q)
				if err != nil {
					return nil, err
				}
				if q.Reverse && !hi.IsZero() {
					msgs = beforeTime(msgs, hi)
				}
				ascending := asc || q.Reverse
				sort.SliceStable(msgs, func(i, j int) bool {
					if ascending {
						return msgs[i].ID < msgs[j].ID
					}
					return msgs[i].ID > msgs[j].ID
				})
				var next *int
				if len(msgs) == limit {
					m := msgs[0].ID
					for _, x := range msgs {
						m = min(m, x.ID)
					}
					next = &m
				}
				return map[string]any{"chat": p.Ref(), "messages": nonNil(msgs), "next_offset_id": next}, nil
			})
		},
	}
	f := c.Flags()
	f.StringVar(&chat, "chat", "", chatFlagHelp)
	f.IntVar(&limit, "limit", 50, "max messages")
	f.BoolVar(&asc, "asc", false, "oldest first")
	f.StringVar(&after, "after", "", "ISO date/time (UTC): messages after it, oldest first")
	f.StringVar(&before, "before", "", "ISO date/time (UTC): messages before it")
	f.IntVar(&minID, "min-id", 0, "only ids greater than this (new since last check)")
	f.IntVar(&maxID, "max-id", 0, "only ids lower than this")
	f.IntVar(&offset, "offset-id", 0, "page older: pass data.next_offset_id")
	f.IntVar(&topic, "topic", 0, "forum topic id")
	_ = c.MarkFlagRequired("chat")
	return c
}

func beforeTime(msgs []tgclient.Message, hi time.Time) []tgclient.Message {
	out := msgs[:0]
	for _, m := range msgs {
		if m.Time.Before(hi) {
			out = append(out, m)
		}
	}
	return out
}

func nonNil(m []tgclient.Message) []tgclient.Message {
	if m == nil {
		return []tgclient.Message{}
	}
	return m
}

func (a *App) messagesSearchCmd() *cobra.Command {
	var (
		chat, from, after, before string
		limit                     int
	)
	c := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search messages in one chat, or account-wide without --chat",
		Args:  exactArgs(1, "QUERY"),
		RunE: func(c *cobra.Command, args []string) error {
			if limit <= 0 {
				return output.Usage("--limit must be positive")
			}
			if from != "" && chat == "" {
				return output.Usage("--from needs --chat")
			}
			lo, err := parseDate("after", after)
			if err != nil {
				return err
			}
			hi, err := parseDate("before", before)
			if err != nil {
				return err
			}
			q := tgclient.SearchQuery{Query: args[0], Limit: limit, After: lo, Before: hi}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				var p *tgclient.Peer
				if chat != "" {
					if p, err = t.Resolve(ctx, chat); err != nil {
						return nil, err
					}
				}
				if from != "" {
					if q.From, err = t.Resolve(ctx, from); err != nil {
						return nil, err
					}
				}
				msgs, err := t.Search(ctx, p, q)
				if err != nil {
					return nil, err
				}
				return map[string]any{"query": q.Query, "messages": nonNil(msgs)}, nil
			})
		},
	}
	f := c.Flags()
	f.StringVar(&chat, "chat", "", "limit to one chat (else account-wide); "+chatFlagHelp)
	f.StringVar(&from, "from", "", "sender (id/@username/name); needs --chat")
	f.IntVar(&limit, "limit", 30, "max messages")
	f.StringVar(&after, "after", "", "ISO date (UTC)")
	f.StringVar(&before, "before", "", "ISO date (UTC)")
	return c
}

func (a *App) messagesShowCmd() *cobra.Command {
	var (
		chat string
		ids  []int
	)
	c := &cobra.Command{
		Use:   "show",
		Short: "Specific messages by id",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if len(ids) == 0 {
				return output.Usage("--id is required")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				msgs, err := t.MessagesByID(ctx, p, ids)
				if err != nil {
					return nil, err
				}
				return map[string]any{"messages": nonNil(msgs)}, nil
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", chatFlagHelp)
	c.Flags().IntSliceVar(&ids, "id", nil, "message id (repeat or comma-separate)")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("id")
	return c
}

func (a *App) messagesContextCmd() *cobra.Command {
	var (
		chat       string
		id, around int
	)
	c := &cobra.Command{
		Use:   "context",
		Short: "Messages around one message, oldest first",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if id <= 0 || around < 0 {
				return output.Usage("--id must be positive and --around non-negative")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				p, err := t.Resolve(ctx, chat)
				if err != nil {
					return nil, err
				}
				older, err := t.History(ctx, p, tgclient.HistoryQuery{OffsetID: id + 1, Limit: around + 1})
				if err != nil {
					return nil, err
				}
				newer, err := t.History(ctx, p, tgclient.HistoryQuery{MinID: id, Limit: around, Reverse: true})
				if err != nil {
					return nil, err
				}
				byID := map[int]tgclient.Message{}
				for _, m := range append(older, newer...) {
					byID[m.ID] = m
				}
				out := make([]tgclient.Message, 0, len(byID))
				for _, m := range byID {
					out = append(out, m)
				}
				sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
				return map[string]any{"messages": out}, nil
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", chatFlagHelp)
	c.Flags().IntVar(&id, "id", 0, "message id")
	c.Flags().IntVar(&around, "around", 8, "messages on each side")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("id")
	return c
}

func (a *App) messagesMentionsCmd() *cobra.Command {
	var (
		chat        string
		unread      bool
		limit, scan int
	)
	c := &cobra.Command{
		Use:   "mentions",
		Short: "Messages that @-mention or reply to you (does not mark them read)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if limit <= 0 || scan <= 0 {
				return output.Usage("--limit and --scan must be positive")
			}
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				var p *tgclient.Peer
				if chat != "" {
					var err error
					if p, err = t.Resolve(ctx, chat); err != nil {
						return nil, err
					}
				}
				msgs, err := t.Mentions(ctx, p, tgclient.MentionsQuery{Unread: unread, Limit: limit, Scan: scan})
				if err != nil {
					return nil, err
				}
				return map[string]any{"unread_only": unread, "messages": nonNil(msgs)}, nil
			})
		},
	}
	c.Flags().StringVar(&chat, "chat", "", "limit to one chat (else all chats); "+chatFlagHelp)
	c.Flags().BoolVar(&unread, "unread", false, "only unread mentions (messages.getUnreadMentions)")
	c.Flags().IntVar(&limit, "limit", 30, "max messages")
	c.Flags().IntVar(&scan, "scan", tgclient.DefaultMentionScan,
		"without --chat/--unread: how many most recent groups to search")
	return c
}

func exactArgs(n int, names string) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if len(args) != n {
			return output.Usage("%s takes %s (got %d arguments)", c.CommandPath(), names, len(args))
		}
		return nil
	}
}
