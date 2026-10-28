// Package guard refuses every MTProto request that is not on an exact read
// allowlist before it leaves the process.
//
// The method name is the TL schema name from the gotd type (TypeName(), e.g.
// "messages.getHistory"). Wrapper requests (invokeWithLayer, initConnection,
// invokeWithoutUpdates) are checked by their inner query. The allowlist is the
// set of reads the CLI makes plus what gotd sends internally; a name prefix is
// never enough (payments.resolveStarGiftOffer is a "resolve" that moves money).
package guard

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// readMethods are the only requests allowed while read-only.
var readMethods = map[string]bool{
	// Reads the commands make.
	"users.getUsers":             true,
	"users.getFullUser":          true,
	"messages.getDialogs":        true,
	"messages.getHistory":        true,
	"messages.getReplies":        true,
	"messages.search":            true,
	"messages.searchGlobal":      true,
	"messages.getMessages":       true,
	"messages.getUnreadMentions": true,
	"messages.getChats":          true,
	"messages.getFullChat":       true,
	"messages.checkChatInvite":   true,
	"channels.getMessages":       true,
	"channels.getChannels":       true,
	"channels.getFullChannel":    true,
	"contacts.resolveUsername":   true,
	"contacts.resolvePhone":      true,
	"contacts.getContacts":       true,
	"contacts.search":            true,
	// Media download, incl. other DCs and CDN.
	"upload.getFile":           true,
	"upload.getFileHashes":     true,
	"upload.getCdnFile":        true,
	"upload.getCdnFileHashes":  true,
	"upload.reuploadCdnFile":   true,
	"auth.exportAuthorization": true,
	"auth.importAuthorization": true,
	// gotd internals.
	"help.getConfig":        true,
	"help.getCdnConfig":     true,
	"ping":                  true,
	"ping_delay_disconnect": true,
}

// authMethods are allowed only in auth mode (the auth subcommands).
var authMethods = map[string]bool{
	"auth.sendCode":         true,
	"auth.resendCode":       true,
	"auth.signIn":           true,
	"auth.checkPassword":    true,
	"auth.exportLoginToken": true,
	"auth.importLoginToken": true,
	"auth.logOut":           true,
	"account.getPassword":   true, // SRP parameters for the 2FA step
}

// readNamedWrites look like reads but are visible to others or act on the account.
// They are not on the allowlist either; listing them keeps them denied if it grows.
var readNamedWrites = map[string]bool{
	"messages.getBotCallbackAnswer": true, // presses an inline button
	"ephemeral.getCallbackAnswer":   true, // presses an inline button
	"messages.getInlineBotResults":  true, // sends a query to a bot
}

// readNamedWriteMarkers deny any method whose name contains them (web views, attach menu, bot apps).
var readNamedWriteMarkers = []string{"webview", "attachmenu", "botapp"}

// alwaysDenied are refused even with read-only off: approving another device's login.
var alwaysDenied = map[string]bool{
	"auth.acceptLoginToken": true,
}

// riskyFields are argument names that turn a read into a write (spend Stars,
// publish location, count a view). A non-zero value blocks the request.
var riskyFields = []string{"Paid", "Stars", "Expires", "Increment"}

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
// Check also refuses allowed methods used with write-like arguments.
func Allowed(name string, authMode bool) bool {
	if name == "" || alwaysDenied[name] || readNamedWrites[name] {
		return false
	}
	low := strings.ToLower(name)
	for _, m := range readNamedWriteMarkers {
		if strings.Contains(low, m) {
			return false
		}
	}
	if authMethods[name] {
		return authMode
	}
	return readMethods[name]
}

// AllowedMethods lists the read-only allowlist (sorted), for tests and docs.
func AllowedMethods(authMode bool) []string {
	var out []string
	for m := range readMethods {
		out = append(out, m)
	}
	if authMode {
		for m := range authMethods {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// riskyArgument names a set write-like argument of req, or "".
func riskyArgument(req any) string {
	v := reflect.ValueOf(req)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		for _, r := range riskyFields {
			if strings.Contains(f.Name, r) && f.IsExported() && !v.Field(i).IsZero() {
				return f.Name
			}
		}
	}
	if r, ok := req.(*tg.ContactsResolveUsernameRequest); ok && r.Referer != "" {
		return "Referer" // would attribute a referral
	}
	return ""
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
	if !Allowed(name, authMode) {
		return &BlockedError{Method: name}
	}
	if f := riskyArgument(req); f != "" {
		return &BlockedError{Method: name + " (" + f + " set)"}
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
				if !errors.As(err, &be) || be.Always || riskyArgument(input) != "" || !g.take(MethodName(input)) {
					return err
				}
			}
			return next.Invoke(ctx, input, output)
		}
	})
}
