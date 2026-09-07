package grpc

import (
	"sync"

	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/tracing"

	"google.golang.org/grpc/codes"
)

// GRPCErrorMapper maps domain errors to gRPC codes. ok=false means no match.
type GRPCErrorMapper interface {
	Map(err error) (code codes.Code, ok bool)
}

var (
	domainMappers   []GRPCErrorMapper
	domainMappersMu sync.RWMutex
)

// RegisterGRPCErrorMapper registers a domain-specific error mapper, which
// MapToGRPC consults after PlatformMapper and in registration order.
//
// A package that owns sentinels declares the mapper — dataprivacy.GRPCMapper and
// its three counterparts in this module are the pattern — and the composition
// root registers it. For this module's four that is one call, errormappers.Register,
// which service.Register makes for a service built from a service.Config and a
// service assembled by hand makes itself; this function is what a consumer calls
// for a mapper of its own. Doing it from an init function is a consumer's choice
// to make and not this module's, because a mapper that installs itself by being
// linked in is a side effect nothing downstream can opt out of.
func RegisterGRPCErrorMapper(m GRPCErrorMapper) {
	domainMappersMu.Lock()
	defer domainMappersMu.Unlock()
	domainMappers = append(domainMappers, m)
}

// PrepareAndLogGRPCStatus derives the gRPC code via MapToGRPC, then logs, traces,
// and returns a status error. defaultCode is the fallback for an error no mapper
// claims.
//
// This is the spelling a handler holding an observability.Operation wants:
// observability's function of the same name takes the code it is handed and
// cannot map, because this package imports it and the reverse edge is a cycle.
// Logger and Span are on Operation so that reaching this one costs nothing:
//
//	grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "doing the thing")
func PrepareAndLogGRPCStatus(err error, logger logging.Logger, span tracing.Span, defaultCode codes.Code, descriptionFmt string, descriptionArgs ...any) error {
	code := MapToGRPC(err, defaultCode)
	return observability.PrepareAndLogGRPCStatus(err, logger, span, code, descriptionFmt, descriptionArgs...)
}

// MapToGRPC returns the appropriate gRPC code for known sentinel errors.
// It tries PlatformMapper first, then each registered domain mapper.
// Use std errors.Is for matching. Returns defaultCode if no match.
func MapToGRPC(err error, defaultCode codes.Code) codes.Code {
	if err == nil {
		return codes.OK
	}
	if c, ok := PlatformMapper.Map(err); ok {
		return c
	}
	domainMappersMu.RLock()
	mappers := domainMappers
	domainMappersMu.RUnlock()
	for _, mapper := range mappers {
		if c, ok := mapper.Map(err); ok {
			return c
		}
	}
	return defaultCode
}
