package grpc

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func TestSearchMappings(T *testing.T) {
	T.Parallel()

	T.Run("maps an exhausted result window to OutOfRange", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(textsearch.ErrResultWindowExceeded)
		must.True(t, ok)
		test.EqOp(t, codes.OutOfRange, code)
	})

	T.Run("an exhausted result window is not an invalid argument", func(t *testing.T) {
		t.Parallel()

		// The cursor was well-formed and this index issued it. What ran out was
		// the range, which is the distinction OutOfRange exists to draw.
		code, _ := PlatformMapper.Map(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, codes.InvalidArgument, code)
	})

	T.Run("an exhausted result window is neither a success nor a server fault", func(t *testing.T) {
		t.Parallel()

		// OK would tell the client it had seen the whole result set; Internal
		// would page an operator over a request the client can correct.
		code, _ := PlatformMapper.Map(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, codes.OK, code)
		test.NotEqOp(t, codes.Internal, code)
		test.NotEqOp(t, codes.Unknown, code)
	})

	T.Run("maps a cursor the index did not issue to InvalidArgument", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(textsearch.ErrInvalidCursor)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("maps an empty query to InvalidArgument", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(textsearch.ErrEmptyQueryProvided)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("the two pagination refusals keep different codes", func(t *testing.T) {
		t.Parallel()

		// Restarting pagination fixes one of them and does nothing for the other.
		invalid, _ := PlatformMapper.Map(textsearch.ErrInvalidCursor)
		exceeded, _ := PlatformMapper.Map(textsearch.ErrResultWindowExceeded)

		test.NotEqOp(t, invalid, exceeded)
	})

	T.Run("maps the vector index's request refusals", func(t *testing.T) {
		t.Parallel()

		notFound, ok := PlatformMapper.Map(vectorsearch.ErrNotFound)
		must.True(t, ok)
		test.EqOp(t, codes.NotFound, notFound)

		empty, ok := PlatformMapper.Map(vectorsearch.ErrEmptyEmbedding)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, empty)

		mismatch, ok := PlatformMapper.Map(vectorsearch.ErrDimensionMismatch)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, mismatch)
	})

	T.Run("leaves the construction-time sentinels unmapped", func(t *testing.T) {
		t.Parallel()

		// These are raised while wiring an index up, not while serving a request.
		// A client that sees one is talking to a service that shipped broken, and
		// the honest answer to that is the default the caller chose — not a code
		// suggesting the request was theirs to fix.
		for _, err := range []error{
			vectorsearch.ErrNilConfig,
			vectorsearch.ErrNilDatabaseClient,
			vectorsearch.ErrInvalidMetric,
			vectorsearch.ErrInvalidDimension,
		} {
			t.Run(err.Error(), func(t *testing.T) {
				t.Parallel()

				_, ok := PlatformMapper.Map(err)
				test.False(t, ok)
				test.EqOp(t, codes.Internal, MapToGRPC(err, codes.Internal))
			})
		}
	})

	T.Run("maps wrapped sentinels too", func(t *testing.T) {
		t.Parallel()

		// The mapper is reached from handlers, which wrap.
		test.EqOp(t, codes.OutOfRange,
			MapToGRPC(platformerrors.Wrap(textsearch.ErrResultWindowExceeded, "paginating beyond the result window"), codes.Unknown))
		test.EqOp(t, codes.InvalidArgument,
			MapToGRPC(platformerrors.Wrap(textsearch.ErrInvalidCursor, "decoding cursor"), codes.Unknown))
	})
}
