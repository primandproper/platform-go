package authorization

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ErrPermissionDenied indicates the requester lacks the authority to perform the
// action.
//
// It is an alias for the platform sentinel rather than a new error, so that
// errors.Is matches whichever one a caller reaches for. The canonical
// declaration lives in the root errors package because errors/http and
// errors/grpc must map it, and they cannot import this package — it imports
// them.
//
// Handlers should return this (optionally wrapped) rather than constructing a
// status or an HTTP code by hand; the platform mappers already turn it into
// 403 with code E110 and into codes.PermissionDenied.
var ErrPermissionDenied = platformerrors.ErrPermissionDenied
