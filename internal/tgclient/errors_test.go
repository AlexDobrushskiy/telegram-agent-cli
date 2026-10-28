package tgclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/guard"
	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

func TestMapError(t *testing.T) {
	cases := []struct {
		err  error
		code string
		exit int
	}{
		{&guard.BlockedError{Method: "messages.sendMessage"}, output.CodeReadOnly, output.ExitAuth},
		{tgerr.New(420, "FLOOD_WAIT_120"), output.CodeFloodWait, output.ExitRPC},
		{tgerr.New(401, "AUTH_KEY_UNREGISTERED"), output.CodeAuth, output.ExitAuth},
		{tgerr.New(401, "SESSION_REVOKED"), output.CodeAuth, output.ExitAuth},
		{tgerr.New(400, "PEER_ID_INVALID"), output.CodeTelegram, output.ExitRPC},
		{output.LockBusy("busy"), output.CodeLockBusy, output.ExitLockBusy},
		{errors.New("boom"), output.CodeInternal, output.ExitInternal},
	}
	for _, c := range cases {
		e := output.As(MapError(c.err))
		if e.Code != c.code || e.Exit != c.exit {
			t.Errorf("%v → %s/%d, want %s/%d", c.err, e.Code, e.Exit, c.code, c.exit)
		}
	}
	if e := output.As(MapError(tgerr.New(420, "FLOOD_WAIT_120"))); e.Details["retry_after"] != 120 {
		t.Errorf("retry_after %v", e.Details)
	}
}

type flakyInvoker struct {
	errs  []error
	calls int
}

func (f *flakyInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	f.calls++
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func TestFloodWaitMiddleware(t *testing.T) {
	short := &flakyInvoker{errs: []error{tgerr.New(420, "FLOOD_WAIT_0")}}
	start := time.Now()
	if err := FloodWaitMiddleware(time.Second, 3).Handle(short).Invoke(context.Background(), &tg.HelpGetConfigRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if short.calls != 2 || time.Since(start) < time.Second {
		t.Fatalf("short wait not retried: calls=%d", short.calls)
	}
	long := &flakyInvoker{errs: []error{tgerr.New(420, "FLOOD_WAIT_100")}}
	err := FloodWaitMiddleware(time.Second, 3).Handle(long).Invoke(context.Background(), &tg.HelpGetConfigRequest{}, nil)
	if _, ok := tgerr.AsFloodWait(err); !ok || long.calls != 1 {
		t.Fatalf("long wait should surface: %v calls=%d", err, long.calls)
	}
}
