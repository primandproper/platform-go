package audit

import (
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Suite is the audit surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "audit",
		Mounted: func(s conformance.Surfaces) bool { return s.Audit != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("reads", func(t *testing.T) {
		t.Parallel()
		reads(t, s)
	})
	t.Run("verification", func(t *testing.T) {
		t.Parallel()
		verification(t, s)
	})

	t.Run("an audited action lands in the acting session's chain", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)

		did := act(t, mine)

		entry := findEntry(t, mine, did)
		must.NotNil(t, entry, must.Sprint("the deployment recorded nothing for an action it audits"))

		test.EqOp(t, did.ResourceID, entry.GetResourceId())
	})

	// The assertion the surface exists to keep true, and the one whose failure
	// is silent. A predicate that lost its scope does not error; it widens.
	t.Run("an entry in another session's chain is not readable by id", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		theirs, _ := subject(t, s)

		must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
			must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

		// The neighbor learns their own entry's identifier the only way
		// anybody legitimately can: by reading their own chain. Nothing here
		// reaches past a client.
		neighbor := findEntry(t, theirs, act(t, theirs))
		must.NotNil(t, neighbor, must.Sprint("the neighbor's own action recorded nothing to hide"))

		// The positive control, and it is not ceremony. "The neighbor's entry
		// is absent" is also true of a read that reaches nothing at all, so a
		// scope resolver returning one fixed wrong answer for every caller
		// passes this test on the strength of being comprehensively broken.
		// Proving this caller reaches its own entry through the same client is
		// what makes the absence below mean confinement.
		ours := findEntry(t, mine, act(t, mine))
		must.NotNil(t, ours, must.Sprint("this caller's own action recorded nothing"))

		reachable, err := mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()),
			&auditpb.GetEntryRequest{EntryId: ours.GetId()})
		must.NoError(t, err, must.Sprint("this caller cannot read its own entry; the absence below proves nothing"))
		must.EqOp(t, ours.GetId(), reachable.GetEntry().GetId())

		_, err = mine.Surfaces.Audit.GetEntry(mine.Context(t.Context()),
			&auditpb.GetEntryRequest{EntryId: neighbor.GetId()})

		// Absent rather than refused, which is what it is from here: a refusal
		// would confirm the identifier names something.
		test.ErrorIs(t, err, audit.ErrEntryNotFound,
			test.Sprint("another session's entry was readable by identifier"))
	})

	t.Run("a listing holds this session's entries and not a neighbor's", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		theirs, _ := subject(t, s)

		ours, neighbor := act(t, mine), act(t, theirs)

		res, err := mine.Surfaces.Audit.ListEntries(mine.Context(t.Context()),
			&auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		// Presence and absence of two named resources, never a count: this
		// listing runs against a database the suite may not own.
		test.True(t, holdsResource(res.GetResults(), ours),
			test.Sprint("this session's own action was missing from its listing"))
		test.False(t, holdsResource(res.GetResults(), neighbor),
			test.Sprint("another session's entry reached this listing"))
	})

	// A consumer's own suite found this one first, and it is the sharper half
	// of the previous assertion: the actor is a query field but the scope is
	// not, so naming a neighbor's actor searches this session's chain for
	// entries that were never written to it.
	t.Run("querying a neighbor's actor searches this session's chain", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		theirs, _ := subject(t, s)

		ours, neighbor := act(t, mine), act(t, theirs)

		if ours.ActorID == "" || neighbor.ActorID == "" {
			t.Skip("conformance: this subject's auditable action reports no actor, so there is no actor to query by")
		}

		// The positive control: the same query shape, naming this caller's own
		// actor, has to find something. Without it, a query that silently
		// matched nothing would satisfy the assertion below.
		found, err := mine.Surfaces.Audit.ListEntries(mine.Context(t.Context()),
			&auditpb.ListEntriesRequest{Query: &auditpb.EntryQuery{ActorId: ours.ActorID}})
		must.NoError(t, err)
		must.True(t, holdsResource(found.GetResults(), ours),
			must.Sprint("an actor query did not find this caller's own entry; the emptiness below proves nothing"))

		res, err := mine.Surfaces.Audit.ListEntries(mine.Context(t.Context()),
			&auditpb.ListEntriesRequest{Query: &auditpb.EntryQuery{ActorId: neighbor.ActorID}})

		// An empty answer rather than a refusal, and that is what makes the
		// confinement structural rather than checked.
		must.NoError(t, err)
		test.False(t, holdsResource(res.GetResults(), neighbor),
			test.Sprint("naming another session's actor returned that session's entries"))
	})

	t.Run("paging a listing drops nothing and repeats nothing", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)

		want := make([]*conformance.Audited, 0, 7)
		for range 7 {
			want = append(want, act(t, mine))
		}

		// Narrowed to one resource type so the walk is over rows these actions
		// produced, even where the chain holds others. Every action from one
		// subject records the same type, which is what makes that possible.
		seen := walk(t, mine, &auditpb.EntryQuery{ResourceType: want[0].ResourceType}, 3)

		for _, did := range want {
			test.MapContainsKey(t, seen, did.ResourceID,
				test.Sprintf("the entry for resource %s was dropped between pages", did.ResourceID))
		}
	})
}

