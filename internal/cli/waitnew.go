package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/waitnew"
)

func init() {
	register("messages", (*App).messagesWaitNewCmd)
}

const minWaitInterval = 5

func (a *App) messagesWaitNewCmd() *cobra.Command {
	var (
		state, chats, since   string
		dms, groups, mentions bool
		timeout, interval     int
	)
	c := &cobra.Command{
		Use:   "wait-new",
		Short: "Block until new incoming messages arrive after the watermark in --state, then return them",
		Long: "Polls the dialog list every --interval seconds and fetches only chats whose top message\n" +
			"passed the per-chat watermark stored in --state. Returns incoming messages (not yours, no\n" +
			"service messages), oldest first, and advances the watermark; exits 0 with an empty list on\n" +
			"timeout. The Telegram lock is taken per poll, so other commands run while it waits.\n" +
			"Never marks anything read. First run (no state file): records the watermark and returns []\n" +
			"at once, or with --since the messages after that date. One waiter per state file.\n" +
			"Filters: --chats limits to ids; --dms / --groups limit to those chat kinds; --mentions keeps\n" +
			"group messages that mention or reply to you (DMs then count only with --dms).",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if strings.TrimSpace(state) == "" {
				return output.Usage("--state is required")
			}
			if timeout < 0 || interval < minWaitInterval {
				return output.Usage("--timeout must be >= 0 and --interval >= %d", minWaitInterval)
			}
			sinceT, err := parseDate("since", since)
			if err != nil {
				return err
			}
			f := waitnew.Filter{DMs: dms, Groups: groups, Mentions: mentions}
			if chats != "" {
				f.Chats = map[int64]bool{}
				for _, s := range strings.Split(chats, ",") {
					id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
					if err != nil || id == 0 {
						return output.Usage("--chats: %q is not a numeric chat id", s)
					}
					f.Chats[id] = true
				}
			}
			path, err := filepath.Abs(state)
			if err != nil {
				return output.Usage("--state: %v", err)
			}
			return a.waitNew(c.Context(), path, f, sinceT, time.Duration(timeout)*time.Second, time.Duration(interval)*time.Second)
		},
	}
	fl := c.Flags()
	fl.StringVar(&state, "state", "", "watermark file (JSON; created on first run)")
	fl.StringVar(&chats, "chats", "", "comma-separated chat ids to watch (default all)")
	fl.BoolVar(&dms, "dms", false, "1:1 chats")
	fl.BoolVar(&groups, "groups", false, "groups and supergroups")
	fl.BoolVar(&mentions, "mentions", false, "group messages that mention or reply to you")
	fl.IntVar(&timeout, "timeout", 600, "seconds to wait (0 = poll once)")
	fl.IntVar(&interval, "interval", 30, "seconds between polls")
	fl.StringVar(&since, "since", "", "first run only: also return messages after this ISO date (UTC)")
	_ = c.MarkFlagRequired("state")
	return c
}

func (a *App) waitNew(ctx context.Context, path string, f waitnew.Filter, since time.Time, timeout, interval time.Duration) error {
	st, ok, err := waitnew.Load(path)
	if err != nil {
		return output.Usage("%v", err)
	}
	out := map[string]any{"state": path, "initialized": false, "timed_out": false, "polls": 0}
	// finish prints the result, then moves the prepared state into place.
	finish := func(res *waitnew.Result, next *waitnew.State) error {
		var commit func() error
		if next != nil {
			var err error
			if commit, err = waitnew.Prepare(path, next, a.now()); err != nil {
				return err
			}
		}
		out["messages"] = nonNil(res.Messages)
		if len(res.Truncated) > 0 {
			out["truncated_chats"] = res.Truncated
		}
		if err := a.emit(out); err != nil {
			return err
		}
		if commit != nil {
			if err := commit(); err != nil {
				fmt.Fprintf(a.Stderr, "wait-new: could not save %s (messages will be returned again): %v\n", path, err)
			}
		}
		return nil
	}
	if !ok {
		var res *waitnew.Result
		err := a.Connect(ctx, a.opts(), func(ctx context.Context, t tgclient.Telegram) error {
			var err error
			st, res, err = waitnew.Init(ctx, t, f, since)
			return err
		})
		if err != nil {
			return err
		}
		out["initialized"] = true
		return finish(res, st)
	}
	deadline := a.now().Add(timeout)
	for polls := 1; ; polls++ {
		var next *waitnew.State
		var res *waitnew.Result
		err := a.Connect(ctx, a.opts(), func(ctx context.Context, t tgclient.Telegram) error {
			var err error
			next, res, err = waitnew.Poll(ctx, t, st, f)
			return err
		})
		out["polls"] = polls
		switch {
		case err == nil:
			if len(res.Messages) > 0 {
				return finish(res, next)
			}
			// Nothing wanted; still absorb your own and filtered-out messages.
			if err := waitnew.Save(path, next, a.now()); err != nil {
				return err
			}
			st = next
		case output.As(tgclient.MapError(err)).Code == output.CodeLockBusy:
			// Another command holds Telegram; try again next poll.
		default:
			return err
		}
		left := deadline.Sub(a.now())
		if left <= 0 {
			out["timed_out"] = true
			return finish(&waitnew.Result{}, nil)
		}
		if err := a.sleep(ctx, min(interval, left)); err != nil {
			return err
		}
	}
}
