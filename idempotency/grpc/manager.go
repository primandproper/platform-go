package grpc

import (
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/idempotency"

	"google.golang.org/grpc/codes"
)

// Store is the record store a gRPC manager reads and writes. It is spelled out
// because the type is a mouthful at every call site.
type Store = cache.Cache[idempotency.Record[Response]]

// Recordable is the gRPC rule for which outcomes are worth recording.
//
// It records success and the client-fault codes, and refuses the server-fault
// ones. The split is the gRPC counterpart of the HTTP 4xx/5xx line and rests on
// the same reasoning: a client-fault answer is stable, so replaying it is both
// correct and cheaper than running the handler again, while a server-fault
// answer usually means the work never landed. Pinning that for the whole TTL
// would leave the caller unable to ever succeed with the key.
//
// The cost is the same hole HTTP has: a handler that has its effect and then
// fails will repeat the effect on retry.
func Recordable(res *Response) bool {
	switch codes.Code(res.StatusCode) {
	case codes.OK,
		codes.InvalidArgument,
		codes.NotFound,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.OutOfRange:
		return true
	default:
		// Internal, Unavailable, Unknown, DeadlineExceeded, ResourceExhausted,
		// Aborted, DataLoss, Canceled, Unimplemented.
		return false
	}
}

// NewManager builds a manager for gRPC replies with Recordable already
// applied.
//
// It exists so the rule above cannot be forgotten. idempotency.NewManager
// records every outcome, which is right for a package that knows nothing about
// status codes and wrong here — and the failure is silent: an Unavailable
// recorded once replays for the whole TTL.
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
