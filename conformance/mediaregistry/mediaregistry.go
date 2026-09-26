package mediaregistry

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Suite is the guarded object read's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "mediaregistry",

		// The HTTP surfaces are not in Surfaces; whether this one is served is
		// read off the probe caller's HTTP inside, and skipped with the reason.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

// answer is one response from the route, reduced to what the assertions read.
type answer struct {
	header http.Header
	body   []byte
	status int
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)
	if probe.HTTP == nil || !probe.HTTP.MediaRegistry {
		t.Skip("conformance: this subject does not serve the registered-object read")
	}

	t.Run("an object is served to the caller who registered it", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		object := registered(t, s, mine)

		got := fetch(t, mine, object.ID)
		must.EqOp(t, http.StatusOK, got.status, must.Sprintf("a caller could not read an object they registered: %s", got.body))
		test.Eq(t, object.Content, got.body, test.Sprint("the route served bytes other than the ones registered"))

		// Both are decided by mediaregistry/http rather than configured: a
		// guard was consulted to produce this response, so no shared cache may
		// answer the next request with it, and the browser does not get to
		// disagree with the row about what the object is.
		test.StrContains(t, got.header.Get("Cache-Control"), "private")
		test.EqOp(t, "nosniff", got.header.Get("X-Content-Type-Options"))
	})

	t.Run("another tenant's object is absent, exactly as an unknown one is", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		object := registered(t, s, mine)

		owned := fetch(t, mine, object.ID)
		must.EqOp(t, http.StatusOK, owned.status,
			must.Sprint("a caller could not read their own object; the refusal below proves nothing"))

		refused := fetch(t, theirs, object.ID)
		test.EqOp(t, http.StatusNotFound, refused.status, test.Sprint("another tenant's object was not refused as an absence"))
		test.StrNotContains(t, string(refused.body), string(object.Content),
			test.Sprint("another tenant's object's bytes reached this caller"))

		indistinguishable(t, refused, fetch(t, theirs, identifiers.New()))
	})

	t.Run("a colleague who did not register an object is refused as an absence", func(t *testing.T) {
		t.Parallel()

		if s.Seams().MediaObjectsShared {
			t.Skip("conformance: this deployment's entitlement lets somebody other than the owner read an object")
		}

		mine := s.Subject(t)
		object := registered(t, s, mine)
		colleague := s.Subject(t, conformance.InTenant(mine.Scope))

		must.StrNotEqFold(t, mine.UserID, colleague.UserID,
			must.Sprint("the subject minted a colleague as the same user; the entitlement this asserts cannot be observed"))

		owned := fetch(t, mine, object.ID)
		must.EqOp(t, http.StatusOK, owned.status,
			must.Sprint("a caller could not read their own object; the refusal below proves nothing"))

		// Sharing a tenant puts the row within the colleague's read, so this
		// refusal is the entitlement's rather than the scope's — and it has to
		// read exactly as the scope's does, or the difference is an oracle for
		// which identifiers a colleague registered.
		refused := fetch(t, colleague, object.ID)
		test.EqOp(t, http.StatusNotFound, refused.status, test.Sprint("a colleague could read an object somebody else registered"))
		test.StrNotContains(t, string(refused.body), string(object.Content),
			test.Sprint("a colleague received an object somebody else registered"))

		indistinguishable(t, refused, fetch(t, colleague, identifiers.New()))
	})
}

// indistinguishable asserts a refusal reads exactly as the answer for an object
// that was never registered, which is the whole of what this route promises a
// caller who guesses.
func indistinguishable(t *testing.T, refused, unknown *answer) {
	t.Helper()

	test.EqOp(t, http.StatusNotFound, unknown.status, test.Sprint("an identifier nothing was registered under was not a 404"))
	test.EqOp(t, unknown.status, refused.status, test.Sprint("a refusal and an absence answered with different statuses"))
	test.EqOp(t, unknown.header.Get("Content-Type"), refused.header.Get("Content-Type"),
		test.Sprint("a refusal and an absence answered with different content types"))
	test.Eq(t, unknown.body, refused.body, test.Sprint("a refusal and an absence answered with different bodies"))
}

// twoTenants mints two callers and refuses to proceed if the subject put them in
// one tenant, since the confinement asserted would then compare a tenant with
// itself.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// registered has the deployment register an object as sub's, skipping where the
// subject cannot say how its deployment would or does not surface who sub is.
func registered(t *testing.T, s *conformance.Session, sub *conformance.Subject) *conformance.RegisteredObject {
	t.Helper()

	register := s.Seams().Actions.Registered
	s.NeedsAction(t, register != nil, "registered")

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier, so there is nobody to register an object as")
	}

	object, err := register(t.Context(), sub.Scope, sub.UserID)
	must.NoError(t, err, must.Sprint("registering an object"))
	must.NotNil(t, object, must.Sprint("the registration action reported no object and no error"))
	must.StrNotEqFold(t, "", object.ID, must.Sprint("the registration action reported no identifier"))
	must.SliceNotEmpty(t, object.Content,
		must.Sprint("the registration action reported no bytes, which no read could be told apart from serving nothing"))

	return object
}

// fetch reads an object through the route as caller.
func fetch(t *testing.T, caller *conformance.Subject, objectID string) *answer {
	t.Helper()

	url := strings.TrimSuffix(caller.HTTP.BaseURL, "/") + mediaregistryhttp.BasePath + "/" + objectID

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	must.NoError(t, err)

	res, err := caller.HTTP.Client.Do(req)
	must.NoError(t, err)

	defer func() { test.NoError(t, res.Body.Close()) }()

	body, err := io.ReadAll(res.Body)
	must.NoError(t, err)

	return &answer{status: res.StatusCode, header: res.Header, body: body}
}