// subject mints a caller together with the action it performs, skipping the
// test where the subject offers none.
func subject(t *testing.T, s *conformance.Session) (caller *conformance.Subject, act func(*testing.T, *conformance.Subject) *conformance.Audited) {
	t.Helper()

	auditable := s.Seams().Actions.Auditable
	s.NeedsAction(t, auditable != nil, "auditable")

	caller = s.Subject(t)

	return caller, func(t *testing.T, as *conformance.Subject) *conformance.Audited {
		t.Helper()

		did, err := auditable(as.Context(t.Context()), as.Scope)
		must.NoError(t, err, must.Sprint("performing an auditable action"))
		must.NotNil(t, did, must.Sprint("the auditable action reported nothing it touched"))
		must.StrNotEqFold(t, "", did.ResourceID, must.Sprint("the auditable action named no resource"))

		return did
	}
}

// findEntry reads the caller's own chain for the entry recording an action,
// returning nil when there is none.
//
// By resource rather than by identifier, because the identifier is the
// deployment's to assign and an action that reported one would be reporting a
// fact only the audit store knows.
func findEntry(t *testing.T, caller *conformance.Subject, did *conformance.Audited) *auditpb.Entry {
	t.Helper()

	res, err := caller.Surfaces.Audit.ListEntries(caller.Context(t.Context()),
		&auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{
				ResourceType: did.ResourceType,
				ResourceId:   did.ResourceID,
			},
		})
	must.NoError(t, err)

	for _, entry := range res.GetResults() {
		if entry.GetResourceId() == did.ResourceID {
			return entry
		}
	}

	return nil
}

// walk pages a listing to exhaustion, failing on a repeat, and returns the
// resource identifiers it saw.
//
// A repeat is the other half of what a cursor gets wrong, and it is the half a
// "did everything arrive" assertion cannot see — a page that resumes one row
// early delivers every row and delivers one of them twice.
func walk(t *testing.T, caller *conformance.Subject, query *auditpb.EntryQuery, size uint32) map[string]struct{} {
	t.Helper()

	seen := map[string]struct{}{}

	var cursor string

	// A bound rather than a bare loop: a cursor that fails to advance is a
	// test that hangs, and a hang reads as an unrelated timeout.
	for range 32 {
		res, err := caller.Surfaces.Audit.ListEntries(caller.Context(t.Context()),
			&auditpb.ListEntriesRequest{Query: query, Filter: filterFor(cursor, size)})
		must.NoError(t, err)

		for _, entry := range res.GetResults() {
			_, repeated := seen[entry.GetResourceId()]
			must.False(t, repeated,
				must.Sprintf("the entry for resource %s arrived on two pages", entry.GetResourceId()))

			seen[entry.GetResourceId()] = struct{}{}
		}

		next := res.GetPagination().GetCursor()
		if next == "" || len(res.GetResults()) == 0 {
			return seen
		}

		cursor = next
	}

	t.Fatal("the listing did not reach its end within the page bound; a cursor is not advancing")

	return seen
}

// filterFor is the page request, with the cursor left absent on the first call.
//
// Cursor is an optional scalar with explicit presence, so an empty string is a
// cursor rather than the absence of one — filtering to nothing on a store that
// takes it at its word.
func filterFor(cursor string, size uint32) *filteringpb.QueryFilter {
	filter := &filteringpb.QueryFilter{MaxResponseSize: &size}

	if cursor != "" {
		filter.Cursor = &cursor
	}

	return filter
}

func holdsResource(entries []*auditpb.Entry, did *conformance.Audited) bool {
	for _, entry := range entries {
		if entry.GetResourceId() == did.ResourceID {
			return true
		}
	}

	return false
}
