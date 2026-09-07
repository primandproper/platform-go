package grpc

import (
	"context"
	stderrors "errors"
	"reflect"
	"sync"

	"github.com/primandproper/platform-go/v14/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/ratelimiting"

	"github.com/cockroachdb/errors/errorspb"
	"github.com/cockroachdb/errors/markers"
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
// description is the one thing that may stand in for the code's name: the short
// account of what the handler was doing, which the handler wrote for this
// reader. The interceptors have none — they are looking at an error somebody
// else returned bare — and pass "". PrepareAndLogGRPCStatus has one, and passes
// it. Neither outranks a registered client-safe sentinel's own words.
//
// The full error still crosses the wire in the status *details*, encoded, which
// is what DecodeErrorFromStatus reads to keep errors.Is working between
// services. That detail is for trusted service-to-service callers; do not expose
// an interceptor-wrapped server directly to untrusted clients without stripping
// it at the edge.
func clientMessage(code codes.Code, err error, description string) string {
	if msg, ok := ClientSafeMessage(err); ok {
		return msg
	}

	if description != "" {
		return description
	}

	return code.String()
}

// ClientSafeMessage reports the words a client may be told for err: the text of
// the first client-safe sentinel in its chain, and false when there is none.
//
// "First" is a position in the chain, not in the lists. The chain is walked
// outermost-first — depth-first through a Join, in the order it was joined —
// and each node is compared against the client-safe sentinels, registered and
// platform alike; the first node that is one of them supplies the message. So a
// more specific wrapper outranks what it wraps: a domain sentinel declared as
// Wrap(platformerrors.ErrUnrecognizedInputValue, "...") and registered speaks
// with its own words, because the walk reaches it before it reaches the
// platform sentinel inside it. A bare platform sentinel, or a registered
// sentinel built with New, is unaffected — there is only one node to match.
//
// The interceptors consult it through clientMessage, for an error a handler
// returned bare, and so does PrepareAndLogGRPCStatus for the description a
// handler passed it — which is how identity/grpc and authentication/signin/grpc
// get this behavior without asking for it, and why neither has to say the word.
//
// It stays exported for the handler that builds its own status by hand, carrying
// a code the mappers would not pick or a message this package cannot guess. Such
// a handler still wants a registered sentinel's own words to win over its own
// description, since the sentinel is more specific and was registered precisely
// to be quoted. Without this it either re-implements the two lists or its
// clients read "FailedPrecondition" where a sentinel had something better to
// say.
func ClientSafeMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	registeredClientSafeMu.RLock()
	registered := registeredClientSafe
	registeredClientSafeMu.RUnlock()

	return firstClientSafeNode(err, registered, clientSafeSentinels)
}

// firstClientSafeNode walks err outermost-first and returns the message of the
// first node that is one of the sentinels in lists. The lists are consulted in
// the order given at every node, so at a single node that answers for more
// than one sentinel — a decoded error's Is matches by mark anywhere in what it
// carries — the earlier list wins, which is why the registered (domain) list is
// passed before the platform one.
func firstClientSafeNode(err error, lists ...[]error) (string, bool) {
	for _, list := range lists {
		for _, sentinel := range list {
			if nodeIs(err, sentinel) {
				return sentinel.Error(), true
			}
		}
	}

	// The walk is the unwrapping errors.As would do, done by hand so that each
	// node is inspected before what it wraps; asserting on the node itself is
	// the point, not an oversight.
	switch u := err.(type) { //nolint:errorlint // this is the unwrap step of a chain walk, not a match
	case interface{ Unwrap() error }:
		if next := u.Unwrap(); next != nil {
			return firstClientSafeNode(next, lists...)
		}
	case interface{ Unwrap() []error }:
		for _, next := range u.Unwrap() {
			if next == nil {
				continue
			}
			if msg, ok := firstClientSafeNode(next, lists...); ok {
				return msg, true
			}
		}
	}

	return "", false
}

// nodeIs is std errors.Is for a single node — identity, or the node's own Is
// method — with no unwrapping, so the caller decides the walk order. The
// comparability guard is the standard library's: comparing two interface
// values of one uncomparable dynamic type panics.
func nodeIs(node, sentinel error) bool {
	if reflect.TypeOf(sentinel).Comparable() && node == sentinel { //nolint:errorlint // one node, no unwrapping, by design
		return true
	}
	if x, ok := node.(interface{ Is(error) bool }); ok && x.Is(sentinel) {
		return true
	}
	return false
}

// clientSafeSentinels are the platform errors whose messages are documented as
// safe to return verbatim.
//
// It is the platform tier only, for the same reason PlatformMapper is: this
// package is a primitive and cannot import what is built on it. A domain whose
// sentinels are written to be read by a caller — links is the worked example,
// where four separate redemption outcomes exist precisely so that a person is
// told which one happened — hands them to RegisterClientSafeSentinels.
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
	platformerrors.ErrNilInputParameter,
	platformerrors.ErrEmptyInputParameter,
	platformerrors.ErrInvalidIDProvided,
	platformerrors.ErrEmptyInputProvided,
	platformerrors.ErrUnrecognizedInputValue,
}

var (
	registeredClientSafe   []error
	registeredClientSafeMu sync.RWMutex
)

