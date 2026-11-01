package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/e2e"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

const e2eLong = "Drive your own bot as yourself, for end-to-end tests: send text, photos, voice notes,\n" +
	"files and locations, reply, react, press inline and keyboard buttons, send Mini App data, open\n" +
	"Mini App URLs, and read back messages, the reply keyboard, the pinned message and live drafts.\n\n" +
	"Writes go only to bots listed in config.json e2e.allowed_chats (add one with\n" +
	"'TGAC_READONLY=0 telegram-agent-cli e2e allow @bot'). They work in read-only mode: the guard lets\n" +
	"exactly these requests through to that bot and nowhere else. Any other target is refused with\n" +
	"exit 8 (e2e_refused) before anything is sent. --chat defaults to the only allowed bot.\n" +
	"Reads never mark messages as read. A wait that ends without its event exits 9 (timeout)."

func init() {
	register("", func(*App) *cobra.Command {
		c := group("e2e", "End-to-end test harness for your own bots (writes only to allow-listed bots)")
		c.Long = e2eLong
		return c
	})
	register("e2e", func(*App) *cobra.Command { return group("send", "Send to the bot: text, photo, voice, file, location") })
	register("e2e send", (*App).e2eSendTextCmd)
	register("e2e send", (*App).e2eSendPhotoCmd)
	register("e2e send", (*App).e2eSendVoiceCmd)
	register("e2e send", (*App).e2eSendFileCmd)
	register("e2e send", (*App).e2eSendLocationCmd)
	register("e2e", (*App).e2eReactCmd)
	register("e2e", (*App).e2ePressCmd)
	register("e2e", (*App).e2eWebAppDataCmd)
	register("e2e", (*App).e2eWebAppURLCmd)
	register("e2e", (*App).e2eStopCmd)
	register("e2e", func(*App) *cobra.Command {
		return group("read", "Read the bot chat: messages, reply keyboard, pinned message")
	})
	register("e2e read", (*App).e2eReadMessagesCmd)
	register("e2e read", (*App).e2eReadKeyboardCmd)
	register("e2e read", (*App).e2eReadPinnedCmd)
	register("e2e", (*App).e2eWaitCmd)
	register("e2e", (*App).e2eStatusCmd)
	register("e2e", (*App).e2eAllowCmd)
	register("e2e", (*App).e2eDisallowCmd)
}

// e2eConn is one connected e2e command: the allow-listed bot and the session.
type e2eConn struct {
	a   *App
	t   tgclient.E2E
	bot *tgclient.Peer
}

// write returns a ctx under which the guard lets e2e writes through to this bot only.
func (c *e2eConn) write(ctx context.Context) context.Context {
	return guard.AllowBot(ctx, c.bot.RawID)
}

func (c *e2eConn) message(ctx context.Context, id int) (*tgclient.BotMessage, error) {
	msgs, err := c.t.BotMessagesByID(ctx, c.bot, []int{id})
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, output.NotFound("message %d not found in the chat with %s", id, c.bot.Name)
	}
	return &msgs[0], nil
}

func (c *e2eConn) waiter() *e2e.Waiter {
	return &e2e.Waiter{T: c.t, Peer: c.bot, Now: c.a.now, Sleep: c.a.sleep}
}

func (a *App) e2eAllowed() ([]string, error) {
	cfg, err := a.Paths.LoadConfig()
	if err != nil {
		return nil, err
	}
	return cfg.E2EAllowed(), nil
}

// withBot checks --chat against the allow-list, connects, resolves the bot and
// runs fn; fn's result is emitted. A chat that cannot be allowed is refused
// before connecting, and the list is read again once connected (the account
// lock held), so a bot disallowed meanwhile gets nothing.
func (a *App) withBot(ctx context.Context, chat string, fn func(ctx context.Context, c *e2eConn) (any, error)) error {
	allowed, err := a.e2eAllowed()
	if err != nil {
		return err
	}
	if err := e2e.Precheck(allowed, chat); err != nil {
		return err
	}
	var data any
	err = a.ConnectE2E(ctx, a.opts(), func(ctx context.Context, t tgclient.E2E) error {
		allowed, err := a.e2eAllowed()
		if err != nil {
			return err
		}
		bot, err := e2e.Target(ctx, t, allowed, chat)
		if err != nil {
			return err
		}
		data, err = fn(ctx, &e2eConn{a: a, t: t, bot: bot})
		return err
	})
	if err != nil {
		return err
	}
	return a.emit(data)
}

