package grpc

import (
	"context"
	stderrors "errors"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/links"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/cockroachdb/errors/errorspb"
	gogoproto "github.com/gogo/protobuf/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

const encodedErrorTypeURL = "type.googleapis.com/cockroach.errorspb.EncodedError"

// DecodeErrorFromStatus extracts the EncodedError from gRPC status details (if present)
// and decodes it so errors.Is() works across the wire. Returns the decoded error, or the
// original status error if no encoded detail is found.
func DecodeErrorFromStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	for _, detail := range st.Details() {
		if anyDetail, isAny := detail.(*anypb.Any); isAny && anyDetail != nil && anyDetail.TypeUrl == encodedErrorTypeURL {
			var enc errorspb.EncodedError
			if unmarshalErr := gogoproto.Unmarshal(anyDetail.Value, &enc); unmarshalErr != nil {
				continue
			}
			if decoded := platformerrors.DecodeError(ctx, enc); decoded != nil {
				return decoded
			}
		}
	}
	return err
}

// encodeErrorToDetails adds the platform-encoded error to status details for wire transmission.
// Uses gogo/protobuf for cockroachdb/errors EncodedError; wraps in anypb for gRPC compatibility.
func encodeErrorToDetails(ctx context.Context, err error) *anypb.Any {
	encoded := platformerrors.EncodeError(ctx, err)
	enc := &encoded
	if enc.GetLeaf() == nil && enc.GetWrapper() == nil {
		return nil
	}
	marshaled, marshalErr := gogoproto.Marshal(enc)
	if marshalErr != nil {
		return nil
	}
	return &anypb.Any{
		TypeUrl: encodedErrorTypeURL,
		Value:   marshaled,
	}
}

// clientMessage returns the status message a client sees.
//
// It is derived from the gRPC code, not from err.Error(). A handler error's text
// is the whole wrapped chain — table names, connection strings, the specific
// permission that was missing — and this package's own sentinels are documented
// as deliberately generic precisely because their message reaches clients
// verbatim. Putting arbitrary internal text on that same channel contradicted
// the stance the package states about itself.
//
// The full error still crosses the wire in the status *details*, encoded, which
// is what DecodeErrorFromStatus reads to keep errors.Is working between
// services. That detail is for trusted service-to-service callers; do not expose
// an interceptor-wrapped server directly to untrusted clients without stripping
// it at the edge.
func clientMessage(code codes.Code, err error) string {
	// Platform sentinels are written to be client-safe, so their own text is
	// better than a generic string — it tells the caller what to do differently.
	for _, sentinel := range clientSafeSentinels {
		if stderrors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}

	return code.String()
}

// clientSafeSentinels are the platform errors whose messages are documented as
// safe to return verbatim.
var clientSafeSentinels = []error{
	platformerrors.ErrPermissionDenied,
	ratelimiting.ErrRateLimited,
	// Both entitlement sentinels. Their messages name no feature and no limit —
	// "not entitled" and "quota exhausted" — and both codes they map to are
	// otherwise ambiguous enough that a client cannot tell which of two very
	// different remedies applies.
	platformerrors.ErrNotEntitled,
	platformerrors.ErrQuotaExhausted,
	// Both signature sentinels: neither says anything about the key, and the
	// stale one names clock skew, which is the difference between a caller that
	// can fix itself and one that files a ticket.
	requestsigning.ErrStaleSignature,
	requestsigning.ErrInvalidSignature,
	// The four redemption outcomes. The links package separates them on purpose
	// — a 256-bit token is never guessed, so naming the outcome is not an oracle
	// — and that reasoning does not stop at the transport. Without these a gRPC
	// client is told "FailedPrecondition" for all four, which is the one thing
	// the separation exists to avoid, while an HTTP client is told which.
	links.ErrLinkNotFound,
	links.ErrLinkAlreadyRedeemed,
	links.ErrLinkExpired,
	links.ErrLinkRevoked,
	links.ErrInvalidToken,
	platformerrors.ErrNilInputParameter,
	platformerrors.ErrEmptyInputParameter,
	platformerrors.ErrInvalidIDProvided,
	platformerrors.ErrEmptyInputProvided,
	platformerrors.ErrUnrecognizedInputValue,
}

// UnaryErrorEncodingInterceptor returns a unary interceptor that encodes handler
// errors into gRPC status details for wire transmission.
// Handlers should return errors (optionally wrapped); the interceptor will
// derive the gRPC code via MapToGRPC and attach the encoded error to details.
func UnaryErrorEncodingInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}

		code := MapToGRPC(err, codes.Unknown)

		// An error the handler already shaped as a status carries a message the
		// handler chose to expose; anything else gets a code-derived one.
		msg := clientMessage(code, err)
		if st, ok := status.FromError(err); ok {
			code = MapToGRPC(err, st.Code())
			msg = st.Message()
		}

		st := status.New(code, msg)
		if detail := encodeErrorToDetails(ctx, err); detail != nil {
			if stWithDetails, withDetailsErr := st.WithDetails(detail); withDetailsErr == nil {
				st = stWithDetails
			}
		}
		return nil, st.Err()
	}
}

// StreamErrorEncodingInterceptor returns a stream interceptor that encodes
// handler errors into gRPC status details for wire transmission.
func StreamErrorEncodingInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		err := handler(srv, ss)
		if err == nil {
			return nil
		}

		code := MapToGRPC(err, codes.Unknown)

		// An error the handler already shaped as a status carries a message the
		// handler chose to expose; anything else gets a code-derived one.
		msg := clientMessage(code, err)
		if st, ok := status.FromError(err); ok {
			code = MapToGRPC(err, st.Code())
			msg = st.Message()
		}

		st := status.New(code, msg)
		if detail := encodeErrorToDetails(ss.Context(), err); detail != nil {
			if stWithDetails, withDetailsErr := st.WithDetails(detail); withDetailsErr == nil {
				st = stWithDetails
			}
		}
		return st.Err()
	}
}
