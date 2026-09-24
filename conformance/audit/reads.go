package audit

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func reads(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an entry read by id renders what the action recorded", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		did := act(t, mine)

		listed := findEntry(t, mine, did)
		must.NotNil(t, listed, must.Sprint("the deployment recorded nothing for an action it audits"))

		response, err := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()),
			&auditpb.GetEntryRequest{EntryId: listed.GetId()})
		must.NoError(t, err)

		entry := response.GetEntry()
		must.NotNil(t, entry)
		test.EqOp(t, listed.GetId(), entry.GetId())
		test.EqOp(t, did.ResourceType, entry.GetResourceType())
		test.EqOp(t, did.ResourceID, entry.GetResourceId())

		if did.ActorID != "" {
			test.EqOp(t, did.ActorID, entry.GetActor().GetId())
		}

		// The chain link is what makes this a log rather than a table, and a
		// rendering that dropped it would leave a client nothing to verify
		// against. No finer time comparison is made than "not the epoch",
		// because one dialect keeps whole seconds.
		test.NotEq(t, "", entry.GetHash(), test.Sprint("an entry was rendered without its hash"))
		must.NotNil(t, entry.GetRecordedAt(), must.Sprint("an entry was rendered without the time it was recorded"))
		test.Positive(t, entry.GetRecordedAt().AsTime().Unix())
	})

	// The sharper half of "another chain's entry is absent": absent in exactly
	// the way an identifier nobody ever wrote is absent. Were the two answers
	// to differ in anything but the identifier the client itself sent, the
	// difference would be an oracle for which identifiers belong to somebody.
	t.Run("a neighbor's entry reads exactly as an identifier nobody wrote", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		theirs, _ := subject(t, s)

		must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
			must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

		neighbor := findEntry(t, theirs, act(t, theirs))
		must.NotNil(t, neighbor, must.Sprint("the neighbor's own action recorded nothing to hide"))

		// The positive control, as everywhere here: this caller reaches its own
		// entry by id through the same client.
		ours := findEntry(t, mine, act(t, mine))
		must.NotNil(t, ours, must.Sprint("this caller's own action recorded nothing"))

		_, err := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()), &auditpb.GetEntryRequest{EntryId: ours.GetId()})
		must.NoError(t, err, must.Sprint("this caller cannot read its own entry; the absences below prove nothing"))

		nobodys := identifiers.New()

		_, hidden := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()),
			&auditpb.GetEntryRequest{EntryId: neighbor.GetId()})
		must.Error(t, hidden, must.Sprint("another session's entry was readable by identifier"))

		_, absent := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()),
			&auditpb.GetEntryRequest{EntryId: nobodys})
		must.Error(t, absent)

		test.EqOp(t, codes.NotFound, status.Code(hidden))
		test.EqOp(t, status.Code(absent), status.Code(hidden))
		test.EqOp(t,
			strings.ReplaceAll(status.Convert(absent).Message(), nobodys, "<id>"),
			strings.ReplaceAll(status.Convert(hidden).Message(), neighbor.GetId(), "<id>"),
			test.Sprint("another session's entry was refused differently from an identifier nobody wrote"))
	})

	t.Run("a read naming no entry is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()), &auditpb.GetEntryRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// A query is a narrowing of the caller's chain and never a widening of it.
	// No request field names a scope, so what this asserts is that the
	// narrowings there are leave alone the one the connection bound.
	t.Run("a query narrows within the caller's chain and never reaches past it", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		theirs, _ := subject(t, s)

		wanted, other, neighbor := act(t, mine), act(t, mine), act(t, theirs)

		// The positive control: a narrowing to one of this caller's resources
		// finds it, and finds nothing else of this caller's.
		narrowed, err := mine.Surfaces.Audit.ListEntries(mine.Context(t.Context()), &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceType: wanted.ResourceType, ResourceId: wanted.ResourceID},
		})
		must.NoError(t, err)
		must.True(t, holdsResource(narrowed.GetResults(), wanted),
			must.Sprint("a narrowing to this caller's own resource did not find it; the emptiness below proves nothing"))
		test.False(t, holdsResource(narrowed.GetResults(), other),
			test.Sprint("a narrowing to one resource answered with another"))

		// A narrowing that only a neighbor's entry matches finds nothing rather
		// than reaching across.
		across, err := mine.Surfaces.Audit.ListEntries(mine.Context(t.Context()), &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceType: neighbor.ResourceType, ResourceId: neighbor.ResourceID},
		})
		must.NoError(t, err)
		test.False(t, holdsResource(across.GetResults(), neighbor),
			test.Sprint("naming another session's resource returned that session's entry"))
	})
}
