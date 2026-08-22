package http

import (
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/links"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
	"github.com/primandproper/platform-go/v13/sessions"
)

// PlatformMapper maps platform-level errors to HTTP error codes and messages.
// It does not depend on any domain.
var PlatformMapper HTTPErrorMapper = platformMapper{}

type platformMapper struct{}

func (platformMapper) Map(err error) (code ErrorCode, msg string, ok bool) {
	if err == nil {
		return ErrNothingSpecific, "", false
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrDataNotFound, "data not found", true
	case errors.Is(err, database.ErrUserAlreadyExists):
		return ErrValidatingRequestInput, "user already exists", true
	case errors.Is(err, circuitbreaking.ErrCircuitBroken):
		return ErrCircuitBroken, "service temporarily unavailable", true
	case errors.Is(err, platformerrors.ErrPermissionDenied):
		// The message is a constant rather than anything derived from the error:
		// naming the missing permission would disclose the permission taxonomy to
		// a caller that just failed to authorize.
		return ErrUserIsNotAuthorized, "permission denied", true
	case errors.Is(err, platformerrors.ErrResourceInUse):
		return ErrResourceConflict, "resource is in use", true
	// Ordered before ErrNotEntitled, which it does not wrap, but which is the
	// broader of the two: an account that has spent its allowance is entitled,
	// and an account that is not entitled has no allowance to spend. Checking the
	// specific one first keeps a caller that wraps both from collapsing into the
	// vaguer answer.
	case errors.Is(err, platformerrors.ErrQuotaExhausted):
		// The message says nothing about the limit, the feature, or how much is
		// left. What remains is on the decision the handler already has, where it
		// can be rendered next to an upgrade link rather than leaked to whoever
		// probes an endpoint for the shape of somebody else's plan.
		return ErrQuotaExhausted, "quota exhausted for the current billing period", true
	case errors.Is(err, platformerrors.ErrNotEntitled):
		return ErrNotEntitled, "not entitled", true
	case errors.Is(err, ratelimiting.ErrRateLimited):
		// The message says nothing about the limit or the key it was counted
		// against. Both are useful to an operator and useful to an attacker
		// probing for the threshold; when a limiter can say when to come back,
		// that answer belongs in Retry-After, where a client can act on it.
		return ErrTooManyRequests, "too many requests", true
	// Ordered before ErrInvalidSignature, which it does not wrap, but the pair
	// reads as one decision: a stale signature is the only verification failure
	// with a cause the caller can diagnose, so it is the only one told what to
	// fix. Neither message says anything about the key.
	case errors.Is(err, requestsigning.ErrStaleSignature):
		return ErrInvalidRequestSignature, "request signature timestamp outside tolerance", true
	case errors.Is(err, requestsigning.ErrInvalidSignature):
		return ErrInvalidRequestSignature, "invalid request signature", true
	// Ordered before ErrNotFound, which both of these wrap. Every unusable
	// session — absent, forged, expired — resolves to the same status and a
	// message that says nothing about which: telling a client apart "no such
	// session" from "that one expired" is an oracle for whether a guessed
	// identifier ever existed.
	case errors.Is(err, sessions.ErrExpired):
		return ErrFetchingSessionContextData, "session expired", true
	case errors.Is(err, sessions.ErrNotFound):
		return ErrFetchingSessionContextData, "no active session", true
	// One code for the four, and a message per outcome. The message is the half
	// a person reads, and here it can be specific without disclosing anything —
	// see ErrActionLinkUnusable on why an action link is not a session cookie in
	// this respect.
	case errors.Is(err, links.ErrLinkAlreadyRedeemed):
		return ErrActionLinkUnusable, "this link has already been used", true
	case errors.Is(err, links.ErrLinkExpired):
		return ErrActionLinkUnusable, "this link has expired", true
	case errors.Is(err, links.ErrLinkRevoked):
		return ErrActionLinkUnusable, "this link is no longer valid", true
	case errors.Is(err, links.ErrLinkNotFound):
		return ErrActionLinkUnusable, "this link is not valid", true
	// A malformed token never named a link, so it is ordinary bad input and gets
	// a 400 rather than the 410 above.
	case errors.Is(err, links.ErrInvalidToken):
		return ErrValidatingRequestInput, "invalid link", true
	// An operation nobody may read and an operation that does not exist are the
	// same answer on purpose. The read paths resolve an owner and return
	// ErrOperationNotFound for a row belonging to somebody else, so that an ID
	// somebody guessed cannot be confirmed as real by the status it comes back
	// with.
	case errors.Is(err, operations.ErrOperationNotFound):
		return ErrDataNotFound, "operation not found", true
	// A subscription refused for capacity is a retry-later, not a failure of the
	// request: the same subscription will be accepted when somebody disconnects.
	case errors.Is(err, operations.ErrTooManyWatchers):
		return ErrTooManyRequests, "too many concurrent operation subscriptions", true
	// The privacy-request sentinels, mapped for the same reasons as the
	// operations ones directly above: a subject asking after their own export or
	// erasure is a client, and "request not found" reaching them as a 500 tells
	// them the service is broken when the answer is that the ID is not one of
	// theirs.
	case errors.Is(err, dataprivacy.ErrRequestNotFound):
		return ErrDataNotFound, "privacy request not found", true
	// A conflict rather than a not-found: the request exists and the caller may
	// see it, it is simply not in the state the call needs. The remedy is a state
	// change somebody else makes — a confirmation arrives, an export finishes —
	// not a corrected request.
	case errors.Is(err, dataprivacy.ErrNotAwaitingConfirmation):
		return ErrResourceConflict, "privacy request is not awaiting confirmation", true
	case errors.Is(err, dataprivacy.ErrArtifactUnavailable):
		return ErrResourceConflict, "privacy request has no downloadable artifact", true
	case errors.Is(err, dataprivacy.ErrEmptySubjectID):
		return ErrValidatingRequestInput, "privacy request requires a subject", true
	case errors.Is(err, dataprivacy.ErrUnknownRequestType):
		return ErrValidatingRequestInput, "unknown privacy request type", true
	case errors.Is(err, idempotency.ErrInFlight):
		return ErrIdempotencyKeyInFlight, "a request with this idempotency key is already in progress", true
	case errors.Is(err, idempotency.ErrFingerprintMismatch):
		return ErrIdempotencyKeyReused, "this idempotency key was already used for a different request", true
	// A malformed key is ordinary bad input, not an idempotency outcome, so it
	// gets the input code and a 400 rather than one of the codes above.
	case errors.Is(err, idempotency.ErrKeyRequired),
		errors.Is(err, idempotency.ErrKeyTooLong),
		errors.Is(err, idempotency.ErrKeyInvalid):
		return ErrValidatingRequestInput, "invalid idempotency key", true
	// The three refusals a text index makes about the request rather than about
	// itself. Each gets the code its remedy needs — see ErrInvalidSearchCursor
	// and ErrSearchWindowExceeded on why two of them are not the general input
	// code. None of the messages names the backend or the ceiling: which engine
	// is behind the interface is not something a client should be branching on,
	// and the depth at which paging stops is an implementation detail that
	// changes with an index setting.
	case errors.Is(err, textsearch.ErrInvalidCursor):
		return ErrInvalidSearchCursor, "invalid search cursor", true
	case errors.Is(err, textsearch.ErrResultWindowExceeded):
		return ErrSearchWindowExceeded, "search pagination limit reached; narrow the query", true
	case errors.Is(err, textsearch.ErrEmptyQueryProvided):
		return ErrValidatingRequestInput, "search query must not be empty", true
	// The vector index's request-shaped refusals. A missing vector is the same
	// answer as a missing row, and the other two are a query the index cannot
	// evaluate: a zero-length embedding, or one of the wrong width for the index
	// it was sent to. The dimensions are not in the message — an index's width is
	// a property of the model behind it, and a caller that got it wrong has it in
	// their own request.
	//
	// The rest of the search sentinels are deliberately absent, and the audit
	// that left them out is worth stating. vectorsearch's ErrNilConfig,
	// ErrNilDatabaseClient, ErrInvalidMetric and ErrInvalidDimension, pgvector's
	// ErrInvalidIdentifier and ErrInvalidFilter, qdrant's ErrUnsupportedID, and
	// elasticsearch's ErrNotReady are all raised while wiring an index up or
	// building a query in code, never in answer to something a client sent. They
	// reach a client only through a service that shipped broken, and a 500 is the
	// honest answer to that. qdrant's ErrUnexpectedStatus is the one that is
	// neither: it is the index's own dependency misbehaving, which is a 500 for
	// the same reason every other unwrapped provider failure is one.
	//
	// That they are all provider-package sentinels is not a coincidence. This
	// mapper imports the packages it maps, and importing a provider would put
	// that vendor's client — the whole Elasticsearch transport stack, in the worst
	// case — into every binary that answers an error. The sentinels a client has
	// to act on therefore live beside the interface, where both transports can
	// reach them for the cost of the interface package.
	case errors.Is(err, vectorsearch.ErrNotFound):
		return ErrDataNotFound, "vector not found", true
	case errors.Is(err, vectorsearch.ErrEmptyEmbedding):
		return ErrValidatingRequestInput, "embedding must not be empty", true
	case errors.Is(err, vectorsearch.ErrDimensionMismatch):
		return ErrValidatingRequestInput, "embedding does not match the index dimension", true
	case errors.Is(err, platformerrors.ErrNilInputParameter),
		errors.Is(err, platformerrors.ErrEmptyInputParameter),
		errors.Is(err, platformerrors.ErrInvalidIDProvided),
		errors.Is(err, platformerrors.ErrEmptyInputProvided),
		errors.Is(err, platformerrors.ErrUnrecognizedInputValue):
		return ErrValidatingRequestInput, "invalid input", true
	default:
		return "", "", false
	}
}
