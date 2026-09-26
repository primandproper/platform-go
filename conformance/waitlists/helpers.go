package waitlists

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	domain "github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxPages bounds a walk through a listing this suite does not own. A visitor's
// catalog is shared with everything else in the deployment, so the list an
// assertion is looking for may not be on the first page — and a walk with no
// bound is a test that never ends against a cursor that never does.
const maxPages = 100

// open is a closing time far enough off that nothing in a run outlives it.
func open() time.Time { return time.Now().UTC().Add(720 * time.Hour).Truncate(time.Second) }

// closed is a closing time already past. The store refuses no closing time, but
// accepts one that has gone by, which is how a list that has stopped taking
// signups is made without a clock the suite can move.
func closed() time.Time { return time.Now().UTC().Add(-time.Hour).Truncate(time.Second) }

// freshContact is an address nobody has used, so a signup found by it is the one
// this assertion made.
func freshContact() string { return identifiers.New() + "@conformance.invalid" }

// openList opens a list in the caller's tenant through the console.
func openList(t *testing.T, operator *conformance.Subject, closesAt time.Time) *waitlistspb.Waitlist {
	t.Helper()

	created, err := operator.Surfaces.Waitlists.CreateList(operator.Context(t.Context()), &waitlistspb.CreateListRequest{
		List: &waitlistspb.WaitlistInput{
			Name:        "conf_" + identifiers.New(),
			Description: "early access to the beta",
			ClosesAt:    timestamppb.New(closesAt),
		},
	})
	must.NoError(t, err, must.Sprint("opening a list"))
	must.NotNil(t, created.GetResult())

	return created.GetResult()
}

// join signs contact up to a list as caller, which is the signed-in half of the
// public form: the tenant and the subject both come off the caller's principal.
func join(t *testing.T, caller *conformance.Subject, listID, contact string) {
	t.Helper()

	_, err := caller.Surfaces.Waitlists.Join(caller.Context(t.Context()),
		&waitlistspb.JoinRequest{ListId: listID, Contact: contact})
	must.NoError(t, err, must.Sprintf("joining %q", contact))
}

// byContact reads a signup through the console by its address, or reports nil
// where there is no live row for it.
//
// It is what every assertion about Join reads back through, because Join says
// nothing about what it did: what the form wrote has to be asked of the half
// that may say.
func byContact(t *testing.T, operator *conformance.Subject, listID, contact string) *waitlistspb.Signup {
	t.Helper()

	found, err := operator.Surfaces.Waitlists.GetSignupByContact(operator.Context(t.Context()),
		&waitlistspb.GetSignupByContactRequest{ListId: listID, Contact: contact})
	if status.Code(err) == codes.NotFound {
		return nil
	}

	must.NoError(t, err, must.Sprintf("reading the signup for %q", contact))

	return found.GetResult()
}

// signedUp joins contact as caller and reads the row back through operator,
// failing if it is not there.
func signedUp(t *testing.T, caller, operator *conformance.Subject, listID, contact string) *waitlistspb.Signup {
	t.Helper()

	join(t, caller, listID, contact)

	signup := byContact(t, operator, listID, contact)
	must.NotNil(t, signup, must.Sprintf("a join for %q left no row the console can find", contact))

	return signup
}

// signupsOn pages one list's live signups through the console.
func signupsOn(t *testing.T, operator *conformance.Subject, listID string) []*waitlistspb.Signup {
	t.Helper()

	page, err := operator.Surfaces.Waitlists.ListSignups(operator.Context(t.Context()),
		&waitlistspb.ListSignupsRequest{ListId: listID})
	must.NoError(t, err)

	return page.GetResults()
}

// userSubject is the subject a signed-in caller's signups are attributed to.
func userSubject(caller *conformance.Subject) *waitlistspb.SignupSubject {
	return &waitlistspb.SignupSubject{Type: string(domain.SubjectUser), Id: caller.UserID}
}

