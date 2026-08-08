package errors

import (
	crdberrors "github.com/cockroachdb/errors"
)

// Re-exports from cockroachdb/errors for construction and wrapping.
// Use std "errors" for Is, As, Unwrap - they work with these types.
var (
	New    = crdberrors.New
	Newf   = crdberrors.Newf
	Errorf = crdberrors.Errorf
	Wrap   = crdberrors.Wrap
	Wrapf  = crdberrors.Wrapf
	Join   = crdberrors.Join

	EncodeError = crdberrors.EncodeError
	DecodeError = crdberrors.DecodeError
)

// Common platform sentinels (wire-transmittable via cockroachdb/errors).
var (
	// ErrNilInputParameter is returned when an input parameter is nil.
	ErrNilInputParameter = crdberrors.New("provided input parameter is nil")
	// ErrEmptyInputParameter is returned when an input parameter is empty.
	ErrEmptyInputParameter = crdberrors.New("provided input parameter is empty")

	// ErrNilInputProvided indicates nil input was provided in an unacceptable context.
	ErrNilInputProvided = crdberrors.New("nil input provided")
	// ErrInvalidIDProvided indicates a required ID was passed in empty.
	ErrInvalidIDProvided = crdberrors.New("required ID provided is empty")
	// ErrEmptyInputProvided indicates a required input was passed in empty.
	ErrEmptyInputProvided = crdberrors.New("input provided is empty")

	// ErrPermissionDenied indicates the requester lacks the authority to perform
	// the action. It lives here rather than in the authorization package so that
	// errors/http and errors/grpc can map it without importing authorization,
	// which imports them back.
	//
	// Its message is deliberately generic: it reaches clients verbatim, and the
	// specific permission that was missing must not.
	ErrPermissionDenied = crdberrors.New("permission denied")

	// ErrResourceInUse indicates the request conflicts with the current state of
	// the resource — most often a delete of something another record still
	// references. It is a client-correctable conflict, not a server failure: the
	// same request may succeed once the references are gone.
	//
	// It lives here rather than in a data-access package for the same reason
	// ErrPermissionDenied does: errors/http and errors/grpc map it, and neither
	// may import a package that imports them back.
	ErrResourceInUse = crdberrors.New("resource is in use")

	// ErrNotEntitled indicates the account's plan does not include the feature
	// the request needs. It is a billing answer rather than a security one: the
	// caller is who they say they are and may do what they asked, they simply
	// have not bought it.
	//
	// It is distinct from ErrPermissionDenied for that reason. Collapsing the two
	// would answer a customer who needs to upgrade with the message shown to one
	// who needs a different role, and would put a paid feature behind a status
	// code that tells a client to stop rather than to buy.
	//
	// It lives here rather than in the entitlements package so that errors/http
	// and errors/grpc can map it without importing entitlements — which would
	// drag a SQL store, a job scheduler, and a message queue into the import
	// graph of the package every handler already depends on.
	ErrNotEntitled = crdberrors.New("not entitled")

	// ErrQuotaExhausted indicates the account is entitled to the feature and has
	// consumed all of it for the current billing period.
	//
	// It is distinct from ratelimiting.ErrRateLimited, which says a request came
	// too fast and will succeed shortly. This one says a period's allowance is
	// spent, and the remedies — wait for the period to roll, or buy more — are
	// neither of them "retry in a moment". A client told the wrong one retries
	// for a month.
	//
	// It lives here for the same reason ErrNotEntitled does.
	ErrQuotaExhausted = crdberrors.New("quota exhausted")

	// ErrUnknownProvider indicates a config named a provider the package does
	// not implement — a typo, or a provider from a newer version of this module.
	//
	// It lives here so that every config subpackage reports the same failure the
	// same way, and so a consumer's startup path can branch on one sentinel
	// rather than on a dozen package-local ones. Constructors wrap it with the
	// offending value; they never substitute a noop implementation, because a
	// misconfigured provider that silently discards its work is a production
	// incident that looks like a healthy process.
	ErrUnknownProvider = crdberrors.New("unknown provider")
)
