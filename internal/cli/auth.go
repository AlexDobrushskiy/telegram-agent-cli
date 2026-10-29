package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", func(*App) *cobra.Command {
		return group("auth", "Login and session (everything but status needs TGAC_READONLY=0)")
	})
	for _, ctor := range []commandCtor{
		(*App).authInitCmd, (*App).authQRCmd, (*App).authRequestCodeCmd, (*App).authVerifyCmd,
		(*App).authPasswordCmd, (*App).authStatusCmd, (*App).authLogoutCmd, (*App).authImportTelethonCmd,
	} {
		register("auth", ctor)
	}
}

func (a *App) authOpts() tgclient.Options {
	o := a.opts()
	o.AuthMode = true
	return o
}

type pendingLogin struct {
	Phone string `json:"phone"`
	Hash  string `json:"hash"`
}

func (a *App) authInitCmd() *cobra.Command {
	var (
		apiID int
		phone string
	)
	c := &cobra.Command{
		Use:   "init",
		Short: "Store api_id/phone; reads api_hash from stdin (needs TGAC_READONLY=0)",
		Long: "Store the my.telegram.org app credentials in config.json (0600).\n" +
			"api_hash is read from stdin (prompted without echo on a terminal).",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if apiID <= 0 {
				return output.Usage("--api-id must be positive")
			}
			phone = strings.ReplaceAll(strings.TrimSpace(phone), " ", "")
			if !strings.HasPrefix(phone, "+") || len(phone) < 6 {
				return output.Usage("--phone must be in international format, e.g. +15550000000")
			}
			hash, err := a.ReadSecret("api_hash: ")
			if err != nil {
				return err
			}
			if !config.ValidAPIHash(hash) {
				return output.Usage("api_hash must be 32 hex chars (read from stdin)")
			}
			cfg, err := a.Paths.LoadConfig()
			if err != nil && a.Paths.ConfigExists() {
				return err
			}
			cfg.APIID, cfg.APIHash, cfg.Phone = apiID, hash, phone
			if err := a.Paths.SaveConfig(cfg); err != nil {
				return err
			}
			return a.emit(map[string]any{"config": a.Paths.Config(), "api_id": apiID, "phone": phone})
		},
	}
	c.Flags().IntVar(&apiID, "api-id", 0, "api_id from my.telegram.org")
	c.Flags().StringVar(&phone, "phone", "", "account phone, international format")
	_ = c.MarkFlagRequired("api-id")
	_ = c.MarkFlagRequired("phone")
	return c
}

func (a *App) authRequestCodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "request-code",
		Short: "Send a login code to the owner's Telegram app (needs TGAC_READONLY=0)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var out map[string]any
			err := a.Raw(c.Context(), a.authOpts(), func(ctx context.Context, conn *tgclient.Conn) error {
				phone := conn.Config.Phone
				if phone == "" {
					return output.Usage("config.json has no phone: run 'auth init'")
				}
				sent, err := conn.Client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
				if err != nil {
					return err
				}
				switch s := sent.(type) {
				case *tg.AuthSentCode:
					b, _ := json.Marshal(pendingLogin{Phone: phone, Hash: s.PhoneCodeHash})
					if err := a.Paths.WritePrivate(a.Paths.Pending(), b); err != nil {
						return err
					}
					out = map[string]any{"sent_to": phone, "via": strings.TrimPrefix(s.Type.TypeName(), "auth.sentCodeType")}
				case *tg.AuthSentCodeSuccess:
					me, err := conn.Client.Self(ctx)
					if err != nil {
						return err
					}
					out = map[string]any{"status": "logged_in", "me": tgclient.UserJSON(me)}
				default:
					return fmt.Errorf("unexpected sendCode result %T", sent)
				}
				return nil
			})
			if err != nil {
				return err
			}
			return a.emit(out)
		},
	}
}