// needsUser skips unless the subject surfaced the caller's user identifier,
// which every assertion about a signup's subject names.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier")
	}
}

// eraseSubject withdraws every signup a caller's user holds in the operator's
// tenant, through the console's erasure RPC.
//
// It is how this suite brings a withdrawn row about. The unsubscribe RPC asks
// the deployment's SignupAuthorizer, which has no default and whose answer a
// suite cannot assume; the erasure asks none, and it leaves the row in exactly
// the state a withdrawal does — which is what the assertions that use it are
// about.
func eraseSubject(t *testing.T, operator, of *conformance.Subject) int64 {
	t.Helper()

	erased, err := operator.Surfaces.Waitlists.WithdrawSignupsForSubject(operator.Context(t.Context()),
		&waitlistspb.WithdrawSignupsForSubjectRequest{Subject: userSubject(of)})
	must.NoError(t, err, must.Sprint("erasing a subject's signups"))

	return erased.GetWithdrawn()
}

// twoTenants mints two callers and refuses to proceed if the subject put them in
// one tenant, since every confinement assertion here would then compare a
// tenant with itself and pass.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.TwoTenants(t, surface)

	return mine, theirs
}

// colleague mints a second caller in of's tenant. A subject that cannot put two
// callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.InTenant(surface, of.ScopeFor(surface)))

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

// visitor is the public half as somebody on a signup page reaches it — a client
// on a connection carrying nobody — and an operator in the tenant that visitor
// lands in, to open lists there and read back what the visitor did.
//
// It skips, with the reason, where the subject supplies no anonymous connection,
// does not say where its visitors land, or cannot mint a caller there.
func visitor(t *testing.T, s *conformance.Session) (waitlistspb.WaitlistsServiceClient, *conformance.Subject) {
	t.Helper()

	seams := s.Seams()

	if seams.Anonymous == nil {
		t.Skip("conformance: this subject supplies no anonymous connection, so the signup page cannot be reached as a visitor")
	}

	if seams.VisitorScope == nil {
		t.Skip("conformance: this subject does not say which tenant a visitor lands in, so no list can be opened where one would find it")
	}

	conn, err := seams.Anonymous(t.Context())
	must.NoError(t, err, must.Sprint("opening a connection with nobody on it"))

	operator := s.Subject(t, conformance.InTenant(surface, *seams.VisitorScope))

	return waitlistspb.NewWaitlistsServiceClient(conn), operator
}

// openListIDs walks every page of the open catalog a client reaches from ctx.
//
// Every page rather than the first, because a visitor's catalog is shared with
// everything else in the deployment: the list an assertion is looking for need
// not be on the first page, and "absent from page one" is not "absent".
func openListIDs(t *testing.T, ctx context.Context, client waitlistspb.WaitlistsServiceClient) []string {
	t.Helper()

	var (
		ids []string
		req = &waitlistspb.ListOpenListsRequest{}
	)

	for range maxPages {
		page, err := client.ListOpenLists(ctx, req)
		must.NoError(t, err, must.Sprint("reading the open catalog"))

		ids = append(ids, listIDs(page.GetResults())...)

		next := page.GetPagination().GetCursor()
		if next == "" || len(page.GetResults()) == 0 {
			return ids
		}

		req = &waitlistspb.ListOpenListsRequest{Filter: &filteringpb.QueryFilter{Cursor: &next}}
	}

	t.Fatalf("conformance: the open catalog was still paging after %d pages", maxPages)

	return nil
}

func listIDs(lists []*waitlistspb.Waitlist) []string {
	out := make([]string, 0, len(lists))
	for _, l := range lists {
		out = append(out, l.GetId())
	}

	return out
}

func signupIDs(signups []*waitlistspb.Signup) []string {
	out := make([]string, 0, len(signups))
	for _, s := range signups {
		out = append(out, s.GetId())
	}

	return out
}
