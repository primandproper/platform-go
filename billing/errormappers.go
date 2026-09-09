package billing

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two are primitives. They may know about database, ratelimiting and the
// platform sentinels, which are primitives too, and nothing above them — so the
// switch that decides what a refused charge means on the wire lives beside the
// error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose: the nil- and
// empty-argument ones wrap platform sentinels the platform mappers already
// answer. internal/sentinelmatrix records which sentinel is in which of the
// three states, and fails when one is in none.
//
// Every case below is reachable from a consumer's own handler over Store as well
// as from billing/grpc, which is why the mapping covers the whole package rather
// than the eighteen RPCs' subset of it. Seven of this package's writes are
// deliberately not on the wire and their refusals still have to mean something:
// a redelivered webhook arriving at a consumer's receiver is answered by
// ErrTransactionExists whether that receiver speaks HTTP, gRPC or neither.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one refusal with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the mappers.
//
// They are the three families where the code alone does not say what to do
// next, and where the sentence does.
//
// Seven refusals share codes.InvalidArgument and each names a different field —
// a currency that is not three characters, an amount below zero, a recurrence on
// a product that does not recur. The description a handler passes says which
// call failed ("creating a product"), not which field, so without these a caller
// correcting a form is told "InvalidArgument" seven ways.
//
// Five share codes.AlreadyExists and name two genuinely different mistakes: four
// are a payment provider's identifier arriving twice, which is the ordinary
// redelivery and is acknowledged, and [ErrIDTaken] is an application handing out
// an identifier it has used before, which is a bug to fix. A caller that cannot
// tell them apart retries the one it should not.
//
// Two share codes.FailedPrecondition and are answers rather than failures:
// a status write that found the status already moved, and a completion of a
// purchase whose money already arrived. Both mean the work has been done, and a
// caller told only "FailedPrecondition" cannot distinguish that from a refusal
// it should escalate.
//
// None of them names a tenant, an account, a person or a row. The four
// not-found sentinels are deliberately absent: a handler's own description
// already says which noun it was reading, so the code carries the whole answer.
var ClientSafeSentinels = []error{
	ErrBackwardsPeriod,
	ErrInvalidKind,
	ErrInvalidStatus,
	ErrInvalidCurrency,
	ErrNegativeAmount,
	ErrUnexpectedBillingInterval,
	ErrAmbiguousTransaction,

	ErrProductExists,
	ErrSubscriptionExists,
	ErrPurchaseExists,
	ErrTransactionExists,
	ErrIDTaken,

	ErrStatusUnchanged,
	ErrAlreadyCompleted,
}

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// The four absences. Absent, archived and belonging to another scope are one
	// answer on each table, which is what keeps a read from being an enumeration
	// oracle over another tenant's catalog or ledger.
	case errors.Is(err, ErrProductNotFound):
		return httperrors.ErrDataNotFound, "no such product", true
	case errors.Is(err, ErrSubscriptionNotFound):
		return httperrors.ErrDataNotFound, "no such subscription", true
	case errors.Is(err, ErrPurchaseNotFound):
		return httperrors.ErrDataNotFound, "no such purchase", true
	case errors.Is(err, ErrTransactionNotFound):
		return httperrors.ErrDataNotFound, "no such transaction", true

	// The four redeliveries. A provider's identifier arriving twice is a
	// conflict rather than a server fault, and it is the answer a webhook
	// handler acknowledges the delivery on instead of retrying it forever.
	case errors.Is(err, ErrProductExists):
		return httperrors.ErrResourceConflict, "a product with that external id already exists", true
	case errors.Is(err, ErrSubscriptionExists):
		return httperrors.ErrResourceConflict, "a subscription with that external id already exists", true
	case errors.Is(err, ErrPurchaseExists):
		return httperrors.ErrResourceConflict, "a purchase with that external id already exists", true
	case errors.Is(err, ErrTransactionExists):
		return httperrors.ErrResourceConflict, "a transaction with that external id has already been recorded", true

	// Not a redelivery: an application that chose an id another row already
	// carries. Same code, different sentence, because the remedy is a code
	// change rather than an acknowledgement.
	case errors.Is(err, ErrIDTaken):
		return httperrors.ErrResourceConflict, "another row already has that id", true

	// The two replay answers. 409 rather than 200, because the caller asked for
	// a change and none was made; and rather than 500, because nothing is wrong.
	case errors.Is(err, ErrStatusUnchanged):
		return httperrors.ErrResourceConflict, ErrStatusUnchanged.Error(), true
	case errors.Is(err, ErrAlreadyCompleted):
		return httperrors.ErrResourceConflict, ErrAlreadyCompleted.Error(), true

	// The seven a caller can correct, each naming the field rather than the
	// call. Their own wording is already written for whoever is reading it, so
	// the sentinel supplies the message.
	case errors.Is(err, ErrInvalidKind):
		return httperrors.ErrValidatingRequestInput, "unrecognized product kind", true
	case errors.Is(err, ErrInvalidStatus):
		return httperrors.ErrValidatingRequestInput, "unrecognized status", true
	case errors.Is(err, ErrInvalidCurrency):
		return httperrors.ErrValidatingRequestInput, "currency must be a three-character ISO 4217 code", true
	case errors.Is(err, ErrNegativeAmount):
		return httperrors.ErrValidatingRequestInput, "amount may not be negative", true
	case errors.Is(err, ErrBackwardsPeriod):
		return httperrors.ErrValidatingRequestInput, ErrBackwardsPeriod.Error(), true
	case errors.Is(err, ErrUnexpectedBillingInterval):
		return httperrors.ErrValidatingRequestInput, ErrUnexpectedBillingInterval.Error(), true
	case errors.Is(err, ErrAmbiguousTransaction):
		return httperrors.ErrValidatingRequestInput, ErrAmbiguousTransaction.Error(), true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrProductNotFound), errors.Is(err, ErrSubscriptionNotFound),
		errors.Is(err, ErrPurchaseNotFound), errors.Is(err, ErrTransactionNotFound):
		return codes.NotFound, true

	// AlreadyExists rather than Aborted: every one of these is a collision on a
	// unique key, and that is the code gRPC reserves for exactly that.
	case errors.Is(err, ErrProductExists), errors.Is(err, ErrSubscriptionExists),
		errors.Is(err, ErrPurchaseExists), errors.Is(err, ErrTransactionExists),
		errors.Is(err, ErrIDTaken):
		return codes.AlreadyExists, true

	// FailedPrecondition rather than AlreadyExists: nothing collided. The row is
	// there and is already in the state the call asked for, which is a condition
	// on the row rather than a duplicate of it — and it is the code a client
	// library does not retry, which is the behavior a redelivered event wants.
	case errors.Is(err, ErrStatusUnchanged), errors.Is(err, ErrAlreadyCompleted):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrInvalidKind), errors.Is(err, ErrInvalidStatus),
		errors.Is(err, ErrInvalidCurrency), errors.Is(err, ErrNegativeAmount),
		errors.Is(err, ErrBackwardsPeriod), errors.Is(err, ErrUnexpectedBillingInterval),
		errors.Is(err, ErrAmbiguousTransaction):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
