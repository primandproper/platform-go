package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCreateList(T *testing.T) {
	T.Parallel()

	T.Run("opens a list and answers with the stored row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		closesAt := testNow.Add(720 * time.Hour)

		res, err := h.server.CreateList(h.ctx(t), &waitlistspb.CreateListRequest{
			List: listInput("Launch", closesAt),
		})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.NotEq(t, "", res.GetResult().GetId())
		test.EqOp(t, "Launch", res.GetResult().GetName())
		test.EqOp(t, closesAt.UTC(), res.GetResult().GetClosesAt().AsTime())

		// The database stamps the creation time, so a response echoing the
		// request would carry the epoch.
		test.False(t, res.GetResult().GetCreatedAt().AsTime().IsZero())

		// The two nullable times stay unset rather than becoming 1970.
		test.Nil(t, res.GetResult().GetLastUpdatedAt())
		test.Nil(t, res.GetResult().GetArchivedAt())
	})

	// The id on the input is dropped on a creation, so "create" cannot be used
	// to write a row at an identifier a client chose.
	T.Run("mints the identifier rather than taking the one the request named", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		input := listInput("Launch", testNow.Add(time.Hour))
		input.Id = "chosen-by-the-client"

		res, err := h.server.CreateList(h.ctx(t), &waitlistspb.CreateListRequest{List: input})
		must.NoError(t, err)

		test.NotEqOp(t, "chosen-by-the-client", res.GetResult().GetId())
	})

	T.Run("refuses a request that named no list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateList(h.ctx(t), &waitlistspb.CreateListRequest{})
		test.Nil(t, res)
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// The store refuses a list with no closing time and there is no default —
	// what reaches a client is the platform mapper's bad request, because the
	// sentinel wraps ErrEmptyInputParameter.
	T.Run("refuses a list with no closing time", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateList(h.ctx(t), &waitlistspb.CreateListRequest{
			List: &waitlistspb.WaitlistInput{Name: "Launch"},
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrEmptyClosesAt)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("refuses an anonymous caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateList(h.anonCtx(t), &waitlistspb.CreateListRequest{
			List: listInput("Launch", testNow.Add(time.Hour)),
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestGetList(T *testing.T) {
	T.Parallel()

	T.Run("reads one of the caller's lists", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		res, err := h.server.GetList(h.ctx(t), &waitlistspb.GetListRequest{ListId: list.ID})
		must.NoError(t, err)

		test.EqOp(t, list.ID, res.GetResult().GetId())
	})

	// The scope is bound into the statement rather than checked in front of it,
	// so another tenant's list is not there rather than forbidden.
	T.Run("answers another tenant's list as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		res, err := h.server.GetList(h.otherCtx(t), &waitlistspb.GetListRequest{ListId: list.ID})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrListNotFound)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

func TestListLists(T *testing.T) {
	T.Parallel()

	// The administrative page carries closed and archived lists as well, which
	// is the whole difference between it and ListOpenLists.
	T.Run("pages the caller's catalog, open and closed alike", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		open := h.seedOpenList(t, testScope)
		closed := h.seedList(t, testScope, testNow.Add(-time.Hour))

		res, err := h.server.ListLists(h.ctx(t), &waitlistspb.ListListsRequest{})
		must.NoError(t, err)

		test.SliceLen(t, 2, res.GetResults())
		test.SliceContains(t, idsOf(res.GetResults()), open.ID)
		test.SliceContains(t, idsOf(res.GetResults()), closed.ID)
	})

	T.Run("carries no other tenant's lists", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedOpenList(t, testScope)

		res, err := h.server.ListLists(h.otherCtx(t), &waitlistspb.ListListsRequest{})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})
}

func TestListOpenLists(T *testing.T) {
	T.Parallel()

	// The one read a caller reaches without a grant, and the reason the tenant
	// has a second source: the visitor has not signed in.
	T.Run("answers an anonymous caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		// The harness's resolver places an anonymous caller in testScope, which
		// is what a multi-tenant deployment's does; the GlobalScope default is
		// asserted in server_test.go.
		open := h.seedOpenList(t, testScope)

		res, err := h.server.ListOpenLists(h.anonCtx(t), &waitlistspb.ListOpenListsRequest{})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, open.ID, res.GetResults()[0].GetId())
	})

	T.Run("omits the closed lists", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		open := h.seedOpenList(t, testScope)
		h.seedList(t, testScope, testNow.Add(-time.Hour))

		res, err := h.server.ListOpenLists(h.ctx(t), &waitlistspb.ListOpenListsRequest{})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, open.ID, res.GetResults()[0].GetId())
	})

	// A signed-in caller's tenant is their principal's, not the resolver's,
	// which is what "the scope has two sources and each call has exactly one"
	// means on a public method.
	T.Run("places a signed-in caller by their principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedOpenList(t, testScope)

		res, err := h.server.ListOpenLists(h.otherCtx(t), &waitlistspb.ListOpenListsRequest{})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})
}

func TestUpdateList(T *testing.T) {
	T.Parallel()

	// The read is inside the transaction, which is what the store's reads taking
	// an executor rather than a reader is for: last_updated_at is the database's,
	// and a response echoing the request would say the row had not changed.
	T.Run("answers with the row as stored rather than as sent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		moved := testNow.Add(24 * time.Hour)

		res, err := h.server.UpdateList(h.ctx(t), &waitlistspb.UpdateListRequest{
			List: &waitlistspb.WaitlistInput{
				Id:          list.ID,
				Name:        "Launch, extended",
				Description: list.Description,
				ClosesAt:    timestamppb.New(moved),
			},
		})
		must.NoError(t, err)

		test.EqOp(t, "Launch, extended", res.GetResult().GetName())
		test.EqOp(t, moved.UTC(), res.GetResult().GetClosesAt().AsTime())
		must.NotNil(t, res.GetResult().GetLastUpdatedAt())
	})

	T.Run("will not reach another tenant's list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		res, err := h.server.UpdateList(h.otherCtx(t), &waitlistspb.UpdateListRequest{
			List: &waitlistspb.WaitlistInput{
				Id:       list.ID,
				Name:     "somebody else's launch",
				ClosesAt: timestamppb.New(testNow.Add(time.Hour)),
			},
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrListNotFound)
	})
}

func TestArchiveList(T *testing.T) {
	T.Parallel()

	// Archiving takes the list out of every read that does not ask for archived
	// rows, so nobody can join it — and leaves the signups against it alone,
	// because archiving is not erasure.
	T.Run("closes the list to new signups and keeps its signups", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		_, err := h.server.ArchiveList(h.ctx(t), &waitlistspb.ArchiveListRequest{ListId: list.ID})
		must.NoError(t, err)

		// An archived list reads as one that is not there rather than as one
		// that is closed, which is the store's own reading: the join's list read
		// is a read of live rows.
		joined, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "grace@example.com",
		})
		test.Nil(t, joined)
		must.Error(t, err)
		test.ErrorIs(t, err, waitlists.ErrListNotFound)

		read, err := h.server.GetSignup(h.ctx(t), &waitlistspb.GetSignupRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)
		test.EqOp(t, signup.ID, read.GetResult().GetId())
	})
}

func idsOf(lists []*waitlistspb.Waitlist) []string {
	out := make([]string, 0, len(lists))
	for _, l := range lists {
		out = append(out, l.GetId())
	}

	return out
}
