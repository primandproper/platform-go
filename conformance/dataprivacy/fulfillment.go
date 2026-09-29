package dataprivacy

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The statuses these assertions read, as the wire spells them.
const (
	statusAwaitingConfirmation = "awaiting_confirmation"
	statusInProgress           = "in_progress"
	statusCompleted            = "completed"
	statusExpired              = "expired"
)

const (
	// directory is the identity suite's surface, which is where an erasure is
	// observed from.
	directory = "identity"

	// getUser is the directory read an erasure is observed through.
	getUser = identitypb.IdentityService_GetUser_FullMethodName
)

// fulfillment asserts that the work a request asks for is done: an export
// completes, an erasure completes and takes its subject and nobody else, and a
// swept export reads expired.
func fulfillment(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an export completes", func(t *testing.T) {
		t.Parallel()

		me := s.Subject(t)
		fulfilled := exported(t, s, me)

		// Only when there is an operation to read, and the operations surface
		// to read it on. Done and succeeded are the operation's own words for
		// what the row above already said.
		if !me.HTTP.Operations || fulfilled.OperationID == "" {
			return
		}

		code, body := call(t, me, http.MethodGet, operationshttp.BasePath+"/"+fulfilled.OperationID, nil)
		must.EqOp(t, http.StatusOK, code, must.Sprintf("reading the operation that fulfilled an export answered %d: %s", code, body))

		op := &envelope[struct {
			State string `json:"state"`
			Done  bool   `json:"done"`
		}]{}
		must.NoError(t, json.Unmarshal(body, op))

		test.True(t, op.Data.Done, test.Sprint("the export completed and its operation says it may still change"))
		test.EqOp(t, "succeeded", op.Data.State)
	})

	t.Run("an erasure completes, and takes only its subject", func(t *testing.T) {
		t.Parallel()

		// A fresh caller, always: this one is erased.
		me := s.Subject(t)
		if me.Surfaces.Identity == nil {
			t.Skip("conformance: this subject mounts no directory, and no client can observe an erasure without one")
		}

		// Beside the erased caller in their directory, so what spares the
		// bystander is that they are somebody else rather than somewhere else.
		here := conformance.InTenant(directory, me.ScopeFor(directory))
		bystander := s.Subject(t, here)
		admin := s.Subject(t, conformance.Making(getUser), conformance.AsAdmin(), here)

		// The positive control. "The user is gone" is also what a read that
		// reaches nobody answers, so both must be found before anything is
		// erased.
		for who, userID := range map[string]string{"the erasure's subject": me.UserID, "the bystander": bystander.UserID} {
			_, err := admin.Surfaces.Identity.GetUser(admin.Context(t.Context()), &identitypb.GetUserRequest{UserId: userID})
			must.NoError(t, err, must.Sprintf("an administrator could not read %s before anything was erased; the absence below proves nothing", who))
		}

		submitted := submit(t, me, "erasure")
		if submitted.Request.Status == statusAwaitingConfirmation {
			// The subject's own link, reached with their session and no token:
			// this deployment waits for a person before it erases one.
			code, body := call(t, me, http.MethodGet,
				dataprivacyhttp.BasePath+"/"+submitted.Request.ID+dataprivacyhttp.ConfirmSuffix, nil)
			must.EqOp(t, http.StatusOK, code, must.Sprintf("confirming an erasure answered %d: %s", code, body))
		}

		s.Await(t, "an erasure to take its subject out of the directory", func() (bool, error) {
			_, err := admin.Surfaces.Identity.GetUser(admin.Context(t.Context()), &identitypb.GetUserRequest{UserId: me.UserID})

			switch status.Code(err) {
			case codes.OK:
				return false, nil
			case codes.NotFound:
				return true, nil
			default:
				return false, platformerrors.Wrap(err, "reading the erasure's subject")
			}
		})

		found, err := admin.Surfaces.Identity.GetUser(admin.Context(t.Context()), &identitypb.GetUserRequest{UserId: bystander.UserID})
		must.NoError(t, err, must.Sprint("an erasure took a bystander in the same directory with its subject"))
		test.EqOp(t, bystander.UserID, found.GetUser().GetId())

		// While the subject can still read their request, it says what
		// happened. A deployment that signs an erased person out answers
		// otherwise, and that is its to decide: nothing here requires the read.
		code, body := call(t, me, http.MethodGet, dataprivacyhttp.BasePath+"/"+submitted.Request.ID, nil)
		if code != http.StatusOK {
			return
		}

		read := &envelope[receipt]{}
		must.NoError(t, json.Unmarshal(body, read))
		test.EqOp(t, statusCompleted, read.Data.Request.Status)
		test.EqOp(t, "", read.Data.Request.ArtifactRef, test.Sprint("an erasure left an artifact behind"))
	})

	t.Run("a swept export reads expired", func(t *testing.T) {
		t.Parallel()

		expire := s.Seams().Actions.ArtifactExpired
		s.NeedsAction(t, expire != nil, "ArtifactExpired")

		me := s.Subject(t)
		fulfilled := exported(t, s, me)

		must.NoError(t, expire(t.Context(), me.ScopeFor(surface), fulfilled.ID))

		// Still there: a subject is entitled to know what was asked in their
		// name after the thing they asked for is gone.
		code, body := call(t, me, http.MethodGet, dataprivacyhttp.BasePath+"/"+fulfilled.ID, nil)
		must.EqOp(t, http.StatusOK, code, must.Sprintf("a swept export's request could not be read by its subject: %s", body))

		read := &envelope[receipt]{}
		must.NoError(t, json.Unmarshal(body, read))
		test.EqOp(t, statusExpired, read.Data.Request.Status)
		test.EqOp(t, "", read.Data.Request.ArtifactRef,
			test.Sprint("an expired export still names an artifact, which nobody will now delete"))
	})
}

