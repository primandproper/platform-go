package pgnotify

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNilConfig indicates a nil Config was passed to NewListener. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil postgres listener config")

	// ErrChannelTooLong indicates a channel name longer than MaxChannelLength.
	// It is reported rather than left to the server, which would truncate it
	// silently — and truncate the producer's name to something that may or may
	// not still match.
	ErrChannelTooLong = platformerrors.New("postgres notification channel name is too long")
)
