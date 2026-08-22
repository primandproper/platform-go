package database

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrInvalidTablePrefix indicates a prefix that is not a plain SQL
	// identifier fragment. Prefixes are interpolated into queries rather than
	// bound, so they are restricted rather than escaped.
	ErrInvalidTablePrefix = platformerrors.New("invalid authorization table prefix")
	// ErrNilExecutor indicates a query executor was required and not supplied.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")
)
