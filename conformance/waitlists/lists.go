package waitlists

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func lists(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a new list answers with the row as stored", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)

		// Whole seconds, because the closing time is compared exactly and the
		// weakest dialect keeps no more than that.
		closesAt := open()

		created, err := operator.Surfaces.Waitlists.CreateList(operator.Context(t.Context()), &waitlistspb.CreateListRequest{
			List: &waitlistspb.WaitlistInput{
				// The identifier on the input is ignored by a creation, so
				// "create" cannot write a row at an identifier a client chose.
				Id:          "chosen-by-the-client",
				Name:        "Launch",
				Description: "early access to the beta",
				ClosesAt:    timestamppb.New(closesAt),
			},
		})
		must.NoError(t, err)

		list := created.GetResult()
		must.NotNil(t, list)
		test.NotEqOp(t, "", list.GetId())
		test.NotEqOp(t, "chosen-by-the-client", list.GetId(),
			test.Sprint("a creation wrote the row at the identifier the client named"))
		test.EqOp(t, "Launch", list.GetName())
		test.EqOp(t, closesAt, list.GetClosesAt().AsTime())

		// The creation time is the database's, so a response echoing the
		// request would carry the epoch; and the two nullable times stay unset
		// rather than becoming 1970.
		test.False(t, list.GetCreatedAt().AsTime().IsZero(),
			test.Sprint("the list answered with no creation time, so the response is not the stored row"))
		test.Nil(t, list.GetLastUpdatedAt())
		test.Nil(t, list.GetArchivedAt())
	})

	t.Run("a creation that names no list is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)

		_, err := operator.Surfaces.Waitlists.CreateList(operator.Context(t.Context()), &waitlistspb.CreateListRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// There is no default closing time and no way to say "never": the column
	// is NOT NULL, and a list nobody decided the end of is refused rather than
	// given one.
	t.Run("a list with no closing time is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)

		_, err := operator.Surfaces.Waitlists.CreateList(operator.Context(t.Context()), &waitlistspb.CreateListRequest{
			List: &waitlistspb.WaitlistInput{Name: "Launch"},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a list read by id is scoped to the caller's tenant", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())

		// The positive control: without it, "the neighbor cannot read it" is
		// also true of a read that reaches nothing at all.
		read, err := mine.Surfaces.Waitlists.GetList(mine.Context(t.Context()),
			&waitlistspb.GetListRequest{ListId: list.GetId()})
		must.NoError(t, err, must.Sprint("this caller cannot read its own list; the absence below proves nothing"))
		test.EqOp(t, list.GetId(), read.GetResult().GetId())

		// Absent rather than forbidden: the scope is bound into the statement,
		// and a refusal would confirm the identifier names a list somewhere.
		_, err = theirs.Surfaces.Waitlists.GetList(theirs.Context(t.Context()),
			&waitlistspb.GetListRequest{ListId: list.GetId()})
		must.Error(t, err, must.Sprint("a neighboring tenant's list was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The console's catalog carries the closed lists as well as the open ones,
	// which is the whole difference between it and ListOpenLists.
	t.Run("the console's catalog is the caller's, open and closed alike", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		taking := openList(t, mine, open())
		stopped := openList(t, mine, closed())
		neighbors := openList(t, theirs, open())

		page, err := mine.Surfaces.Waitlists.ListLists(mine.Context(t.Context()), &waitlistspb.ListListsRequest{})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		ids := listIDs(page.GetResults())
		test.SliceContains(t, ids, taking.GetId(), test.Sprint("an open list was missing from its own tenant's catalog"))
		test.SliceContains(t, ids, stopped.GetId(), test.Sprint("a closed list was missing from the console's catalog"))
		test.SliceNotContains(t, ids, neighbors.GetId(), test.Sprint("a neighboring tenant's list reached this catalog"))
	})

	t.Run("the open catalog omits the lists that have stopped taking signups", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		taking := openList(t, operator, open())
		stopped := openList(t, operator, closed())

		ids := openListIDs(t, operator.Context(t.Context()), operator.Surfaces.Waitlists)
		test.SliceContains(t, ids, taking.GetId(), test.Sprint("an open list was missing from the open catalog"))
		test.SliceNotContains(t, ids, stopped.GetId(), test.Sprint("a closed list reached the open catalog"))
	})

	// A signed-in caller's tenant is their principal's rather than the
	// resolver's, which is what "the scope has two sources and each call has
	// exactly one" means on a public method.
	t.Run("the open catalog places a signed-in caller by their principal", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())

		test.SliceContains(t, openListIDs(t, mine.Context(t.Context()), mine.Surfaces.Waitlists), list.GetId(),
			test.Sprint("this caller's own open list was missing from its catalog; the absence below proves nothing"))
		test.SliceNotContains(t, openListIDs(t, theirs.Context(t.Context()), theirs.Surfaces.Waitlists), list.GetId(),
			test.Sprint("a neighboring tenant's open list reached a signed-in caller's catalog"))
	})

	// last_updated_at is the database's, and a response echoing the request
	// would say the row had not changed.
	t.Run("an update answers with the row as stored rather than as sent", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		moved := open().Add(24 * time.Hour)

		updated, err := operator.Surfaces.Waitlists.UpdateList(operator.Context(t.Context()), &waitlistspb.UpdateListRequest{
			List: &waitlistspb.WaitlistInput{
				Id:          list.GetId(),
				Name:        "Launch, extended",
				Description: list.GetDescription(),
				ClosesAt:    timestamppb.New(moved),
			},
		})
		must.NoError(t, err)
		test.EqOp(t, list.GetId(), updated.GetResult().GetId())
		test.EqOp(t, "Launch, extended", updated.GetResult().GetName())
		test.EqOp(t, moved, updated.GetResult().GetClosesAt().AsTime())
		test.NotNil(t, updated.GetResult().GetLastUpdatedAt(),
			test.Sprint("an update answered with no update time, so the response is not the stored row"))
	})

	t.Run("an update will not reach another tenant's list", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		list := openList(t, mine, open())

		_, err := theirs.Surfaces.Waitlists.UpdateList(theirs.Context(t.Context()), &waitlistspb.UpdateListRequest{
			List: &waitlistspb.WaitlistInput{
				Id:       list.GetId(),
				Name:     "somebody else's launch",
				ClosesAt: timestamppb.New(open()),
			},
		})
		must.Error(t, err, must.Sprint("a neighboring tenant's list was rewritten"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And the row is as its owner left it, which is the control: the owner
		// still reaches it, under the name it was opened with.
		read, err := mine.Surfaces.Waitlists.GetList(mine.Context(t.Context()),
			&waitlistspb.GetListRequest{ListId: list.GetId()})
		must.NoError(t, err)
		test.EqOp(t, list.GetName(), read.GetResult().GetName())
	})

	// The granted half of include_archived. An administrator holds the grant
	// that retires a list, which is the grant a deployment reads the archive
	// off, so the console's catalog asked for with the archive in it answers
	// with the retired list.
	t.Run("an administrator asking for retired lists receives them", func(t *testing.T) {
		t.Parallel()

		admin := s.Subject(t, conformance.AsAdmin())
		live := openList(t, admin, open())
		retired := openList(t, admin, open())

		ctx := admin.Context(t.Context())

		_, err := admin.Surfaces.Waitlists.ArchiveList(ctx, &waitlistspb.ArchiveListRequest{ListId: retired.GetId()})
		must.NoError(t, err)

		// The control: without asking, the retired list is not in the catalog.
		without, err := admin.Surfaces.Waitlists.ListLists(ctx, &waitlistspb.ListListsRequest{})
		must.NoError(t, err)
		test.SliceNotContains(t, listIDs(without.GetResults()), retired.GetId())

		include := true

		with, err := admin.Surfaces.Waitlists.ListLists(ctx,
			&waitlistspb.ListListsRequest{Filter: &filteringpb.QueryFilter{IncludeArchived: &include}})
		must.NoError(t, err)

		ids := listIDs(with.GetResults())
		test.SliceContains(t, ids, live.GetId())
		test.SliceContains(t, ids, retired.GetId(),
			test.Sprint("an administrator asked for retired lists and was answered without them"))
	})

	// Archiving takes the list out of every read that does not ask for
	// archived rows, so nobody can join it — and leaves the signups against it
	// alone, because archiving is not erasure.
	t.Run("an archived list takes no more signups and keeps the ones it has", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		list := openList(t, operator, open())
		kept := signedUp(t, operator, operator, list.GetId(), freshContact())
		ctx := operator.Context(t.Context())

		_, err := operator.Surfaces.Waitlists.ArchiveList(ctx, &waitlistspb.ArchiveListRequest{ListId: list.GetId()})
		must.NoError(t, err)

		// An archived list reads as one that is not there rather than as one
		// that is closed: the join's read of the list is a read of live rows.
		_, err = operator.Surfaces.Waitlists.Join(ctx,
			&waitlistspb.JoinRequest{ListId: list.GetId(), Contact: freshContact()})
		must.Error(t, err, must.Sprint("an archived list took a signup"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		read, err := operator.Surfaces.Waitlists.GetSignup(ctx,
			&waitlistspb.GetSignupRequest{ListId: list.GetId(), SignupId: kept.GetId()})
		must.NoError(t, err, must.Sprint("archiving a list took its signups with it"))
		test.EqOp(t, kept.GetId(), read.GetResult().GetId())
	})
}
