package observability

import (
	"fmt"

	"github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/tracing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PrepareAndLogError standardizes our error handling by logging, tracing, and formatting an error consistently.
func PrepareAndLogError(err error, logger logging.Logger, span tracing.Span, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if span != nil {
		tracing.AttachErrorToSpan(span, desc, err)
	}

	if logger != nil {
		logger.Error(desc, err)
	}

	if desc != "" {
		return errors.Wrap(err, desc)
	}
	return err
}

// PrepareError standardizes our error handling by logging, tracing, and formatting an error consistently.
func PrepareError(err error, span tracing.Span, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if span != nil {
		tracing.AttachErrorToSpan(span, desc, err)
	}

	if desc != "" {
		return errors.Wrap(err, desc)
	}
	return err
}

// unspecifiedErrorDescription stands in for a description a caller did not
// supply, so the error still reaches both pillars.
const unspecifiedErrorDescription = "unspecified error"

// AcknowledgeError standardizes our error handling by logging and tracing consistently.
//
// A nil error is nothing to acknowledge, and returns without emitting — matching
// PrepareError and PrepareAndLogError, which already return nil untouched.
//
// An empty description does not: this used to drop the error entirely, which
// made the one argument that is only ever decoration decide whether the error
// was reported at all.
func AcknowledgeError(err error, logger logging.Logger, span tracing.Span, descriptionFmt string, descriptionArgs ...any) {
	if err == nil {
		return
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if desc == "" {
		desc = unspecifiedErrorDescription
	}

	logging.EnsureLogger(logger).Error(desc, err)
	tracing.AttachErrorToSpan(span, desc, err)
}

// PrepareAndLogGRPCStatus logs and traces err, then returns it as a gRPC status
// error carrying code.
//
// code is used exactly as it was given. This function does not consult the
// registered error mappers and cannot: errors/grpc imports this package to reach
// this function, so the import that would let this one call MapToGRPC is a
// cycle.
//
// A caller who wants the registry's answer rather than the one they guessed
// wants errors/grpc.PrepareAndLogGRPCStatus, which is this signature with code
// renamed defaultCode: it runs MapToGRPC over the error and calls this with the
// result. A handler holding an Operation spells it
//
//	grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "doing the thing")
//
// and that is why Operation carries no GRPCStatus method. It carried one, it
// delegated here, and so it could not map either.
func PrepareAndLogGRPCStatus(err error, logger logging.Logger, span tracing.Span, code codes.Code, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)

	// The wrapping, the log line and the span event are PrepareAndLogError's,
	// spelled once there; what this adds is the status the chain travels under.
	return GRPCStatusError(PrepareAndLogError(err, logger, span, "%s", desc), code, desc)
}

// grpcStatusError is a handler failure that answers to both idioms: the sentinel
// chain for errors.Is, and a gRPC status for status.Code.
//
// It has to be both, and a *status.Error is only ever the second. Building one
// with status.Errorf(code, "%v", err) — which is what PrepareAndLogGRPCStatus
// used to return — renders the chain into the message and drops it. The sentinel
// is not merely unmatched afterwards, it is gone: nothing downstream can put
// back what it was handed as a string.
//
// Something downstream is meant to carry it.
// grpcerrors.UnaryErrorEncodingInterceptor exists to put the chain into the
// status details so a client's errors.Is matches across the wire, and it can
// only encode the chain it is given. A handler that flattened first hands it a
// leaf whose only content is a message, which is encoding that carries nothing.
//
// The message is the description the handler chose rather than the chain. The
// chain still crosses the wire, encoded in the details, for a client that
// decodes it — that detail is for trusted service-to-service callers, as
// grpcerrors documents. The message is what a client that reads nothing else
// sees, and keeping the chain out of it is why a table name a store put in its
// error does not reach that reader.
type grpcStatusError struct {
	err  error
	msg  string
	code codes.Code
}

func (e *grpcStatusError) Error() string { return e.err.Error() }

func (e *grpcStatusError) Unwrap() error { return e.err }

func (e *grpcStatusError) GRPCStatus() *status.Status { return status.New(e.code, e.msg) }

// GRPCStatusError pairs err with the status a client should be told about it,
// keeping err itself — sentinels, wrapping and all — reachable underneath.
//
// message is what the status says, and an empty one falls back to the code's own
// name, since a status with no message tells a client nothing at all.
//
// A handler with sentinels of its own wants grpcerrors.PrepareAndLogGRPCStatus
// instead: it maps the code through the registered mappers, which this package
// cannot reach, and lets a registered client-safe sentinel's own words outrank
// the description. This is the constructor underneath it, for a caller that has
// already decided both.
//
// The concrete type stays unexported deliberately. Each of its three methods is
// reached through the chain — errors.Is and errors.As for the first two,
// status.FromError for the third — so exporting the struct would add a name a
// caller could spell and none would need.
func GRPCStatusError(err error, code codes.Code, message string) error {
	if err == nil {
		return nil
	}

	if message == "" {
		message = code.String()
	}

	return &grpcStatusError{err: err, msg: message, code: code}
}
