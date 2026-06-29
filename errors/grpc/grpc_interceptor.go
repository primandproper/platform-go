package grpc

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v2/errors"

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
		msg := err.Error()

		// If already a status.Error, preserve code and details.
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
		msg := err.Error()

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
