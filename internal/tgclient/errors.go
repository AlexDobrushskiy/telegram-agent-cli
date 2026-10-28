package tgclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

// MapError turns gotd/guard errors into the stable output errors.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe
	}
	var be *guard.BlockedError
	if errors.As(err, &be) {
		return output.ReadOnly("%s", be.Error()).WithDetail("method", be.Method)
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return output.FloodWait(int(d / time.Second))
	}
	if auth.IsUnauthorized(err) || tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN") {
		return output.Auth("not logged in (%v): run 'telegram-agent-cli auth qr' or import a session", rpcType(err))
	}
	if e, ok := tgerr.As(err); ok {
		return output.Telegram("telegram: %s (%d)", e.Message, e.Code).WithDetail("rpc_error", e.Type)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return output.Telegram("timed out talking to Telegram")
	}
	return output.Internal("%v", err)
}

func rpcType(err error) string {
	if e, ok := tgerr.As(err); ok {
		return e.Type
	}
	return "unauthorized"
}

// FloodWaitMiddleware sleeps through short FLOOD_WAITs (≤ max) and retries, like Telethon's flood_sleep_threshold.
func FloodWaitMiddleware(max time.Duration, retries int) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
			for attempt := 0; ; attempt++ {
				err := next.Invoke(ctx, input, out)
				d, ok := tgerr.AsFloodWait(err)
				if !ok || d > max || attempt >= retries {
					return err
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("waiting out FLOOD_WAIT: %w", ctx.Err())
				case <-time.After(d + time.Second):
				}
			}
		}
	})
}
