package http

import (
	stderrors "errors"
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSearchMappings(T *testing.T) {
	T.Parallel()

	T.Run("maps a cursor the index did not issue to a 400 with its own code", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(textsearch.ErrInvalidCursor)

		test.EqOp(t, ErrInvalidSearchCursor, code)
		test.EqOp(t, "invalid search cursor", msg)
		test.EqOp(t, http.StatusBadRequest, HTTPStatusForCode(code))
	})

	T.Run("maps an exhausted result window to a 400 with its own code", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(textsearch.ErrResultWindowExceeded)

		test.EqOp(t, ErrSearchWindowExceeded, code)
		test.EqOp(t, "search pagination limit reached; narrow the query", msg)
		test.EqOp(t, http.StatusBadRequest, HTTPStatusForCode(code))
	})

	T.Run("neither pagination refusal is a 416", func(t *testing.T) {
		t.Parallel()

		// 416 is scoped to the Range header, and a response carrying it is
		// expected to carry Content-Range, which a cursor cannot supply.
		invalid, _ := ToAPIError(textsearch.ErrInvalidCursor)
		exceeded, _ := ToAPIError(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, http.StatusRequestedRangeNotSatisfiable, HTTPStatusForCode(invalid))
		test.NotEqOp(t, http.StatusRequestedRangeNotSatisfiable, HTTPStatusForCode(exceeded))
	})

	T.Run("the two pagination refusals keep their own codes", func(t *testing.T) {
		t.Parallel()

		// Same status, different code, because the remedies differ: one is
		// answered by restarting pagination and the other by narrowing the query.
		invalid, _ := ToAPIError(textsearch.ErrInvalidCursor)
		exceeded, _ := ToAPIError(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, invalid, exceeded)
		test.NotEqOp(t, ErrValidatingRequestInput, invalid)
		test.NotEqOp(t, ErrValidatingRequestInput, exceeded)
	})

	T.Run("an exhausted result window is not a server fault", func(t *testing.T) {
		t.Parallel()

		// ErrTalkingToSearchProvider is the 500 this used to fall through to, and
		// it says the index misbehaved when the index did exactly what it says on
		// the tin.
		code, _ := ToAPIError(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, ErrTalkingToSearchProvider, code)
		test.NotEqOp(t, ErrNothingSpecific, code)
		test.NotEqOp(t, http.StatusInternalServerError, HTTPStatusForCode(code))
	})

	T.Run("maps an empty query to a 400", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(textsearch.ErrEmptyQueryProvided)

		test.EqOp(t, ErrValidatingRequestInput, code)
		test.EqOp(t, "search query must not be empty", msg)
		test.EqOp(t, http.StatusBadRequest, HTTPStatusForCode(code))
	})

	T.Run("maps the vector index's request refusals", func(t *testing.T) {
		t.Parallel()

		notFound, _ := ToAPIError(vectorsearch.ErrNotFound)
		test.EqOp(t, ErrDataNotFound, notFound)
		test.EqOp(t, http.StatusNotFound, HTTPStatusForCode(notFound))

		empty, _ := ToAPIError(vectorsearch.ErrEmptyEmbedding)
		test.EqOp(t, ErrValidatingRequestInput, empty)

		mismatch, msg := ToAPIError(vectorsearch.ErrDimensionMismatch)
		test.EqOp(t, ErrValidatingRequestInput, mismatch)
		test.EqOp(t, "embedding does not match the index dimension", msg)
	})

	T.Run("leaves the construction-time sentinels unmapped", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			vectorsearch.ErrNilConfig,
			vectorsearch.ErrNilDatabaseClient,
			vectorsearch.ErrInvalidMetric,
			vectorsearch.ErrInvalidDimension,
		} {
			t.Run(err.Error(), func(t *testing.T) {
				t.Parallel()

				code, _, ok := PlatformMapper.Map(err)
				test.False(t, ok)
				test.EqOp(t, http.StatusInternalServerError, HTTPStatusForCode(code))
			})
		}
	})

	T.Run("says nothing about the backend or the ceiling", func(t *testing.T) {
		t.Parallel()

		// The message reaches the client verbatim. Which engine is behind the
		// interface is not something a client should be branching on, and the
		// depth at which paging stops moves with an index setting.
		_, msg := ToAPIError(platformerrors.Wrapf(textsearch.ErrResultWindowExceeded, "from %d + size %d exceeds elasticsearch max_result_window %d", 9900, 200, 10000))

		test.EqOp(t, "search pagination limit reached; narrow the query", msg)
		test.StrNotContains(t, msg, "elasticsearch")
		test.StrNotContains(t, msg, "10000")
	})

	T.Run("maps wrapped sentinels too", func(t *testing.T) {
		t.Parallel()

		code, _ := ToAPIError(platformerrors.Wrap(textsearch.ErrInvalidCursor, "decoding cursor"))

		test.EqOp(t, ErrInvalidSearchCursor, code)
	})

	T.Run("round-trips through the response envelope", func(t *testing.T) {
		t.Parallel()

		status, body := ToAPIResponse(textsearch.ErrResultWindowExceeded)

		must.NotNil(t, body)
		must.NotNil(t, body.Error)
		test.EqOp(t, http.StatusBadRequest, status)
		test.EqOp(t, ErrSearchWindowExceeded, body.Error.Code)
	})

	T.Run("round-trips back to the sentinels", func(t *testing.T) {
		t.Parallel()

		// What a typed client reads: the same sentinel a caller inside the
		// serving process would have gotten. Both codes come from exactly one
		// sentinel, so both round trips are lossless.
		test.True(t, stderrors.Is(ErrorForCode(ErrInvalidSearchCursor), textsearch.ErrInvalidCursor))
		test.True(t, stderrors.Is(ErrorForCode(ErrSearchWindowExceeded), textsearch.ErrResultWindowExceeded))
	})
}