func (a *App) loadPending() (pendingLogin, error) {
	var p pendingLogin
	b, err := os.ReadFile(a.Paths.Pending())
	if errors.Is(err, os.ErrNotExist) {
		return p, output.Usage("no pending login: run 'telegram-agent-cli auth request-code' first")
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

func (a *App) loggedIn(ctx context.Context, conn *tgclient.Conn) (map[string]any, error) {
	_ = os.Remove(a.Paths.Pending())
	me, err := conn.Client.Self(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "logged_in", "me": tgclient.UserJSON(me)}, nil
}

func passwordFromEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

func (a *App) authVerifyCmd() *cobra.Command {
	var code, pwEnv string
	c := &cobra.Command{
		Use:   "verify",
		Short: "Finish login with the code and, if set, the 2FA password (needs TGAC_READONLY=0)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			pend, err := a.loadPending()
			if err != nil {
				return err
			}
			var out map[string]any
			err = a.Raw(c.Context(), a.authOpts(), func(ctx context.Context, conn *tgclient.Conn) error {
				_, err := conn.Client.Auth().SignIn(ctx, pend.Phone, strings.TrimSpace(code), pend.Hash)
				if errors.Is(err, auth.ErrPasswordAuthNeeded) {
					pw := passwordFromEnv(pwEnv)
					if pw == "" {
						out = map[string]any{"status": "password_needed", "next": "telegram-agent-cli auth password --password-env VAR"}
						return nil
					}
					_, err = conn.Client.Auth().Password(ctx, pw)
				}
				var signUp *auth.SignUpRequired
				if errors.As(err, &signUp) {
					return output.Auth("no Telegram account for %s", pend.Phone)
				}
				if err != nil {
					return err
				}
				out, err = a.loggedIn(ctx, conn)
				return err
			})
			if err != nil {
				return err
			}
			return a.emit(out)
		},
	}
	c.Flags().StringVar(&code, "code", "", "login code from the Telegram app")
	c.Flags().StringVar(&pwEnv, "password-env", "", "env var holding the 2FA password, if the account has one")
	_ = c.MarkFlagRequired("code")
	return c
}

func (a *App) authPasswordCmd() *cobra.Command {
	var pwEnv string
	c := &cobra.Command{
		Use:   "password",
		Short: "Second login step for accounts with 2FA (needs TGAC_READONLY=0)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			pw := passwordFromEnv(pwEnv)
			if pw == "" {
				return output.Usage("$%s is empty", pwEnv)
			}
			var out map[string]any
			err := a.Raw(c.Context(), a.authOpts(), func(ctx context.Context, conn *tgclient.Conn) error {
				if _, err := conn.Client.Auth().Password(ctx, pw); err != nil {
					return err
				}
				var err error
				out, err = a.loggedIn(ctx, conn)
				return err
			})
			if err != nil {
				return err
			}
			return a.emit(out)
		},
	}
	c.Flags().StringVar(&pwEnv, "password-env", "", "env var holding the 2FA password")
	_ = c.MarkFlagRequired("password-env")
	return c
}

func (a *App) authQRCmd() *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "qr",
		Short: "Interactive QR login: Settings → Devices → Link Desktop Device (needs TGAC_READONLY=0)",
		Long: "Shows a login QR on stderr; scan it from the owner's phone (Settings → Devices →\n" +
			"Link Desktop Device). Asks for the 2FA password on this terminal if the account has one.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(c.Context(), timeout)
			defer cancel()
			d := tg.NewUpdateDispatcher()
			loggedIn := qrlogin.OnLoginToken(d)
			opts := a.authOpts()
			opts.UpdateHandler = d
			var out map[string]any
			err := a.Raw(ctx, opts, func(ctx context.Context, conn *tgclient.Conn) error {
				if st, err := conn.Client.Auth().Status(ctx); err == nil && st.Authorized {
					out = map[string]any{"status": "already_logged_in", "me": tgclient.UserJSON(st.User)}
					return nil
				}
				_, err := conn.Client.QR().Auth(ctx, loggedIn, func(_ context.Context, t qrlogin.Token) error {
					fmt.Fprint(a.Stderr, "\033[2J\033[H")
					fmt.Fprintln(a.Stderr, "Telegram → Settings → Devices → Link Desktop Device, then scan:")
					fmt.Fprintln(a.Stderr)
					qrterminal.GenerateWithConfig(t.URL(), qrterminal.Config{
						Level: qrterminal.L, Writer: a.Stderr, HalfBlocks: true,
						BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
						BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK,
						QuietZone: 2,
					})
					return nil
				})
				if errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
					pw, perr := a.ReadSecret("Two-step verification password: ")
					if perr != nil {
						return perr
					}
					_, err = conn.Client.Auth().Password(ctx, pw)
				}
				if errors.Is(err, context.DeadlineExceeded) {
					return output.Auth("QR not scanned within %s", timeout)
				}
				if err != nil {
					return err
				}
				out, err = a.loggedIn(ctx, conn)
				return err
			})
			if err != nil {
				return err
			}
			return a.emit(out)
		},
	}
	c.Flags().Var(newDurFlag(&timeout, 180*time.Second), "timeout", "give up after this long (seconds or a duration: 300, 5m)")
	return c
}

