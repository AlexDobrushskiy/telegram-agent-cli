package cli

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/watch"
)

func init() {
	register("", func(*App) *cobra.Command {
		c := group("watch", "Watchers: user-approved autonomous replies (the only send path in read-only mode)")
		c.Long = "A watcher is an agent session the user registered to answer a narrow set of chats.\n" +
			"'watch reply' is its only way to send; every rule is checked on each call.\n\n" + watch.RegistryHelp
		return c
	})
	register("watch", (*App).watchReplyCmd)
	register("watch", (*App).watchListCmd)
	register("watch", (*App).watchCheckCmd)
}

// watchConfig is config.json with defaults; without a config the defaults apply
// (a send then fails at connect time with an auth error).
func (a *App) watchConfig() (config.Config, error) {
	if !a.Paths.ConfigExists() {
		return config.Config{}.WithDefaults(""), nil
	}
	cfg, err := a.Paths.LoadConfig()
	if err != nil {
		return cfg, err
	}
	return cfg.WithDefaults(""), nil
}

func (a *App) watchReplyCmd() *cobra.Command {
	var (
		name, chat, text string
		replyTo          int
		dryRun           bool
	)
	c := &cobra.Command{
		Use:   "reply",
		Short: "Send one reply as a registered watcher, if every rule allows it (exit 7 + reason if not)",
		Long: "Sends one text reply on behalf of a registered watcher. Works in read-only mode: the guard\n" +
			"lets exactly this one messages.sendMessage through. Refusals exit 7 with error.code\n" +
			"watcher_refused, the reason in error.message and a stable id in error.details.rule.\n" +
			"Rules, in order: no PAUSE-ALL; registered and valid; no PAUSE; caller ($<watcher_session_env>)\n" +
			"equals the entry's session; text 1..max_chars; per-watcher, global and per-chat limits;\n" +
			"chat is a 1:1 with a person, or a group with groups:true listed in the entry's chats; scope;\n" +
			"1:1: latest message is theirs, not already answered, the owner has not written within backoff;\n" +
			"group: --reply-to required, target not the owner's, not answered by a watcher or the owner,\n" +
			"mentions/replies to the owner if mentions_only, the owner has not written within backoff.\n" +
			"The registry is re-read after taking the watch lock and again right before sending.\n" +
			"--dry-run runs every check (reading the chat) and sends nothing.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			chatID, err := strconv.ParseInt(strings.TrimSpace(chat), 10, 64)
			if err != nil || chatID == 0 {
				return output.Usage("--chat must be a numeric chat id (from chats list)")
			}
			if replyTo < 0 {
				return output.Usage("--reply-to must be a message id")
			}
			text = strings.TrimSpace(text)
			return a.watchReply(c.Context(), name, chatID, text, replyTo, dryRun)
		},
	}
	f := c.Flags()
	f.StringVar(&name, "watcher", "", "registered watcher name")
	f.StringVar(&chat, "chat", "", "numeric chat id")
	f.StringVar(&text, "text", "", "reply text (trimmed)")
	f.IntVar(&replyTo, "reply-to", 0, "message id to reply to (required in groups)")
	f.BoolVar(&dryRun, "dry-run", false, "run every check, send nothing")
	_ = c.MarkFlagRequired("watcher")
	_ = c.MarkFlagRequired("chat")
	_ = c.MarkFlagRequired("text")
	return c
}

func (a *App) watchReply(ctx context.Context, name string, chatID int64, text string, replyTo int, dryRun bool) error {
	cfg, err := a.watchConfig()
	if err != nil {
		return err
	}
	g, err := watch.Open(a.Paths, name, a.getenv(cfg.WatcherSessionEnv), cfg.GlobalPerHour, a.now)
	if err != nil {
		return err
	}
	if err := g.TextLength(text); err != nil {
		return err
	}
	if err := a.Paths.Ensure(); err != nil {
		return err
	}
	// One check-and-send at a time, so two watchers cannot both answer one message.
	if !dryRun {
		lock := a.Paths.NewWatchLock()
		if err := lock.Acquire(ctx, config.LockWait()); err != nil {
			return err
		}
		defer lock.Release()
	}
	// The registry may have changed while this call waited for the lock.
	if err := g.Reload(); err != nil {
		return err
	}
	if err := g.TextLength(text); err != nil {
		return err
	}
	ledger, err := g.LoadLedger()
	if err != nil {
		return err
	}
	if err := g.Limits(chatID, ledger); err != nil {
		return err
	}
	var result any
	err = a.Connect(ctx, a.opts(), func(ctx context.Context, t tgclient.Telegram) error {
		self, err := t.Self(ctx)
		if err != nil {
			return err
		}
		p, err := t.Resolve(ctx, strconv.FormatInt(chatID, 10))
		if err != nil {
			return err
		}
		if p.ID != chatID {
			return watch.Refuse(watch.RuleChatType, "chat %d resolved to a different chat %d", chatID, p.ID)
		}
		plan, err := g.CheckChat(ctx, t, p, self, ledger, replyTo, text)
		if err != nil {
			return err
		}
		if dryRun {
			result = map[string]any{"dry_run": true, "would_send": plan}
			return nil
		}
		// Last look: a revocation, pause or binding change made meanwhile stops the send.
		if err := g.Recheck(ctx, t, p, self, text); err != nil {
			return err
		}
		if err := g.Limits(chatID, ledger); err != nil {
			return err
		}
		sent, sendErr := t.SendText(guard.AllowOnce(ctx, guard.MethodSendMessage), p, text, plan.ReplyTo)
		if err := g.Record(plan, sent, sendErr); err != nil {
			if sendErr == nil && sent != nil {
				return output.Internal("sent message %d but could not log it: %v", sent.ID, err)
			}
			return errors.Join(sendErr, err)
		}
		if sendErr != nil {
			return sendErr
		}
		result = map[string]any{"sent": sent, "watcher": name, "answers": plan.Answers}
		return nil
	})
	if err != nil {
		return err
	}
	return a.emit(result)
}

