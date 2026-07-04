package http

import (
	"sync"
)

// HTTPErrorMapper maps domain errors to (ErrorCode, message). ok=false means no match.
type HTTPErrorMapper interface {
	Map(err error) (code ErrorCode, msg string, ok bool)
}

var (
	domainMappers   []HTTPErrorMapper
	domainMappersMu sync.RWMutex
)

// RegisterHTTPErrorMapper registers a domain-specific error mapper.
// Domains call this from init() to contribute their error mappings.
func RegisterHTTPErrorMapper(m HTTPErrorMapper) {
	domainMappersMu.Lock()
	defer domainMappersMu.Unlock()
	domainMappers = append(domainMappers, m)
}

// ToAPIError maps known sentinel errors to ErrorCode and a safe user-facing message.
// It tries PlatformMapper first, then each registered domain mapper.
// Returns (code, message). Unknown errors fall back to the neutral ErrNothingSpecific and
// "an error occurred" — never a domain-specific code (e.g. a payment panic must not report "database").
func ToAPIError(err error) (code ErrorCode, msg string) {
	if err == nil {
		return ErrNothingSpecific, ""
	}
	if c, m, ok := PlatformMapper.Map(err); ok {
		return c, m
	}
	domainMappersMu.RLock()
	mappers := domainMappers
	domainMappersMu.RUnlock()
	for _, mapper := range mappers {
		if c, m, ok := mapper.Map(err); ok {
			return c, m
		}
	}
	return ErrNothingSpecific, "an error occurred"
}
