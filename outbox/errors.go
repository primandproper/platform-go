package outbox

import (
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrEmptyTopic indicates a Message was enqueued without a topic.
	ErrEmptyTopic = platformerrors.New("empty outbox message topic")
	// ErrNilPayload indicates a Message was enqueued with no payload.
	ErrNilPayload = platformerrors.New("nil outbox message payload")
	// ErrNilExecutor indicates Enqueue was called without a query executor. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")
	// ErrInvalidClaimMode indicates a claim mode that is unknown, or unsupported
	// by the configured dialect.
	ErrInvalidClaimMode = platformerrors.New("invalid outbox claim mode")
	// ErrNilDatabaseClient indicates a nil database.Client was passed to
	// NewRelay. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")
	// ErrNotifyUnsupported indicates a notify channel was configured on a
	// dialect without LISTEN/NOTIFY. It wraps dialect.ErrUnsupported, so a
	// caller may check either.
	ErrNotifyUnsupported = platformerrors.Wrap(dialect.ErrUnsupported, "outbox notifications require postgres")
	// ErrNilPublisherProvider indicates a nil PublisherProvider was passed to
	// NewRelay. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilPublisherProvider = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil publisher provider")
	// ErrUnnamedSideEffect indicates a side effect was registered without a
	// name. The name is what spans and errors call it by, so an unnamed one is
	// a derived write nothing can attribute.
	ErrUnnamedSideEffect = platformerrors.New("unnamed outbox side effect")
	// ErrNilSideEffect indicates a side effect was registered with a nil
	// function. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilSideEffect = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil outbox side effect")
	// ErrDuplicateSideEffect indicates two side effects were registered under
	// one name. Names are how a trace tells them apart, and a repeated one is
	// usually the same registration wired twice — which would derive the same
	// event twice.
	ErrDuplicateSideEffect = platformerrors.New("duplicate outbox side effect")
)
