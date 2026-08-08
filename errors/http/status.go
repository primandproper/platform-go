package http

import (
	"net/http"
)

// codeToStatus maps a known ErrorCode to the HTTP status code it should produce.
// Codes not present here fall back to http.StatusInternalServerError, which keeps
// unknown or server-side failures from leaking as anything other than a 500.
var codeToStatus = map[ErrorCode]int{
	ErrFetchingSessionContextData: http.StatusUnauthorized,        // E101
	ErrDecodingRequestInput:       http.StatusBadRequest,          // E102
	ErrValidatingRequestInput:     http.StatusBadRequest,          // E103
	ErrDataNotFound:               http.StatusNotFound,            // E104
	ErrMisbehavingDependency:      http.StatusBadGateway,          // E106
	ErrUserIsBanned:               http.StatusForbidden,           // E109
	ErrUserIsNotAuthorized:        http.StatusForbidden,           // E110
	ErrCircuitBroken:              http.StatusServiceUnavailable,  // E112
	ErrIdempotencyKeyInFlight:     http.StatusConflict,            // E113
	ErrIdempotencyKeyReused:       http.StatusUnprocessableEntity, // E114
	ErrResourceConflict:           http.StatusConflict,            // E115
	ErrTooManyRequests:            http.StatusTooManyRequests,     // E116
	ErrInvalidRequestSignature:    http.StatusUnauthorized,        // E117
	// Both entitlement denials are 402. The status is unfashionable and it is
	// also the only one that says what is actually true: the request is
	// well-formed, the caller is authenticated and authorized, and the thing
	// standing between them and the response is money. A 403 would send them to
	// an administrator who cannot help, and a 429 would tell them to retry a
	// request that will fail identically for the rest of the month.
	ErrNotEntitled:    http.StatusPaymentRequired, // E118
	ErrQuotaExhausted: http.StatusPaymentRequired, // E119
}

// HTTPStatusForCode returns the HTTP status code that corresponds to an ErrorCode.
// Unmapped codes (including ErrNothingSpecific and any server-side failure such as
// ErrTalkingToDatabase, ErrTalkingToSearchProvider, ErrSecretGeneration, or
// ErrEncryptionIssue) resolve to http.StatusInternalServerError.
func HTTPStatusForCode(code ErrorCode) int {
	if status, ok := codeToStatus[code]; ok {
		return status
	}

	return http.StatusInternalServerError
}

// ToAPIResponse maps a handler error to the HTTP status and response envelope that
// should be sent to the client. It combines ToAPIError (error -> code + safe message)
// with HTTPStatusForCode (code -> status), so callers get everything needed to write a
// consistent error response in one call. A nil error resolves to 200 with an empty
// envelope, though callers typically only invoke this on a non-nil error.
func ToAPIResponse(err error) (int, *APIResponse[any]) {
	if err == nil {
		return http.StatusOK, &APIResponse[any]{}
	}

	code, msg := ToAPIError(err)

	return HTTPStatusForCode(code), NewAPIErrorResponse(msg, code, ResponseDetails{})
}
