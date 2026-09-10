package mediaregistry_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// echoingStore answers a batched read with one object per id it was asked for,
// which is the whole-set case: what comes back is what went in, so anything the
// batching did to the set is visible in the answer.
func echoingStore() *mediaregistrymock.StoreMock {
	return &mediaregistrymock.StoreMock{
		ListObjectsByIDsFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			objectIDs []string,
		) ([]*mediaregistry.Object, error) {
			objects := make([]*mediaregistry.Object, 0, len(objectIDs))
			for _, id := range objectIDs {
				objects = append(objects, &mediaregistry.Object{ID: id, Scope: scope})
			}

			return objects, nil
		},
	}
}

// ids builds n ids that sort in the order they were generated, so an assertion
// about ordering can be made against the sequence rather than against a copy of
// the sorting.
func ids(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("obj_%06d", i))
	}

	return out
}

func TestListObjectsByIDsInBatches(T *testing.T) {
	T.Parallel()

	const scope = "acct_1"

	T.Run("a set that fits is the one statement it always was", func(t *testing.T) {
		t.Parallel()

		store := echoingStore()
		want := ids(mediaregistry.MaxObjectIDsPerRead)

		read, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), store, want)
		must.NoError(t, err)
		must.SliceLen(t, len(want), read)

		// Exactly at the limit, and still one call: the boundary belongs to the
		// store's refusal, which is > rather than >=, so the largest set the
		// store accepts is the largest one this hands over whole.
		calls := store.ListObjectsByIDsCalls()
		must.SliceLen(t, 1, calls)
		test.Eq(t, want, calls[0].ObjectIDs)
	})

	T.Run("a larger set is cut into batches and answers as one", func(t *testing.T) {
		t.Parallel()

		store := echoingStore()

		// Two full batches and a partial one, so the last chunk being short is
		// covered rather than assumed.
		want := ids(2*mediaregistry.MaxObjectIDsPerRead + 1)

		read, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), store, want)
		must.NoError(t, err)
		must.SliceLen(t, len(want), read)

		calls := store.ListObjectsByIDsCalls()
		must.SliceLen(t, 3, calls)
		test.SliceLen(t, mediaregistry.MaxObjectIDsPerRead, calls[0].ObjectIDs)
		test.SliceLen(t, mediaregistry.MaxObjectIDsPerRead, calls[1].ObjectIDs)
		test.SliceLen(t, 1, calls[2].ObjectIDs)

		// No batch may exceed what the store accepts, which is the whole point
		// of the cut.
		for _, call := range calls {
			test.LessEq(t, mediaregistry.MaxObjectIDsPerRead, len(call.ObjectIDs))
		}

		got := make([]string, 0, len(read))
		for _, object := range read {
			got = append(got, object.ID)
		}

		test.Eq(t, want, got, test.Sprint("the answer is in id order across batches, not within each one"))
	})

	T.Run("an unsorted set comes back in id order and the caller's slice is untouched", func(t *testing.T) {
		t.Parallel()

		store := echoingStore()

		want := ids(mediaregistry.MaxObjectIDsPerRead + 1)

		shuffled := slices.Clone(want)
		slices.Reverse(shuffled)

		handed := slices.Clone(shuffled)

		read, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), store, handed)
		must.NoError(t, err)
		must.SliceLen(t, len(want), read)

		got := make([]string, 0, len(read))
		for _, object := range read {
			got = append(got, object.ID)
		}

		// Ordered by id, which is what the single read promises and what a
		// concatenation of batches would otherwise have broken.
		test.Eq(t, want, got)

		// And the set the caller passed is the set the caller still holds. A
		// read that sorted its argument in place would be the same surprise as a
		// write that filled one in.
		test.Eq(t, shuffled, handed, test.Sprint("the caller's slice was reordered"))
	})

	T.Run("an id named twice is one row, as a single statement would have made it", func(t *testing.T) {
		t.Parallel()

		store := echoingStore()

		// Every id twice, so the set is over the limit only because of its
		// duplicates: without the dedupe this would be two batches, and with it
		// one, and the answer would carry each row twice either way.
		doubled := slices.Concat(ids(mediaregistry.MaxObjectIDsPerRead), ids(mediaregistry.MaxObjectIDsPerRead))

		read, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), store, doubled)
		must.NoError(t, err)
		must.SliceLen(t, mediaregistry.MaxObjectIDsPerRead, read)

		must.SliceLen(t, 1, store.ListObjectsByIDsCalls())
	})

	T.Run("a nil store is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		_, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), nil, ids(3))
		must.ErrorIs(t, err, mediaregistry.ErrNilStore)
	})

	T.Run("a batch that fails is reported rather than partially answered", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("reading the second batch")
		calls := 0

		store := &mediaregistrymock.StoreMock{
			ListObjectsByIDsFunc: func(
				_ context.Context,
				_ database.SQLQueryExecutor,
				_ tenancy.Scope,
				objectIDs []string,
			) ([]*mediaregistry.Object, error) {
				calls++
				if calls > 1 {
					return nil, boom
				}

				objects := make([]*mediaregistry.Object, 0, len(objectIDs))
				for _, id := range objectIDs {
					objects = append(objects, &mediaregistry.Object{ID: id})
				}

				return objects, nil
			},
		}

		// The rows the first batch did read are not handed back beside the
		// error. A partial answer here would be indistinguishable from the
		// partial answer this read makes on purpose — the ids that named
		// nothing — and the caller could not tell a short set from a failed one.
		read, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Of(scope), store, ids(2*mediaregistry.MaxObjectIDsPerRead))
		must.ErrorIs(t, err, boom)
		test.Nil(t, read)
	})
}

// TestListObjectsByIDsInBatches_RefusalIsTheStores pins that the free function
// adds no refusals of its own beyond the nil store.
//
// Everything else — the executor, the scope, the ids — is the store's to judge,
// and a second copy of those checks here would be a second place for them to
// drift from the ones the statement actually runs behind.
func TestListObjectsByIDsInBatches_RefusalIsTheStores(t *testing.T) {
	t.Parallel()

	refusal := platformerrors.New("the store's own refusal")

	store := &mediaregistrymock.StoreMock{
		ListObjectsByIDsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, []string,
		) ([]*mediaregistry.Object, error) {
			return nil, refusal
		},
	}

	// An unset scope, a nil executor and an empty set all reach the store rather
	// than being answered here.
	for name, set := range map[string][]string{
		"empty":     nil,
		"one":       ids(1),
		"batched":   ids(2 * mediaregistry.MaxObjectIDsPerRead),
		"unbatched": ids(mediaregistry.MaxObjectIDsPerRead),
	} {
		_, err := mediaregistry.ListObjectsByIDsInBatches(
			t.Context(), nil, tenancy.Scope{}, store, set)
		must.ErrorIs(t, err, refusal, must.Sprintf("the %s set", name))
	}
}