// watcherSummary is one `watch list` row. The session id itself is never shown.
type watcherSummary struct {
	*watch.Watcher
	Valid         bool     `json:"valid"`
	Problems      []string `json:"problems,omitempty"`
	Paused        bool     `json:"paused"`
	SessionBound  bool     `json:"session_bound"`
	CallerMatches bool     `json:"caller_matches"`
	ListedChats   int      `json:"listed_chats"`
	SendsLastHour int      `json:"sends_last_hour"`
	SendsLastDay  int      `json:"sends_last_day"`
	FailedLastDay int      `json:"failed_last_day"`
	LastSend      *string  `json:"last_send"`
	BadLogLines   int      `json:"bad_log_lines,omitempty"`
}

func (a *App) summarize(reg *watch.Registry, name, caller string) watcherSummary {
	w, problems := reg.Watcher(name)
	s := watcherSummary{Watcher: w, Problems: problems}
	s.SessionBound = w.Session != ""
	s.CallerMatches = caller != "" && caller == w.Session
	s.ListedChats = len(w.Chats)
	for _, p := range w.PausePaths() {
		s.Paused = s.Paused || fileExists(p)
	}
	if w.LogPath() != "" {
		sends, bad, err := watch.ReadJSONL[watch.Send](w.LogPath())
		if err != nil {
			s.Problems = append(s.Problems, "send log: "+err.Error())
		}
		s.BadLogLines = bad
		now := a.now()
		var last time.Time
		for _, x := range sends {
			at := x.Time()
			if at.After(now.Add(-time.Hour)) {
				s.SendsLastHour++
			}
			if at.After(now.Add(-24 * time.Hour)) {
				s.SendsLastDay++
				if !x.OK {
					s.FailedLastDay++
				}
			}
			if at.After(last) {
				last = at
			}
		}
		if !last.IsZero() {
			s.LastSend = ptr(tgclient.ISO(last))
		}
	}
	s.Valid = len(s.Problems) == 0
	return s
}

func (a *App) watchListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Registered watchers with pause state, validity and recent send counts (read-only, offline)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.watchConfig()
			if err != nil {
				return err
			}
			reg, err := watch.LoadRegistry(a.Paths)
			if err != nil {
				return err
			}
			caller := a.getenv(cfg.WatcherSessionEnv)
			rows := []watcherSummary{}
			global := 0
			for _, name := range reg.Names() {
				s := a.summarize(reg, name, caller)
				global += s.SendsLastHour
				rows = append(rows, s)
			}
			return a.emit(map[string]any{
				"registry":               a.Paths.Watchers(),
				"registry_exists":        !reg.Missing,
				"pause_all":              fileExists(a.Paths.PauseAll()),
				"global_per_hour":        cfg.GlobalPerHour,
				"global_sends_last_hour": global,
				"watcher_session_env":    cfg.WatcherSessionEnv,
				"watchers":               rows,
			})
		},
	}
}

func (a *App) watchCheckCmd() *cobra.Command {
	var name string
	c := &cobra.Command{
		Use:   "check",
		Short: "Validate one watcher's registry entry and state (read-only, offline; valid:false lists problems)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.watchConfig()
			if err != nil {
				return err
			}
			reg, err := watch.LoadRegistry(a.Paths)
			if err != nil {
				return err
			}
			if !reg.Has(name) {
				return output.NotFound("watcher %q is not registered in %s", name, a.Paths.Watchers())
			}
			caller := a.getenv(cfg.WatcherSessionEnv)
			s := a.summarize(reg, name, caller)
			var warnings []string
			if s.Approved == "" {
				warnings = append(warnings, "approved is empty: record who approved this watcher and for what")
			}
			if s.SessionBound && !s.CallerMatches {
				warnings = append(warnings, "this shell's $"+cfg.WatcherSessionEnv+" does not match the session binding: replies from here would be refused")
			}
			chats := make([]int64, 0, len(s.Chats))
			for id := range s.Chats {
				chats = append(chats, id)
			}
			slices.Sort(chats)
			return a.emit(map[string]any{
				"watcher":   s,
				"valid":     s.Valid,
				"pause_all": fileExists(a.Paths.PauseAll()),
				"chats":     chats,
				"warnings":  nonNilStrings(warnings),
			})
		},
	}
	c.Flags().StringVar(&name, "watcher", "", "registered watcher name")
	_ = c.MarkFlagRequired("watcher")
	return c
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
