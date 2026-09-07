package errors

import (
	crdberrors "github.com/cockroachdb/errors"
)

// Re-exports from cockroachdb/errors for construction and wrapping.
//
// Use std "errors" for Is, As and Unwrap — they work with these types, and they
// work on an error that has crossed a gRPC connection too, provided that
// connection has errors/grpc's UnaryErrorDecodingInterceptor installed and the
// RPC was unary; a streaming RPC's error is not decoded on the client yet. [Is]
// below says when you would want cockroachdb's instead.
var (
	New    = crdberrors.New
	Newf   = crdberrors.Newf
	Errorf = crdberrors.Errorf
	Wrap   = crdberrors.Wrap
	Wrapf  = crdberrors.Wrapf

	// Join is also how a failure is reported as a sentinel without losing what
	// caused it. Join(sentinel, cause) matches both under errors.Is, and
	// errors.As still reaches into cause; Wrap(sentinel, cause.Error()) matches
	// only the sentinel, having put everything else somewhere only a human can
	// read it. Reach for it wherever a caller is owed something to branch on and
	// an operator is owed the reason.
	//
	// Note that crdberrors.Mark, which looks like it does this, does not: its
	// mark is visible to cockroachdb's own matcher and not to std errors.Is,
	// which is what this module uses everywhere.
	Join = crdberrors.Join

	EncodeError = crdberrors.EncodeError
	DecodeError = crdberrors.DecodeError

	// Is and As are cockroachdb's rather than the standard library's. They
	// compare an error's *mark* — the type name and message cockroachdb records
	// for it — as well as its identity, which is what an error reconstructed by
	// DecodeError has instead of the identity the sentinel was declared with.
	//
	// Reach for them when holding an error whose provenance is genuinely
	// unknown. They are a superset of the standard library's, but they are not
	// the default recommendation: most errors here never leave the process, and
	// there std errors.Is is the same answer with one less import. The superset
	// is also where they are looser: a mark is a type chain plus a message and
	// not an identity, so two sentinels declared with the same wording in
	// different packages match each other under these, which is why the module
	// keeps sentinel messages unique.
	//
	// In particular they are not needed on an error that came back over gRPC.
	// That was the case they were exported for, and errors/grpc's decoding
	// interceptor now returns an error implementing Is, so std errors.Is matches
	// a sentinel across a connection. Making the common path work was better
	// than documenting a second matcher every call site had to remember.
	Is = crdberrors.Is
	As = crdberrors.As
)

// Common platform sentinels (wire-transmittable via cockroachdb/errors).
var (
	// ErrNilInputParameter is returned when an input parameter is nil.
	ErrNilInputParameter = crdberrors.New("provided input parameter is nil")
	// ErrEmptyInputParameter is returned when an input parameter is empty.
	ErrEmptyInputParameter = crdberrors.New("provided input parameter is empty")

	// ErrInvalidIDProvided indicates a required ID was passed in empty.
	ErrInvalidIDProvided = crdberrors.New("required ID provided is empty")
	// ErrEmptyInputProvided indicates a required input was passed in empty.
	ErrEmptyInputProvided = crdberrors.New("input provided is empty")

	// ErrUnrecognizedInputValue indicates an input that was supplied, is not
	// empty, and is not one of the values the callee accepts — an enum member
	// from a newer client, a misspelled state, a provider name with a typo.
	//
	// It exists because the alternative in practice was to reach for
	// ErrEmptyInputProvided, which says the opposite of what happened. A caller
	// that branches on "they left it out" and gets it for "they sent something I
	// do not know" writes the wrong remedy, and an operator reading the log is
	// told a field was missing while the request plainly carried it.
	//
	// Like every sentinel here its message reaches clients verbatim, so it names
	// no value; wrap it with the offending one.
	ErrUnrecognizedInputValue = crdberrors.New("input provided is not a recognized value")

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
