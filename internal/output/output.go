// Package output holds the JSON envelope and the stable error/exit-code contract.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SchemaVersion is bumped on breaking changes to the JSON shapes.
const SchemaVersion = "1"

// Exit codes.
const (
	ExitOK       = 0
	ExitInternal = 1
	ExitUsage    = 2
	ExitAuth     = 3 // not logged in, or blocked by read-only mode
	ExitNotFound = 4 // not found or ambiguous
	ExitRPC      = 5 // Telegram RPC error or flood wait
	ExitLockBusy = 6
	ExitWatcher  = 7 // watcher refused to send
)

// Error codes (the "code" field of the error object).
const (
	CodeUsage          = "usage"
	CodeAuth           = "auth"
	CodeReadOnly       = "readonly"
	CodeNotFound       = "not_found"
	CodeAmbiguous      = "ambiguous"
	CodeTelegram       = "telegram"
	CodeFloodWait      = "flood_wait"
	CodeLockBusy       = "lock_busy"
	CodeWatcherRefused = "watcher_refused"
	CodeInternal       = "internal"
)

// Error is an error with a stable code and exit status.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	Exit    int            `json:"-"`
}

func (e *Error) Error() string { return e.Message }

// WithDetail returns e with an extra detail field.
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

func newErr(code string, exit int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Exit: exit}
}

func Usage(format string, a ...any) *Error { return newErr(CodeUsage, ExitUsage, format, a...) }
func Auth(format string, a ...any) *Error  { return newErr(CodeAuth, ExitAuth, format, a...) }
func ReadOnly(format string, a ...any) *Error {
	return newErr(CodeReadOnly, ExitAuth, format, a...)
}
func NotFound(format string, a ...any) *Error {
	return newErr(CodeNotFound, ExitNotFound, format, a...)
}
func Ambiguous(format string, a ...any) *Error {
	return newErr(CodeAmbiguous, ExitNotFound, format, a...)
}
func Telegram(format string, a ...any) *Error {
	return newErr(CodeTelegram, ExitRPC, format, a...)
}
func FloodWait(seconds int) *Error {
	return newErr(CodeFloodWait, ExitRPC, "telegram: FLOOD_WAIT, retry in %d s", seconds).
		WithDetail("retry_after", seconds)
}
func LockBusy(format string, a ...any) *Error {
	return newErr(CodeLockBusy, ExitLockBusy, format, a...)
}
func WatcherRefused(format string, a ...any) *Error {
	return newErr(CodeWatcherRefused, ExitWatcher, format, a...)
}
func Internal(format string, a ...any) *Error {
	return newErr(CodeInternal, ExitInternal, format, a...)
}

// As returns err as *Error, wrapping unknown errors as internal.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal("%v", err)
}

type envelope struct {
	SchemaVersion string `json:"schema_version"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data,omitempty"`
	Error         *Error `json:"error,omitempty"`
}

func write(w io.Writer, v envelope) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Success writes {"schema_version":"1","ok":true,"data":…}.
func Success(w io.Writer, data any) error {
	if data == nil {
		data = struct{}{}
	}
	return write(w, envelope{SchemaVersion: SchemaVersion, OK: true, Data: data})
}

// Failure writes {"schema_version":"1","ok":false,"error":{…}} and returns the exit code.
func Failure(w io.Writer, err error) int {
	e := As(err)
	_ = write(w, envelope{SchemaVersion: SchemaVersion, OK: false, Error: e})
	return e.Exit
}