// waitFlags are the optional --wait/--timeout of write commands.
type waitFlags struct {
	kind     string
	timeout  float64
	interval float64
	all      bool
}

func (w *waitFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&w.kind, "wait", "", "after sending, wait for: "+strings.Join(e2e.Kinds, "|"))
	c.Flags().Float64Var(&w.timeout, "timeout", 30, "with --wait: seconds to wait")
	c.Flags().Float64Var(&w.interval, "interval", 1, "with --wait: polling period in seconds")
	c.Flags().BoolVar(&w.all, "all", false, "with --wait draft: collect drafts until the final message")
}

func (w *waitFlags) validate() error {
	if w.kind != "" && !slices.Contains(e2e.Kinds, w.kind) {
		return output.Usage("--wait must be one of %s", strings.Join(e2e.Kinds, "|"))
	}
	if w.timeout <= 0 || w.interval <= 0 {
		return output.Usage("--timeout and --interval must be positive")
	}
	return nil
}

func (w *waitFlags) spec(kind string, since, msg int) e2e.WaitSpec {
	return e2e.WaitSpec{
		For: kind, Since: since, Msg: msg, All: w.all,
		Timeout: secs(w.timeout), Interval: secs(w.interval),
	}
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// watch starts the --wait of a write command (nil without --wait). Call it
// before the write so whatever the write causes counts, however fast it lands.
// msg is the message the write acts on (edit/reaction watch it; 0 = the recent
// ones); mine leaves our own reaction out of a reaction wait.
func (c *e2eConn) watch(ctx context.Context, w *waitFlags, msg int, mine bool) (*e2e.Watch, error) {
	if w.kind == "" {
		return nil, nil
	}
	if w.kind != e2e.ForEdit && w.kind != e2e.ForReaction {
		msg = 0
	}
	s := w.spec(w.kind, 0, msg)
	s.IgnoreMine = mine
	return c.waiter().Begin(ctx, s)
}

// watchSent points a send's reaction wait at the message it sent.
func (c *e2eConn) watchSent(x *e2e.Watch, w *waitFlags, id int) {
	if x != nil && w.kind == e2e.ForReaction {
		x.Only(id)
	}
}

// afterWrite finishes the --wait started by watch and adds its result to out.
func (c *e2eConn) afterWrite(ctx context.Context, x *e2e.Watch, out map[string]any) (any, error) {
	if x == nil {
		return out, nil
	}
	r, err := x.Wait(ctx)
	if err != nil {
		var oe *output.Error
		if errors.As(err, &oe) && oe.Code == output.CodeTimeout {
			// The write happened; say so next to the timeout.
			return nil, oe.WithDetail("write", out)
		}
		return nil, err
	}
	out["wait"] = r
	return out, nil
}

func chatFlag(c *cobra.Command, chat *string) {
	c.Flags().StringVar(chat, "chat", "", "allow-listed bot (@username or id; default: the only one in e2e.allowed_chats)")
}

// sent wraps the message a send created.
func (c *e2eConn) sent(ctx context.Context, id int) (map[string]any, error) {
	m, err := c.message(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"chat": c.bot.Ref(), "sent": m}, nil
}

func (a *App) e2eSendTextCmd() *cobra.Command {
	var (
		chat, text string
		reply      int
		w          waitFlags
	)
	c := &cobra.Command{
		Use:   "text [TEXT]",
		Short: "Send a text message (TEXT or --message)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				if text != "" {
					return output.Usage("give the text as an argument or --message, not both")
				}
				text = args[0]
			}
			if strings.TrimSpace(text) == "" {
				return output.Usage("the message is empty")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				x, err := b.watch(ctx, &w, 0, false)
				if err != nil {
					return nil, err
				}
				id, err := b.t.BotSendText(b.write(ctx), b.bot, text, reply)
				if err != nil {
					return nil, err
				}
				b.watchSent(x, &w, id)
				out, err := b.sent(ctx, id)
				if err != nil {
					return nil, err
				}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().StringVar(&text, "message", "", "text to send")
	c.Flags().IntVar(&reply, "reply-to", 0, "message id to reply to")
	w.add(c)
	return c
}

// mediaCmd builds send photo|voice|file.
func (a *App) mediaCmd(kind, use, short string, caption bool) *cobra.Command {
	var (
		chat, path, capt string
		reply, duration  int
		w                waitFlags
	)
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := os.Stat(path)
			if err != nil || !st.Mode().IsRegular() {
				return output.Usage("--path %q is not a readable file", path)
			}
			if err := w.validate(); err != nil {
				return err
			}
			m := tgclient.OutMedia{Kind: kind, Path: path, Duration: duration}
			if kind == "voice" {
				b, err := os.ReadFile(path)
				if err != nil {
					return output.Usage("cannot read %s: %v", path, err)
				}
				d, err := e2e.OpusDuration(b)
				if err != nil {
					return output.Usage("%s: a voice note must be an Ogg/Opus file (%v)", path, err)
				}
				if m.Duration <= 0 {
					m.Duration = d
				}
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				x, err := b.watch(ctx, &w, 0, false)
				if err != nil {
					return nil, err
				}
				id, err := b.t.BotSendMedia(b.write(ctx), b.bot, m, capt, reply)
				if err != nil {
					return nil, err
				}
				b.watchSent(x, &w, id)
				out, err := b.sent(ctx, id)
				if err != nil {
					return nil, err
				}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().StringVar(&path, "path", "", "file to send")
	if caption {
		c.Flags().StringVar(&capt, "caption", "", "caption")
	}
	if kind == "voice" {
		c.Flags().IntVar(&duration, "duration", 0, "seconds (default: read from the file)")
	}
	c.Flags().IntVar(&reply, "reply-to", 0, "message id to reply to")
	_ = c.MarkFlagRequired("path")
	w.add(c)
	return c
}

func (a *App) e2eSendPhotoCmd() *cobra.Command {
	return a.mediaCmd("photo", "photo", "Send a photo (jpg/png) with an optional caption", true)
}

func (a *App) e2eSendVoiceCmd() *cobra.Command {
	return a.mediaCmd("voice", "voice", "Send a voice note (an Ogg/Opus file; arrives as voice, not a document)", false)
}

func (a *App) e2eSendFileCmd() *cobra.Command {
	return a.mediaCmd("file", "file", "Send a file as a document", true)
}

func (a *App) e2eSendLocationCmd() *cobra.Command {
	var (
		chat      string
		lat, long float64
		reply     int
		w         waitFlags
	)
	c := &cobra.Command{
		Use:   "location",
		Short: "Send a location",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if lat < -90 || lat > 90 || long < -180 || long > 180 {
				return output.Usage("--lat must be in [-90,90] and --long in [-180,180]")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				x, err := b.watch(ctx, &w, 0, false)
				if err != nil {
					return nil, err
				}
				id, err := b.t.BotSendMedia(b.write(ctx), b.bot, tgclient.OutMedia{Kind: "location", Lat: lat, Long: long}, "", reply)
				if err != nil {
					return nil, err
				}
				b.watchSent(x, &w, id)
				out, err := b.sent(ctx, id)
				if err != nil {
					return nil, err
				}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().Float64Var(&lat, "lat", 0, "latitude")
	c.Flags().Float64Var(&long, "long", 0, "longitude")
	c.Flags().IntVar(&reply, "reply-to", 0, "message id to reply to")
	_ = c.MarkFlagRequired("lat")
	_ = c.MarkFlagRequired("long")
	w.add(c)
	return c
}

func (a *App) e2eReactCmd() *cobra.Command {
	var (
		chat, emoji string
		msg         int
		remove      bool
		w           waitFlags
	)
	c := &cobra.Command{
		Use:   "react",
		Short: "Set your reaction on a message (--emoji E), or remove it (--remove)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			emoji = e2e.NormalizeEmoji(emoji)
			if remove == (emoji != "") {
				return output.Usage("give exactly one of --emoji or --remove")
			}
			if msg <= 0 {
				return output.Usage("--msg must be a message id")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				x, err := b.watch(ctx, &w, msg, true)
				if err != nil {
					return nil, err
				}
				if err := b.t.React(b.write(ctx), b.bot, msg, emoji); err != nil {
					return nil, err
				}
				m, err := b.message(ctx, msg)
				if err != nil {
					return nil, err
				}
				out := map[string]any{"chat": b.bot.Ref(), "reacted": map[string]any{"msg": msg, "emoji": emoji, "removed": remove}, "message": m}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().IntVar(&msg, "msg", 0, "message id")
	c.Flags().StringVar(&emoji, "emoji", "", "reaction emoji, e.g. 👍")
	c.Flags().BoolVar(&remove, "remove", false, "remove your reaction")
	w.add(c)
	return c
}

func (a *App) e2ePressCmd() *cobra.Command {
	var (
		chat, button string
		msg          int
		keyboard     bool
		w            waitFlags
	)
	c := &cobra.Command{
		Use:   "press",
		Short: "Press an inline button (--msg N --button B) or a reply-keyboard button (--keyboard --button B)",
		Long: "Inline: presses a callback button of message --msg (like a tap: the bot gets a callback_query)\n" +
			"and returns the bot's answer (answered:false when it never called answerCallbackQuery).\n" +
			"--keyboard: finds the button in the reply keyboard currently shown and sends its exact text,\n" +
			"which is what a tap does. --button is the button text, or its 0-based index row by row.\n" +
			"Mini App (web_app) buttons: use web-app-data or webapp-url.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if button == "" {
				return output.Usage("--button is required")
			}
			if keyboard == (msg > 0) {
				return output.Usage("give exactly one of --msg (inline button) or --keyboard")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				if keyboard {
					return b.pressKeyboard(ctx, button, &w)
				}
				return b.pressInline(ctx, msg, button, &w)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().IntVar(&msg, "msg", 0, "message with the inline button")
	c.Flags().StringVar(&button, "button", "", "button text or 0-based index")
	c.Flags().BoolVar(&keyboard, "keyboard", false, "press a reply-keyboard button")
	w.add(c)
	return c
}

func (c *e2eConn) pressInline(ctx context.Context, msg int, sel string, w *waitFlags) (any, error) {
	m, err := c.message(ctx, msg)
	if err != nil {
		return nil, err
	}
	if len(m.Inline) == 0 {
		return nil, output.NotFound("message %d has no inline buttons", msg)
	}
	btn, err := e2e.FindButton(m.Inline, sel)
	if err != nil {
		return nil, err
	}
	if btn.Type != "callback" {
		return nil, output.Usage("button %q is a %s button, not a callback button", btn.Text, btn.Type)
	}
	if btn.Password {
		return nil, output.Usage("button %q needs the account password; not supported", btn.Text)
	}
	x, err := c.watch(ctx, w, msg, false)
	if err != nil {
		return nil, err
	}
	ans, err := c.t.PressCallback(c.write(ctx), c.bot, msg, btn.CallbackData())
	if err != nil {
		return nil, err
	}
	out := map[string]any{"chat": c.bot.Ref(), "msg": msg, "pressed": btn, "answer": ans}
	return c.afterWrite(ctx, x, out)
}

func (c *e2eConn) pressKeyboard(ctx context.Context, sel string, w *waitFlags) (any, error) {
	kb, err := e2e.ReadKeyboard(ctx, c.t, c.bot)
	if err != nil {
		return nil, err
	}
	if kb == nil {
		return nil, output.NotFound("the chat shows no reply keyboard")
	}
	btn, err := e2e.FindButton(kb.Markup.Rows, sel)
	if err != nil {
		return nil, err
	}
	if btn.Type != "text" {
		return nil, output.Usage("button %q is a %s button; only plain buttons send their text (Mini App: web-app-data / webapp-url)", btn.Text, btn.Type)
	}
	x, err := c.watch(ctx, w, 0, false)
	if err != nil {
		return nil, err
	}
	id, err := c.t.BotSendText(c.write(ctx), c.bot, btn.Text, 0)
	if err != nil {
		return nil, err
	}
	c.watchSent(x, w, id)
	out, err := c.sent(ctx, id)
	if err != nil {
		return nil, err
	}
	out["pressed"] = btn
	out["keyboard_message_id"] = kb.MessageID
	return c.afterWrite(ctx, x, out)
}

func (a *App) e2eWebAppDataCmd() *cobra.Command {
	var (
		chat, button, data string
		w                  waitFlags
	)
	c := &cobra.Command{
		Use:   "web-app-data",
		Short: "Send Mini App data as if the reply-keyboard web_app button's Mini App called sendData",
		Long: "Sends --data through the reply-keyboard web_app button --button (text or index), the way\n" +
			"Telegram.WebApp.sendData does from a Mini App opened by that button: the bot gets a message\n" +
			"with web_app_data {button_text, data}. Uses messages.sendWebViewData; the Mini App page itself\n" +
			"is not loaded (open it with webapp-url to test the page).",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if button == "" {
				return output.Usage("--button is required")
			}
			if len(data) == 0 || len(data) > 4096 {
				return output.Usage("--data must be 1..4096 bytes")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				kb, err := e2e.ReadKeyboard(ctx, b.t, b.bot)
				if err != nil {
					return nil, err
				}
				if kb == nil {
					return nil, output.NotFound("the chat shows no reply keyboard")
				}
				btn, err := e2e.FindButton(kb.Markup.Rows, button)
				if err != nil {
					return nil, err
				}
				if btn.Type != "web_app" {
					return nil, output.Usage("button %q is a %s button, not a web_app button", btn.Text, btn.Type)
				}
				since, err := e2e.NewestID(ctx, b.t, b.bot)
				if err != nil {
					return nil, err
				}
				x, err := b.watch(ctx, &w, 0, false)
				if err != nil {
					return nil, err
				}
				if err := b.t.SendWebAppData(b.write(ctx), b.bot, btn.Text, data); err != nil {
					return nil, err
				}
				out := map[string]any{"chat": b.bot.Ref(), "button": btn, "data": data}
				// The service message Telegram adds on our side, if any.
				if msgs, err := b.t.BotHistory(ctx, b.bot, since, 10); err == nil {
					for _, m := range msgs {
						if m.From == "me" && m.Service != nil {
							mm := m
							out["sent"] = &mm
							break
						}
					}
				}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().StringVar(&button, "button", "", "reply-keyboard web_app button (text or index)")
	c.Flags().StringVar(&data, "data", "", "the data string (what sendData would send)")
	w.add(c)
	return c
}

func (a *App) e2eWebAppURLCmd() *cobra.Command {
	var (
		chat, button, platform string
		msg                    int
		menu, main             bool
	)
	c := &cobra.Command{
		Use:   "webapp-url",
		Short: "Open a Mini App as the client does and print its URL with signed initData",
		Long: "Asks Telegram to open the bot's Mini App and prints the URL it returns, which carries a\n" +
			"freshly signed initData (#tgWebAppData=…). Open that URL in a browser to test the page with a\n" +
			"valid login. Entry points: --button B (reply-keyboard web_app button), --msg N --button B (inline\n" +
			"web_app button), --menu (the bot's menu button), --main (the bot's main Mini App).\n" +
			"The URL is a credential for the Mini App: don't paste it anywhere public.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			n := 0
			for _, on := range []bool{button != "" && msg == 0, msg > 0, menu, main} {
				if on {
					n++
				}
			}
			if n != 1 || (msg > 0 && button == "") {
				return output.Usage("give exactly one of --button, --msg with --button, --menu, --main")
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				req, label, err := b.webAppRequest(ctx, button, msg, menu, main)
				if err != nil {
					return nil, err
				}
				req.Platform = platform
				u, err := b.t.WebAppURL(b.write(ctx), b.bot, req)
				if err != nil {
					return nil, err
				}
				return map[string]any{"chat": b.bot.Ref(), "entry": req.Kind, "button": label, "url": u}, nil
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().StringVar(&button, "button", "", "web_app button text or index")
	c.Flags().IntVar(&msg, "msg", 0, "message with an inline web_app button")
	c.Flags().BoolVar(&menu, "menu", false, "the bot's menu button")
	c.Flags().BoolVar(&main, "main", false, "the bot's main Mini App")
	c.Flags().StringVar(&platform, "platform", tgclient.DefaultWebAppPlatform, "client platform to report (android, ios, tdesktop, web…)")
	return c
}

func (c *e2eConn) webAppRequest(ctx context.Context, button string, msg int, menu, main bool) (tgclient.WebAppRequest, string, error) {
	switch {
	case main:
		return tgclient.WebAppRequest{Kind: "main"}, "", nil
	case menu:
		mb, err := c.t.MenuButton(ctx, c.bot)
		if err != nil {
			return tgclient.WebAppRequest{}, "", err
		}
		if mb == nil {
			return tgclient.WebAppRequest{}, "", output.NotFound("%s has no Mini App menu button", c.bot.Name)
		}
		return tgclient.WebAppRequest{Kind: "menu", URL: mb.URL}, mb.Text, nil
	case msg > 0:
		m, err := c.message(ctx, msg)
		if err != nil {
			return tgclient.WebAppRequest{}, "", err
		}
		btn, err := e2e.FindButton(m.Inline, button)
		if err != nil {
			return tgclient.WebAppRequest{}, "", err
		}
		if btn.Type != "web_app" {
			return tgclient.WebAppRequest{}, "", output.Usage("button %q is a %s button, not a web_app button", btn.Text, btn.Type)
		}
		return tgclient.WebAppRequest{Kind: "inline", URL: btn.URL}, btn.Text, nil
	}
	kb, err := e2e.ReadKeyboard(ctx, c.t, c.bot)
	if err != nil {
		return tgclient.WebAppRequest{}, "", err
	}
	if kb == nil {
		return tgclient.WebAppRequest{}, "", output.NotFound("the chat shows no reply keyboard")
	}
	btn, err := e2e.FindButton(kb.Markup.Rows, button)
	if err != nil {
		return tgclient.WebAppRequest{}, "", err
	}
	if btn.Type != "web_app" {
		return tgclient.WebAppRequest{}, "", output.Usage("button %q is a %s button, not a web_app button", btn.Text, btn.Type)
	}
	return tgclient.WebAppRequest{Kind: "keyboard", URL: btn.URL}, btn.Text, nil
}

func (a *App) e2eStopCmd() *cobra.Command {
	var (
		chat    string
		draftID int64
		w       waitFlags
	)
	c := &cobra.Command{
		Use:   "stop",
		Short: "Tap stop on the bot's live draft --draft ID (the bot gets stopped_message_generation)",
		Long: "Does what the stop button on a streaming (sendMessageDraft) reply does: sends\n" +
			"messages.setTyping with sendMessageStopDraftAction for that draft. The draft id is the draft_id\n" +
			"from 'e2e wait --for draft'. Works only while the bot sent the draft with can_stop.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if draftID == 0 {
				return output.Usage("--draft is required (the draft_id from wait --for draft)")
			}
			if err := w.validate(); err != nil {
				return err
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				x, err := b.watch(ctx, &w, 0, false)
				if err != nil {
					return nil, err
				}
				if err := b.t.StopDraft(b.write(ctx), b.bot, draftID); err != nil {
					return nil, err
				}
				out := map[string]any{"chat": b.bot.Ref(), "stopped": draftID}
				return b.afterWrite(ctx, x, out)
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().Int64Var(&draftID, "draft", 0, "draft_id of the live draft")
	w.add(c)
	return c
}

func (a *App) e2eReadMessagesCmd() *cobra.Command {
	var (
		chat, from   string
		since, limit int
	)
	c := &cobra.Command{
		Use:   "messages",
		Short: "Messages of the bot chat, oldest first: text, entities, reply_to, reactions, buttons, keyboards",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if limit < 1 || limit > 100 {
				return output.Usage("--limit must be 1..100")
			}
			if from != "any" && from != "bot" && from != "me" {
				return output.Usage("--from must be bot, me or any")
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				msgs, err := b.t.BotHistory(ctx, b.bot, since, limit)
				if err != nil {
					return nil, err
				}
				out := []tgclient.BotMessage{}
				for _, m := range msgs {
					if from == "any" || m.From == from {
						out = append(out, m)
					}
				}
				slices.Reverse(out)
				return map[string]any{"chat": b.bot.Ref(), "messages": out}, nil
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().IntVar(&since, "since", 0, "only messages with a larger id")
	c.Flags().IntVar(&limit, "limit", 20, "newest N messages (1..100)")
	c.Flags().StringVar(&from, "from", "any", "bot, me or any")
	return c
}

func (a *App) e2eReadKeyboardCmd() *cobra.Command {
	var chat string
	c := &cobra.Command{
		Use:   "keyboard",
		Short: "The reply keyboard the chat shows now: buttons, placeholder, persistent (null = none)",
		Long: "The reply keyboard in effect is the one on the newest bot message that carries a non-inline\n" +
			"markup (a later 'remove keyboard' or force-reply means none). Shows its buttons (text and type,\n" +
			"web_app URLs), input_field_placeholder, is_persistent, resize and one_time, and the message that set it.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				kb, err := e2e.ReadKeyboard(ctx, b.t, b.bot)
				if err != nil {
					return nil, err
				}
				return map[string]any{"chat": b.bot.Ref(), "keyboard": kb}, nil
			})
		},
	}
	chatFlag(c, &chat)
	return c
}

func (a *App) e2eReadPinnedCmd() *cobra.Command {
	var chat string
	c := &cobra.Command{
		Use:   "pinned",
		Short: "The pinned message of the bot chat (null = none)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				m, err := e2e.ReadPinned(ctx, b.t, b.bot)
				if err != nil {
					return nil, err
				}
				return map[string]any{"chat": b.bot.Ref(), "pinned": m}, nil
			})
		},
	}
	chatFlag(c, &chat)
	return c
}

func (a *App) e2eWaitCmd() *cobra.Command {
	var (
		chat, kind     string
		since, msg     int
		timeout, every float64
		all            bool
	)
	c := &cobra.Command{
		Use:   "wait",
		Short: "Wait for the bot: --for reply|edit|reaction|pin|keyboard|draft (exit 9 on timeout)",
		Long: "Polls the chat until the event happens, then prints it; exit 9 (timeout) if it doesn't.\n" +
			"  reply     a new bot message with id > --since (default: the newest id at start); returns the\n" +
			"            first one and all of them\n" +
			"  edit      a bot message's text, entities, buttons or media change (--msg N, or the 20 newest)\n" +
			"  reaction  reactions on a message change, added or removed (--msg N, or the 20 newest, both sides)\n" +
			"  pin       the pinned message changes or is edited\n" +
			"  keyboard  the reply keyboard in effect changes\n" +
			"  draft     a live draft arrives (sendMessageDraft); --all collects drafts until the final message\n" +
			"To avoid races, note an id first (from a send's output) and pass it as --since, or use --wait on\n" +
			"the write command itself.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !slices.Contains(e2e.Kinds, kind) {
				return output.Usage("--for must be one of %s", strings.Join(e2e.Kinds, "|"))
			}
			if timeout <= 0 || every <= 0 {
				return output.Usage("--timeout and --interval must be positive")
			}
			return a.withBot(c.Context(), chat, func(ctx context.Context, b *e2eConn) (any, error) {
				r, err := b.waiter().Wait(ctx, e2e.WaitSpec{
					For: kind, Since: since, Msg: msg, All: all, Timeout: secs(timeout), Interval: secs(every),
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"chat": b.bot.Ref(), "wait": r}, nil
			})
		},
	}
	chatFlag(c, &chat)
	c.Flags().StringVar(&kind, "for", "", strings.Join(e2e.Kinds, "|"))
	c.Flags().IntVar(&since, "since", 0, "reply/draft: count only messages with a larger id")
	c.Flags().IntVar(&msg, "msg", 0, "edit/reaction: watch this message only")
	c.Flags().Float64Var(&timeout, "timeout", 30, "seconds")
	c.Flags().Float64Var(&every, "interval", 1, "polling period in seconds")
	c.Flags().BoolVar(&all, "all", false, "draft: collect drafts until the final message")
	_ = c.MarkFlagRequired("for")
	return c
}

func (a *App) e2eStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show e2e.allowed_chats and what each entry resolves to",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			allowed, err := a.e2eAllowed()
			if err != nil {
				return err
			}
			type row struct {
				Entry string            `json:"entry"`
				Chat  *tgclient.ChatRef `json:"chat,omitempty"`
				OK    bool              `json:"ok"`
				Error string            `json:"error,omitempty"`
			}
			rows := []row{}
			if len(allowed) == 0 {
				return a.emit(map[string]any{"allowed_chats": rows})
			}
			err = a.ConnectE2E(c.Context(), a.opts(), func(ctx context.Context, t tgclient.E2E) error {
				for _, e := range allowed {
					r := row{Entry: e}
					p, err := t.Resolve(ctx, e)
					switch {
					case err != nil:
						r.Error = output.As(tgclient.MapError(err)).Message
					case p.Type != "bot":
						ref := p.Ref()
						r.Chat, r.Error = &ref, "not a bot: e2e refuses it"
					default:
						ref := p.Ref()
						r.Chat, r.OK = &ref, true
					}
					rows = append(rows, r)
				}
				return nil
			})
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"allowed_chats": rows})
		},
	}
}

func (a *App) e2eAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "allow @BOT",
		Short: "Add a bot to e2e.allowed_chats (needs TGAC_READONLY=0 and the owner's OK)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := a.Paths.LoadConfig()
			if err != nil {
				return err
			}
			var entry string
			err = a.ConnectE2E(c.Context(), a.opts(), func(ctx context.Context, t tgclient.E2E) error {
				p, err := t.Resolve(ctx, args[0])
				if err != nil {
					return err
				}
				if p.Type != "bot" {
					return output.E2ERefused("%s is a %s, not a bot; e2e only writes to bots", args[0], p.Type)
				}
				entry = "@" + p.Username
				if p.Username == "" {
					entry = strconv.FormatInt(p.ID, 10)
				}
				return nil
			})
			if err != nil {
				return err
			}
			// Re-read: the file may have changed while we were connected.
			if cfg, err = a.Paths.LoadConfig(); err != nil {
				return err
			}
			if cfg.E2E == nil {
				cfg.E2E = &config.E2E{}
			}
			if !slices.ContainsFunc(cfg.E2E.AllowedChats, func(s string) bool { return strings.EqualFold(s, entry) }) {
				cfg.E2E.AllowedChats = append(cfg.E2E.AllowedChats, entry)
			}
			if err := a.Paths.SaveConfig(cfg); err != nil {
				return err
			}
			return a.emit(map[string]any{"allowed_chats": cfg.E2E.AllowedChats})
		},
	}
}

func (a *App) e2eDisallowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disallow ENTRY",
		Short: "Remove an entry from e2e.allowed_chats (needs TGAC_READONLY=0)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := a.Paths.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.E2E == nil {
				return output.NotFound("%s is not in e2e.allowed_chats", args[0])
			}
			n := len(cfg.E2E.AllowedChats)
			cfg.E2E.AllowedChats = slices.DeleteFunc(cfg.E2E.AllowedChats, func(s string) bool { return e2e.SameRef(s, args[0]) })
			if len(cfg.E2E.AllowedChats) == n {
				return output.NotFound("%s is not in e2e.allowed_chats", args[0])
			}
			if err := a.Paths.SaveConfig(cfg); err != nil {
				return err
			}
			return a.emit(map[string]any{"allowed_chats": cfg.E2E.AllowedChats})
		},
	}
}
