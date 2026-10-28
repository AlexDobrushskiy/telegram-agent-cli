package config

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

const lockPoll = 300 * time.Millisecond

// Lock is an flock on a file; the data dir's .lock lets one process talk to Telegram at a time.
type Lock struct {
	path string
	busy string // what holds it, for the lock_busy message
	f    *os.File
}

// NewLock returns an unlocked Lock on the data dir's .lock file.
func (p Paths) NewLock() *Lock {
	return &Lock{path: p.Lock(), busy: "another telegram-agent-cli is talking to Telegram"}
}

// NewWatchLock returns the lock that serialises watcher check-and-send runs.
func (p Paths) NewWatchLock() *Lock {
	return &Lock{path: p.WatchLock(), busy: "another 'watch reply' is running"}
}

// Acquire waits up to wait for the exclusive lock; a timeout is a lock_busy error.
func (l *Lock) Acquire(ctx context.Context, wait time.Duration) error {
	if l.f != nil {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			l.f = f
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return output.LockBusy("%s (waited %.0fs; set %s to wait longer)", l.busy, wait.Seconds(), EnvLockWait)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// Release drops the lock; safe to call when not held.
func (l *Lock) Release() {
	if l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
