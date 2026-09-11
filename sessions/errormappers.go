package sessions

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two packages are primitives. They may know about database, ratelimiting
// and the platform sentinels, which are primitives too, and nothing above them
// — so the switch that decides a session error's status lives beside the
// session errors, and the import runs the other way from the one this file's
// sibling errors.go used to describe.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register,
// which service.Register makes for a service built from a service.Config and a
// service assembled by hand makes itself, next to the mappers it declares for
// its own sentinels.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one failure with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// Ordered before ErrNotFound, which it wraps. Every unusable session —
	// absent, forged, expired — resolves to the same status and a message that
	// says nothing about which: telling a client apart "no such session" from
	// "that one expired" is an oracle for whether a guessed identifier ever
	// existed. The two messages differ only in what a person reads.
	case errors.Is(err, ErrExpired):
		return httperrors.ErrFetchingSessionContextData, "session expired", true
	case errors.Is(err, ErrNotFound):
		return httperrors.ErrFetchingSessionContextData, "no active session", true
	default:
		return "", "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// Unauthenticated for every unusable session — absent, forged, expired.
	// ErrNotFound alone covers all of them, since ErrExpired and the two timeout
	// errors wrap it, and nothing is lost by collapsing them: gRPC has one code
	// for "we do not know who you are", and telling a client apart "no such
	// session" from "that one expired" is an oracle for whether a guessed
	// identifier ever existed. HTTPMapper splits the two only to vary the
	// message a person reads; the status is the same there too.
	case errors.Is(err, ErrNotFound):
		return codes.Unauthenticated, true
	default:
		return codes.Unknown, false
	}
}