func (a *App) authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Whether the stored session is logged in",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !fileExists(a.Paths.Session()) || !a.Paths.ConfigExists() {
				return a.emit(map[string]any{"authorized": false, "me": nil})
			}
			st, err := a.authStatus(c.Context())
			if err != nil {
				return err
			}
			return a.emit(st)
		},
	}
}

func (a *App) authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Terminate this session; it leaves the owner's device list (needs TGAC_READONLY=0)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts := a.authOpts()
			opts.NeedAuth = true
			err := a.Raw(c.Context(), opts, func(ctx context.Context, conn *tgclient.Conn) error {
				_, err := conn.API.AuthLogOut(ctx)
				return err
			})
			if err != nil {
				return err
			}
			for _, p := range []string{a.Paths.Session(), a.Paths.Peers(), a.Paths.Pending()} {
				_ = os.Remove(p)
			}
			return a.emit(map[string]any{"logged_out": true})
		},
	}
}

func (a *App) authImportTelethonCmd() *cobra.Command {
	var (
		path            string
		force, noVerify bool
	)
	c := &cobra.Command{
		Use:   "import-telethon",
		Short: "Import a Telethon .session (auth key + entity cache), verify with getMe (needs TGAC_READONLY=0)",
		Long: "Reads the Telethon session's `sessions` row (dc_id, server_address, port, auth_key)\n" +
			"and `entities` access hashes, writes session.json, and checks the login.\n" +
			"The imported session shares the Telethon session's authorization (same device entry).",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !fileExists(path) && fileExists(path+".session") {
				path += ".session"
			}
			if !fileExists(path) {
				return output.Usage("no such file: %s", path)
			}
			imp, err := tgclient.ReadTelethonSession(path)
			if err != nil {
				return err
			}
			if err := a.writeImported(c.Context(), imp, force); err != nil {
				return err
			}
			out := map[string]any{"imported": true, "dc": imp.Data.DC, "peers": len(imp.Peers), "verified": false, "me": nil}
			switch {
			case noVerify:
			case !a.Paths.ConfigExists():
				out["hint"] = "run 'auth init' with the same api_id, then 'auth status'"
			default:
				st, err := a.authStatus(c.Context())
				if err != nil {
					return err
				}
				out["verified"], out["me"] = st["authorized"], st["me"]
			}
			return a.emit(out)
		},
	}
	c.Flags().StringVar(&path, "session", "", "path to the Telethon .session file")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing session.json")
	c.Flags().BoolVar(&noVerify, "no-verify", false, "skip the getMe check (offline)")
	_ = c.MarkFlagRequired("session")
	return c
}

func (a *App) writeImported(ctx context.Context, imp *tgclient.TelethonImport, force bool) error {
	p := a.Paths
	if err := p.Ensure(); err != nil {
		return err
	}
	lock := p.NewLock()
	if err := lock.Acquire(ctx, config.LockWait()); err != nil {
		return err
	}
	defer lock.Release()
	if fileExists(p.Session()) && !force {
		return output.Usage("%s already exists: pass --force to replace it", p.Session())
	}
	loader := session.Loader{Storage: &tgclient.FileStorage{Path: p.Session(), Write: p.WritePrivate}}
	if err := loader.Save(ctx, imp.Data); err != nil {
		return err
	}
	cache := tgclient.LoadPeerCache(p.Peers())
	for _, tp := range imp.Peers {
		cache.Put(tp.Peer, tp.Phone, false)
	}
	return cache.Save(p.WritePrivate)
}
