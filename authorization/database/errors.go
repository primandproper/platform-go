package database

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

var (
	// ErrInvalidTablePrefix indicates a prefix that is not a plain SQL
	// identifier fragment. Prefixes are interpolated into queries rather than
	// bound, so they are restricted rather than escaped.
	ErrInvalidTablePrefix = platformerrors.New("invalid authorization table prefix")
	// ErrNilExecutor indicates a query executor was required and not supplied.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil authorization database query executor")
	// ErrWrittenNameMissing indicates a role or permission that was written by
	// name inside the caller's transaction and then could not be read back in
	// that same transaction. Every dialect this package runs against shows a
	// transaction its own writes, so this is a broken invariant rather than a
	// state a caller can reach or recover from by retrying; it exists so that
	// the failure names itself instead of surfacing later as a foreign-key
	// violation on a grant.
	ErrWrittenNameMissing = platformerrors.New("a role or permission written by name could not be read back")
)