// RegisterClientSafeSentinels records errors whose own message the interceptors
// may put on the wire verbatim, rather than the generic string a gRPC code
// renders as.
//
// It is the companion to RegisterGRPCErrorMapper and answers the other half of
// the same question: the mapper decides the status, this decides whether the
// status carries the sentinel's own words. Registering a mapper without these
// is the usual case — most sentinels describe the system rather than the caller
// — and a sentinel whose text names a table, a key, or a policy must not be
// registered here at all.
//
// This module's own sets are links.ClientSafeSentinels and
// identity.ClientSafeSentinels, and errormappers.Register hands both over
// alongside the five mappers, so a consumer registering the domain tier gets
// both halves in one call.
//
// It is additive and safe to call from more than one goroutine, and a sentinel
// registered twice costs a second comparison and nothing else.
func RegisterClientSafeSentinels(sentinels ...error) {
	registeredClientSafeMu.Lock()
	defer registeredClientSafeMu.Unlock()
	registeredClientSafe = append(registeredClientSafe, sentinels...)
}

// handlerStatus reports the status a handler shaped its error as, and false for
// an error nobody shaped.
//
// It finds the GRPCStatus implementer in the chain and asks it directly rather
// than calling status.FromError on err. FromError on a *wrapped* status rebuilds
// the status with err.Error() as its message, so one consumer interceptor doing
// fmt.Errorf("...: %w", err) between the handler and this one would put the
// whole internal chain on the wire as the message — the very text clientMessage
// exists to keep off it. The implementer's own status carries the message the
// handler chose, however many times it has been wrapped since. A nil status
// from the implementer is treated as not shaped, as FromError treats it.
func handlerStatus(err error) (*status.Status, bool) {
	var shaped interface{ GRPCStatus() *status.Status }
	if !stderrors.As(err, &shaped) {
		return nil, false
	}
	st := shaped.GRPCStatus()
	if st == nil {
		return nil, false
	}
	return st, true
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
		msg := clientMessage(code, err, "")
		if st, ok := handlerStatus(err); ok {
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
		msg := clientMessage(code, err, "")
		if st, ok := handlerStatus(err); ok {
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

// decodedError is a decoded sentinel chain that still answers as the status it
// arrived as.
//
// It exists because the two halves of "what went wrong" live in different places
// on the wire and a caller wants both. DecodeErrorFromStatus answers the first —
// it returns the sentinel chain out of the status details, so errors.Is matches
// — and in doing so returns a plain error, which status.Code then reports as
// codes.Unknown. A caller switching on the code and a caller comparing against a
// sentinel would each break the other's approach.
//
// So this carries the decoded error for errors.Is and errors.As, and the
// original status for status.FromError and status.Code. It is the same
// three-property shape authorization/grpc's denial and ratelimiting/grpc's
// refusal already use, applied to an error arriving rather than leaving.
type decodedError struct {
	decoded error
	status  *status.Status
}

func (e *decodedError) Error() string { return e.decoded.Error() }

func (e *decodedError) Unwrap() error { return e.decoded }

func (e *decodedError) GRPCStatus() *status.Status { return e.status }

// Is is what makes the standard library's errors.Is work on an error that has
// crossed a connection, and it is the whole reason this type is worth having
// rather than returning what DecodeErrorFromStatus returns.
//
// EncodeError and DecodeError round-trip an error's cockroachdb *mark* — its
// type name and message — and not the identity of the sentinel value. So a
// decoded ErrUserNotFound is a different pointer from the one the package
// declared, and std errors.Is walks the chain comparing pointers and finds
// nothing. cockroachdb's own matcher compares marks and finds it.
//
// errors.Is consults an Is method when a value in the chain has one, so
// delegating here means a caller writes the obvious thing:
//
//	if errors.Is(err, identity.ErrUsernameTaken) { ... }
//
// and it is true. Without this method that line compiles, reads correctly, is
// always false, and sends a registration down the "something went wrong" branch
// on a collision the server named precisely. Telling callers to reach for a
// different matcher was the alternative, and a rule that has to be remembered at
// every call site is not a rule.
//
// The limitation, stated plainly: a mark is a type chain plus a message, not an
// identity. Two sentinels declared with identical wording in different packages
// carry the same mark, are indistinguishable after a round trip, and errors.Is
// on what came back answers true for both of them. The module therefore keeps
// its sentinel messages unique across packages, and internal/sentinelmatrix
// enforces that rather than leaving it to review.
func (e *decodedError) Is(target error) bool { return markers.Is(e.decoded, target) }

// UnaryErrorDecodingInterceptor is DecodeErrorFromStatus as a client
// interceptor, so a caller gets sentinels back without remembering to ask.
//
// DecodeErrorFromStatus on its own is a function every call site has to wrap its
// result in, and the one that forgets gets a *status.Error that no errors.Is
// matches — which reads exactly like a server that failed to encode, and is why
// the encoding side has been an interceptor from the start and this side was
// not. The two are now symmetric: the server encodes on the way out, the client
// decodes on the way in, and the sentinel a store returned is the sentinel the
// caller compares against.
//
// The error it returns answers to both idioms — see decodedError — because
// making the decode automatic would otherwise silently break every caller that
// reads status.Code, which is the more common of the two and the one nobody
// would think to re-check after installing an interceptor.
//
// Std errors.Is works on what it returns, which is not free — see decodedError's
// Is method for why it takes one, and what the alternative silently cost.
//
// Unary only, and that is narrower than the encoding side: the server has
// StreamErrorEncodingInterceptor, so a streaming RPC's error does cross the
// wire encoded, but nothing on the client decodes it yet. A client of a
// streaming RPC therefore gets a *status.Error that std errors.Is does not
// match, and DecodeErrorFromStatus is what such a client calls by hand today.
func UnaryErrorDecodingInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err == nil {
			return nil
		}

		st, ok := status.FromError(err)
		if !ok {
			return err
		}

		decoded := DecodeErrorFromStatus(ctx, err)
		if decoded == nil || stderrors.Is(decoded, err) {
			// Nothing was encoded in the details, so the status error is
			// already the best answer and wrapping it would only hide it.
			return err
		}

		return &decodedError{decoded: decoded, status: st}
	}
}
