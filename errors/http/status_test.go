package http

import (
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/shoenig/test"
)

func TestHTTPStatusForCode(T *testing.T) {
	T.Parallel()

	cases := map[ErrorCode]int{
		ErrFetchingSessionContextData: http.StatusUnauthorized,
		ErrDecodingRequestInput:       http.StatusBadRequest,
		ErrValidatingRequestInput:     http.StatusBadRequest,
		ErrDataNotFound:               http.StatusNotFound,
		ErrMisbehavingDependency:      http.StatusBadGateway,
		ErrUserIsBanned:               http.StatusForbidden,
		ErrUserIsNotAuthorized:        http.StatusForbidden,
		ErrCircuitBroken:              http.StatusServiceUnavailable,
		// unmapped / server-side codes fall back to 500.
		ErrNothingSpecific:         http.StatusInternalServerError,
		ErrTalkingToDatabase:       http.StatusInternalServerError,
		ErrTalkingToSearchProvider: http.StatusInternalServerError,
		ErrSecretGeneration:        http.StatusInternalServerError,
		ErrEncryptionIssue:         http.StatusInternalServerError,
	}

	for code, expected := range cases {
		T.Run(string(code), func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, expected, HTTPStatusForCode(code))
		})
	}

	T.Run("unknown code falls back to 500", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, http.StatusInternalServerError, HTTPStatusForCode("E_TOTALLY_UNKNOWN"))
	})
}

func TestToAPIResponse(T *testing.T) {
	T.Parallel()

	T.Run("nil error resolves to 200 with empty envelope", func(t *testing.T) {
		t.Parallel()

		status, resp := ToAPIResponse(nil)
		test.EqOp(t, http.StatusOK, status)
		test.NotNil(t, resp)
		test.Nil(t, resp.Error)
	})

	T.Run("known platform error maps to its status and envelope", func(t *testing.T) {
		t.Parallel()

		status, resp := ToAPIResponse(sql.ErrNoRows)
		test.EqOp(t, http.StatusNotFound, status)
		test.NotNil(t, resp.Error)
		test.EqOp(t, ErrDataNotFound, resp.Error.Code)
		test.EqOp(t, "data not found", resp.Error.Message)
	})

	T.Run("unknown error falls back to 500 and neutral code", func(t *testing.T) {
		t.Parallel()

		status, resp := ToAPIResponse(errors.New("totally unknown error that no mapper handles"))
		test.EqOp(t, http.StatusInternalServerError, status)
		test.NotNil(t, resp.Error)
		test.EqOp(t, ErrNothingSpecific, resp.Error.Code)
		test.EqOp(t, "an error occurred", resp.Error.Message)
	})
}
