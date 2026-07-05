package grpc

import (
	"sync"

	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

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

// RegisterGRPCErrorMapper registers a domain-specific error mapper.
// Domains call this from init() to contribute their error mappings.
func RegisterGRPCErrorMapper(m GRPCErrorMapper) {
	domainMappersMu.Lock()
	defer domainMappersMu.Unlock()
	domainMappers = append(domainMappers, m)
}

// PrepareAndLogGRPCStatus derives the gRPC code via MapToGRPC, then logs, traces, and returns
// a status error. Use defaultCode as the fallback for unknown errors.
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
