package http

import (
	"net/http"
)

// codeToStatus maps a known ErrorCode to the HTTP status code it should produce.
// Codes not present here fall back to http.StatusInternalServerError, which keeps
// unknown or server-side failures from leaking as anything other than a 500.
var codeToStatus = map[ErrorCode]int{
	ErrFetchingSessionContextData: http.StatusUnauthorized,       // E101
	ErrDecodingRequestInput:       http.StatusBadRequest,         // E102
	ErrValidatingRequestInput:     http.StatusBadRequest,         // E103
	ErrDataNotFound:               http.StatusNotFound,           // E104
	ErrMisbehavingDependency:      http.StatusBadGateway,         // E106
	ErrUserIsBanned:               http.StatusForbidden,          // E109
	ErrUserIsNotAuthorized:        http.StatusForbidden,          // E110
	ErrCircuitBroken:              http.StatusServiceUnavailable, // E112
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
