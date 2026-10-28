package cli

import (
	"context"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/tgclient"
)

func init() {
	register("", (*App).meCmd)
	register("", (*App).doctorCmd)
	register("", (*App).versionCmd)
}

func (a *App) meCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "The logged-in user",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withSession(c.Context(), func(ctx context.Context, t tgclient.Telegram) (any, error) {
				return t.Self(ctx)
			})
		},
	}
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Build version",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return a.emit(map[string]any{
				"version": Version, "commit": Commit, "date": Date,
				"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
			})
		},
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// authStatus connects (no auth required) and reports whether the session is logged in.
func (a *App) authStatus(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"authorized": false, "me": nil}
	err := a.Raw(ctx, a.opts(), func(ctx context.Context, c *tgclient.Conn) error {
		st, err := c.Client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		out["authorized"] = st.Authorized
		if st.Authorized {
			out["me"] = tgclient.UserJSON(st.User)
		}
		return nil
	})
	return out, err
}

func (a *App) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Config, session, file modes, login state, read-only flag",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p := a.Paths
			d := map[string]any{
				"version":   Version,
				"home":      p.Home,
				"readonly":  config.ReadOnly(),
				"lock_wait": config.LockWait().Seconds(),
				"config":    fileExists(p.Config()),
				"session":   fileExists(p.Session()),
			}
			modes := map[string]string{}
			for name, path := range map[string]string{"home": p.Home, "config": p.Config(), "session": p.Session(), "peers": p.Peers()} {
				if m := config.Mode(path); m != "" {
					modes[name] = m
				}
			}
			d["modes"] = modes
			var problems []string
			if m := modes["home"]; m != "" && m != "0700" {
				problems = append(problems, "data dir is "+m+", want 0700")
			}
			for _, k := range []string{"config", "session", "peers"} {
				if m := modes[k]; m != "" && m != "0600" {
					problems = append(problems, k+" is "+m+", want 0600")
				}
			}
			configOK := false
			if d["config"] == true {
				cfg, err := p.LoadConfig()
				if err == nil {
					err = cfg.Validate()
				}
				if err != nil {
					problems = append(problems, "config: "+err.Error())
				} else {
					configOK = true
					cfg = cfg.WithDefaults(a.opts().AppVersion)
					d["device"] = map[string]any{
						"device_model": cfg.DeviceModel, "system_version": cfg.SystemVersion,
						"app_version": cfg.AppVersion, "lang_code": cfg.LangCode,
					}
				}
			}
			d["authorized"] = false
			if configOK && d["session"] == true {
				st, err := a.authStatus(c.Context())
				if err != nil {
					d["error"] = output.As(err).Message
				}
				for k, v := range st {
					d[k] = v
				}
			}
			d["problems"] = problems
			if problems == nil {
				d["problems"] = []string{}
			}
			return a.emit(d)
		},
	}
}
