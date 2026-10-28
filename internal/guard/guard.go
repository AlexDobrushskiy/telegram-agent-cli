// Package guard refuses non-read MTProto requests before they leave the process.
//
// The method name is the TL schema name from the gotd type (TypeName(), e.g.
// "messages.getHistory"). Wrapper requests (invokeWithLayer, initConnection,
// invokeWithoutUpdates) are checked by their inner query.
package guard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// readPrefixes are method-name prefixes (after the namespace) treated as reads.
var readPrefixes = []string{"get", "search", "resolve", "check"}

// transport are non-read-named requests needed to read: media DC auth, pings, CDN.
var transport = map[string]bool{
	"auth.exportAuthorization": true,
	"auth.importAuthorization": true,
	"upload.reuploadCdnFile":   true,
	"ping":                     true,
	"ping_delay_disconnect":    true,
}

// readNamedWrites look like reads but are visible to others or act on the account.
var readNamedWrites = map[string]bool{
	"messages.getBotCallbackAnswer": true, // presses an inline button
	"messages.getInlineBotResults":  true, // sends a query to a bot
}

// readNamedWriteMarkers deny any method whose name contains them (web views, attach menu, bot apps).
var readNamedWriteMarkers = []string{"webview", "attachmenu", "botapp"}

// authMethods are allowed only in auth mode (the auth subcommands).
var authMethods = map[string]bool{
	"auth.sendCode":         true,
	"auth.resendCode":       true,
	"auth.signIn":           true,
	"auth.checkPassword":    true,
	"auth.exportLoginToken": true,
	"auth.importLoginToken": true,
	"auth.logOut":           true,
}

// alwaysDenied are refused even with read-only off: approving another device's login.
var alwaysDenied = map[string]bool{
	"auth.acceptLoginToken": true,
}

// BlockedError is returned for a refused request.
type BlockedError struct {
	Method string
	Always bool
}

func (e *BlockedError) Error() string {
	if e.Always {
		return fmt.Sprintf("%s is never allowed by telegram-agent-cli", e.Method)
	}
	return fmt.Sprintf("read-only mode blocked %s (writes need TGAC_READONLY=0)", e.Method)
}

// MethodName returns the TL name of a request, or "" if it has none.
func MethodName(req any) string {
	if n, ok := req.(interface{ TypeName() string }); ok {
		return n.TypeName()
	}
	return ""
}

// Allowed reports whether a method name may be called while read-only.
// It cannot see request fields; Check also handles messages.getMessagesViews.
func Allowed(name string, authMode bool) bool {
	if name == "" || alwaysDenied[name] || readNamedWrites[name] {
		return false
	}
	if authMethods[name] {
		return authMode
	}
	if transport[name] {
		return true
	}
	method := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		method = name[i+1:]
	}
	low := strings.ToLower(method)
	for _, m := range readNamedWriteMarkers {
		if strings.Contains(low, m) {
			return false
		}
	}
	for _, p := range readPrefixes {
		if hasWordPrefix(method, p) {
			return true
		}
	}
	return false
}

// hasWordPrefix matches "get" in "get" and "getHistory" but not "getaway".
func hasWordPrefix(method, prefix string) bool {
	if !strings.HasPrefix(method, prefix) {
		return false
	}
	rest := method[len(prefix):]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsUpper(r)
}

// inner returns the wrapped query of a wrapper request.
func inner(req any) (bin.Object, bool) {
	switch r := req.(type) {
	case *tg.InvokeWithLayerRequest:
		return r.Query, true
	case *tg.InitConnectionRequest:
		return r.Query, true
	case *tg.InvokeWithoutUpdatesRequest:
		return r.Query, true
	}
	return nil, false
}

// Check returns a *BlockedError if req must not be sent.
func Check(req any, readOnly, authMode bool) error {
	if q, ok := inner(req); ok {
		if q == nil {
			return &BlockedError{Method: MethodName(req) + " (empty query)"}
		}
		return Check(q, readOnly, authMode)
	}
	name := MethodName(req)
	if alwaysDenied[name] {
		return &BlockedError{Method: name, Always: true}
	}
	if !readOnly {
		return nil
	}
	if name == "" {
		return &BlockedError{Method: fmt.Sprintf("%T", req)}
	}
	if v, ok := req.(*tg.MessagesGetMessagesViewsRequest); ok && v.Increment {
		return &BlockedError{Method: name + " (increment=true)"}
	}
	if !Allowed(name, authMode) {
		return &BlockedError{Method: name}
	}
	return nil
}

// MethodSendMessage is the RPC a watcher reply is granted (see AllowOnce).
const MethodSendMessage = "messages.sendMessage"

type grantKey struct{}

// grant lets one request named method through read-only mode.
type grant struct {
	method string
	used   atomic.Bool
}

func (g *grant) take(name string) bool {
	return g != nil && name != "" && name == g.method && g.used.CompareAndSwap(false, true)
}

// AllowOnce returns a ctx under which exactly one request named method passes
// read-only mode. Other methods, later calls and always-denied methods stay blocked.
func AllowOnce(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, grantKey{}, &grant{method: method})
}

// Middleware is the gotd middleware; readOnly is evaluated per call.
func Middleware(readOnly func() bool, authMode bool) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			if err := Check(input, readOnly(), authMode); err != nil {
				var be *BlockedError
				g, _ := ctx.Value(grantKey{}).(*grant)
				if !errors.As(err, &be) || be.Always || !g.take(MethodName(input)) {
					return err
				}
			}
			return next.Invoke(ctx, input, output)
		}
	})
}
