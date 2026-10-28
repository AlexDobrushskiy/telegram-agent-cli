package tgclient

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/config"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// FileStorage is gotd session storage: an atomically written 0600 JSON file.
type FileStorage struct {
	Path  string
	Write func(path string, data []byte) error
}

func (f *FileStorage) LoadSession(context.Context) ([]byte, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, session.ErrNotFound
	}
	return b, err
}

func (f *FileStorage) StoreSession(_ context.Context, data []byte) error {
	return f.Write(f.Path, data)
}

// Options configures one connection.
type Options struct {
	Paths      config.Paths
	AppVersion string
	AuthMode   bool // allow auth.* methods through the read-only guard
	NeedAuth   bool // fail with an auth error unless the session is logged in
	// UpdateHandler enables updates (needed for QR login); nil means no updates.
	UpdateHandler telegram.UpdateHandler
	// Middlewares run after the read-only guard, i.e. they see only what goes to the network.
	Middlewares []telegram.Middleware
}

// Conn is a locked, connected client handed to Run's callback.
type Conn struct {
	Client *telegram.Client
	API    *tg.Client
	Config config.Config
	Cache  *PeerCache
	Self   *tg.User // set when Options.NeedAuth
}

// Session returns the Telegram implementation over this connection.
func (c *Conn) Session() *Session { return NewSession(c.API, c.Cache, c.Self) }

const floodSleepThreshold = 30 * time.Second

// Run takes the data dir lock, connects, calls fn, disconnects and saves the peer cache.
func Run(ctx context.Context, opts Options, fn func(ctx context.Context, c *Conn) error) error {
	p := opts.Paths
	cfg, err := p.LoadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return output.Auth("%s: %v (run 'telegram-agent-cli auth init')", p.Config(), err)
	}
	cfg = cfg.WithDefaults(opts.AppVersion)
	if opts.NeedAuth {
		if _, err := os.Stat(p.Session()); err != nil {
			return output.Auth("not logged in: run 'telegram-agent-cli auth qr' or 'auth import-telethon'")
		}
	}
	if err := p.Ensure(); err != nil {
		return err
	}
	lock := p.NewLock()
	if err := lock.Acquire(ctx, config.LockWait()); err != nil {
		return err
	}
	defer lock.Release()
	defer p.FixModes()

	cache := LoadPeerCache(p.Peers())
	client := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: &FileStorage{Path: p.Session(), Write: p.WritePrivate},
		Middlewares: append([]telegram.Middleware{
			guard.Middleware(config.ReadOnly, opts.AuthMode),
			FloodWaitMiddleware(floodSleepThreshold, 3),
		}, opts.Middlewares...),
		Device: telegram.DeviceConfig{
			DeviceModel:    cfg.DeviceModel,
			SystemVersion:  cfg.SystemVersion,
			AppVersion:     cfg.AppVersion,
			SystemLangCode: cfg.LangCode,
			LangCode:       cfg.LangCode,
		},
		UpdateHandler: opts.UpdateHandler,
		NoUpdates:     opts.UpdateHandler == nil,
	})
	err = client.Run(ctx, func(ctx context.Context) error {
		c := &Conn{Client: client, API: client.API(), Config: cfg, Cache: cache}
		if opts.NeedAuth {
			st, err := client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !st.Authorized {
				return output.Auth("not logged in: run 'telegram-agent-cli auth qr' or 'auth import-telethon'")
			}
			c.Self = st.User
		}
		return fn(ctx, c)
	})
	if serr := cache.Save(p.WritePrivate); err == nil && serr != nil {
		err = serr
	}
	return MapError(err)
}

// RunSession is Run for the read/send commands: authorised, wrapped as Telegram.
func RunSession(ctx context.Context, opts Options, fn func(ctx context.Context, t Telegram) error) error {
	opts.NeedAuth = true
	return Run(ctx, opts, func(ctx context.Context, c *Conn) error {
		return fn(ctx, c.Session())
	})
}