// exported submits an export as caller, waits for it to finish, and asserts
// that it completed with an artifact, an expiry and nothing missing.
func exported(t *testing.T, s *conformance.Session, caller *conformance.Subject) *request {
	t.Helper()

	submitted := submit(t, caller, "export")

	// The control that the work really is asynchronous: an export that came
	// back finished would make the wait below a formality.
	must.EqOp(t, statusInProgress, submitted.Request.Status, must.Sprint("an export's receipt did not say it was under way"))

	fulfilled := awaitTerminal(t, s, caller, submitted.Request.ID)

	must.EqOp(t, statusCompleted, fulfilled.Status, must.Sprintf("an export finished as %s: %s %v", fulfilled.Status, fulfilled.LastError, fulfilled.Failures))
	test.NotEqOp(t, "", fulfilled.ArtifactRef, test.Sprint("a completed export named no artifact"))
	test.False(t, fulfilled.ExpiresAt.IsZero(), test.Sprint("a completed export's artifact has no expiry, so no sweep will ever delete it"))
	test.NotNil(t, fulfilled.CompletedAt, test.Sprint("a completed export carries no completion time"))
	test.MapEmpty(t, fulfilled.Failures, test.Sprint("a registered collector failed against this deployment's schema"))

	return fulfilled
}

// awaitTerminal reads a request as its subject until it stops changing, and
// returns it as it ended.
//
// The request rather than its operation: the row is what a subject sees, and an
// operation that finished without moving it is exactly the failure to catch.
func awaitTerminal(t *testing.T, s *conformance.Session, caller *conformance.Subject, requestID string) *request {
	t.Helper()

	var last request

	s.Await(t, "privacy request "+requestID+" to finish", func() (bool, error) {
		code, body := call(t, caller, http.MethodGet, dataprivacyhttp.BasePath+"/"+requestID, nil)
		if code != http.StatusOK {
			return false, platformerrors.Newf("reading the request answered %d: %s", code, body)
		}

		read := &envelope[receipt]{}
		if err := json.Unmarshal(body, read); err != nil {
			return false, err
		}

		last = read.Data.Request

		return last.Status != statusAwaitingConfirmation && last.Status != statusInProgress, nil
	})

	return &last
}
