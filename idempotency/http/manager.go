package http

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/idempotency"
)

// Store is the record store an HTTP manager reads and writes. It is spelled
// out because the type is a mouthful at every call site.
type Store = cache.Cache[idempotency.Record[Response]]

// Recordable is the HTTP rule for which responses are worth recording.
//
// A 4xx is a stable answer: the same request will be rejected the same way, so
// replaying it is both correct and cheaper than running the handler again. A
// 5xx is not. It usually means the work never landed, and pinning it for the
// whole TTL would leave a client unable to ever succeed with that key — so the
// claim is released and the next attempt runs the handler.
//
// The cost of that choice is the one hole in the guarantee: a handler that has
// its effect and then fails will repeat the effect on retry. Recording 5xx
// instead trades that rare case for a common and worse one.
func Recordable(res *Response) bool {
	return res.StatusCode < http.StatusInternalServerError
}

// NewManager builds a manager for HTTP responses with Recordable already
// applied.
//
// It exists so the rule above cannot be forgotten. idempotency.NewManager
// records everything by default, which is right for a package that knows
// nothing about status codes and wrong for HTTP — and the failure is silent:
// a 500 recorded once replays for the whole TTL.
//
// Options are appended after the default, so a caller passing their own
// idempotency.WithRecordable still wins.
func NewManager(
	store Store,
	locker distributedlock.ScopedLocker,
	opts ...idempotency.Option,
) (*idempotency.Manager[Response], error) {
	return idempotency.NewManager(
		store,
		locker,
		append([]idempotency.Option{idempotency.WithRecordable(Recordable)}, opts...)...,
	)
}
