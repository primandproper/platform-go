package fake

import (
	"testing"

	"github.com/primandproper/platform-go/v13/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestBuildFakePage(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		actual := BuildFakePage(func() *recordChild {
			return BuildFakeRecord[recordChild]()
		})

		must.NotNil(t, actual)
		test.SliceLen(t, DefaultPageSize, actual.Data)
		test.NotEq(t, "", actual.Cursor)
		test.EqOp(t, uint64(DefaultPageSize), actual.TotalCount)
		test.EqOp(t, uint64(DefaultPageSize), actual.FilteredCount)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), actual.MaxResponseSize)

		// A fake page is one that was answered, so its counts read as counts
		// rather than as the absence of any.
		filtered, total, known := actual.Counts()

		test.True(t, known)
		test.EqOp(t, uint64(DefaultPageSize), filtered)
		test.EqOp(t, uint64(DefaultPageSize), total)

		// Every element comes from its own call, rather than from one value repeated.
		test.NotEqOp(t, actual.Data[0].ID, actual.Data[1].ID)
	})

	T.Run("of a given size", func(t *testing.T) {
		t.Parallel()

		actual := BuildFakePageOfSize(7, func() *recordChild {
			return BuildFakeRecord[recordChild]()
		})

		must.NotNil(t, actual)
		test.SliceLen(t, 7, actual.Data)
		test.EqOp(t, uint64(7), actual.TotalCount)
	})

	T.Run("of no elements", func(t *testing.T) {
		t.Parallel()

		actual := BuildFakePageOfSize(0, func() *recordChild {
			t.Error("the element builder was called for a page of none")

			return nil
		})

		must.NotNil(t, actual)
		test.SliceEmpty(t, actual.Data)
		test.EqOp(t, uint64(0), actual.TotalCount)

		// A page of none is a page that counted none, not one that could not
		// count. The two are the same zero on the field and different answers
		// from Counts.
		_, _, known := actual.Counts()
		test.True(t, known)
	})
}
