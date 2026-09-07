package notifications

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications database client")

	// ErrNilExecutor indicates a nil executor. Every method a consumer calls
	// here runs on one the caller supplies — a database.Tx for a write, a
	// database.SQLQueryExecutor for a read — so there is no connection of the
	// store's own for it to fall back to. It wraps errors.ErrNilInputParameter,
	// so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications query executor")

	// ErrScopeMismatch indicates a write whose entity names a different scope
	// than the write does.
	//
	// The scope the call named is what the statement binds, so an entity naming
	// another one is refused rather than corrected: the two disagreeing is a
	// caller holding one tenant's notification and filing it into another, which
	// is a stale value or a mix-up and is not a thing to guess at. An entity that
	// names no scope adopts the argument.
	ErrScopeMismatch = platformerrors.New("notification entity names a different scope than the write")

	// ErrNilNotification indicates a nil *Notification where one was required.
	ErrNilNotification = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notification")

	// ErrNilDevice indicates a nil *Device where one was required.
	ErrNilDevice = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil device")

	// ErrNotificationNotFound indicates a notification that does not exist in the
	// inbox that asked. One belonging to another principal, or to another scope,
	// reads as absent — which is what it is from here, and is the answer that
	// does not turn a read into an oracle for what other people have been told.
	ErrNotificationNotFound = platformerrors.New("notification not found")

	// ErrDeviceNotFound indicates a device registration that does not exist under
	// the principal that asked.
	ErrDeviceNotFound = platformerrors.New("device not found")

	// ErrEmptyPrincipal indicates a write or read addressed to nobody.
	//
	// It is refused rather than stored, because the empty principal is not a
	// wildcard and is not "everybody": an inbox row filed under it is one nobody
	// can list, and a device row under it is a token nothing will ever push to
	// and nothing will ever prune.
	ErrEmptyPrincipal = platformerrors.New("empty principal")

	// ErrEmptyTopic indicates a notification filed under no topic. A topic is
	// what a client groups, mutes and routes by, so a notification without one
	// is one no client can decide what to do with.
	ErrEmptyTopic = platformerrors.New("empty notification topic")

	// ErrEmptyToken indicates a device registration carrying no device token.
	ErrEmptyToken = platformerrors.New("empty device token")

	// ErrUnknownPlatform indicates a platform this package does not serve — one
	// notifications/mobile has no sender for, and therefore one that would never
	// produce the provider feedback that prunes a dead token.
	ErrUnknownPlatform = platformerrors.New("unknown device platform")
)
