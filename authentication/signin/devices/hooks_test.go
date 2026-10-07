package devices_test

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/devices"
	devicesmock "github.com/primandproper/platform-go/v15/authentication/signin/devices/mock"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	hookScope = tenancy.Of("tenant_a")

	// laptop is what the extractor these tests hand the hooks reads.
	laptop = devices.Origin{IPAddress: "203.0.113.7", UserAgent: "Mozilla/5.0", DeviceName: "Ada's laptop"}

	errInnerRefused = platformerrors.New("the wrapped hooks refused")
	errStoreFailed  = platformerrors.New("the store could not record")
)

// readsLaptop is an extractor that answers laptop whatever it is handed.
func readsLaptop(context.Context) devices.Origin { return laptop }

// signInFor is a completed sign-in for a user, of a login with a refresh token
// that outlives its access token.
func signInFor(userID string) *signin.SignIn {
	issued := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)

	return &signin.SignIn{
		Principal:             &identity.Principal{User: &identity.User{ID: userID}},
		FamilyID:              "family_1",
		TokenID:               "token_1",
		ExpiresAt:             issued.Add(time.Hour),
		RefreshTokenExpiresAt: issued.Add(30 * 24 * time.Hour),
	}
}

// innerHooks are the hooks a consumer already had, recording that they ran and
// answering what a test tells them to.
type innerHooks struct {
	signin.NoopHooks

	err   error
	calls int
}

func (h *innerHooks) AfterIssueToken(context.Context, database.Tx, tenancy.Scope, *signin.SignIn) error {
	h.calls++

	return h.err
}

func (h *innerHooks) AfterRevokeSignIns(context.Context, database.Tx, tenancy.Scope, *signin.Revocation) error {
	h.calls++

	return h.err
}

func TestNewHooks(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := devices.NewHooks(nil, nil, readsLaptop)
		test.ErrorIs(t, err, devices.ErrNilStore)

		// No default extractor: which parts of a request to believe is the
		// deployment's to say, and PeerExtractor is passed by name.
		_, err = devices.NewHooks(nil, &devicesmock.StoreMock{}, nil)
		test.ErrorIs(t, err, devices.ErrNilExtractor)
	})

	T.Run("wraps no hooks at all as the no-op set", func(t *testing.T) {
		t.Parallel()

		hooks, err := devices.NewHooks(nil, &devicesmock.StoreMock{}, devices.PeerExtractor)
		must.NoError(t, err)

		// Every method it does not override is the wrapped set's, so a nil inner
		// must be something every one of them can be called on.
		test.NoError(t, hooks.AfterAuthenticate(t.Context(), nil, hookScope, &signin.Authentication{}))
	})
}

func TestHooks_AfterIssueToken(T *testing.T) {
	T.Parallel()

	T.Run("records the login on the token's transaction, after the wrapped hooks", func(t *testing.T) {
		t.Parallel()

		inner := &innerHooks{}
		signIn := signInFor("user_1")

		store := &devicesmock.StoreMock{
			RecordFunc: func(_ context.Context, _ database.Tx, scope tenancy.Scope, sighting *devices.Sighting) error {
				test.EqOp(t, 1, inner.calls)
				test.EqOp(t, hookScope, scope)
				test.EqOp(t, "family_1", sighting.FamilyID)
				test.EqOp(t, "user_1", sighting.UserID)
				test.EqOp(t, laptop, sighting.Origin)

				return nil
			},
		}

		hooks, err := devices.NewHooks(inner, store, readsLaptop)
		must.NoError(t, err)

		must.NoError(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, signIn))
		test.SliceLen(t, 1, store.RecordCalls())
	})

	// The row lives as long as the login could: the refresh token's deadline
	// where there is one, and the access token's where there is not.
	T.Run("keeps the row for as long as the login could live", func(t *testing.T) {
		t.Parallel()

		withRefresh := signInFor("user_1")

		withoutRefresh := signInFor("user_1")
		withoutRefresh.RefreshTokenExpiresAt = time.Time{}

		store := &devicesmock.StoreMock{
			RecordFunc: func(context.Context, database.Tx, tenancy.Scope, *devices.Sighting) error { return nil },
		}

		hooks, err := devices.NewHooks(nil, store, readsLaptop)
		must.NoError(t, err)

		must.NoError(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, withRefresh))
		must.NoError(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, withoutRefresh))

		calls := store.RecordCalls()
		must.SliceLen(t, 2, calls)
		test.EqOp(t, withRefresh.RefreshTokenExpiresAt, calls[0].Sighting.ExpiresAt)
		test.EqOp(t, withoutRefresh.ExpiresAt, calls[1].Sighting.ExpiresAt)
	})

	// The request behind an impersonation is the operator's, and the login is
	// listed to the subject — who would be shown their operator's address.
	T.Run("records nothing for an impersonation", func(t *testing.T) {
		t.Parallel()

		signIn := signInFor("user_1")
		signIn.ActorID = "operator_1"
		signIn.ActorScope = hookScope

		inner := &innerHooks{}
		store := &devicesmock.StoreMock{}

		hooks, err := devices.NewHooks(inner, store, readsLaptop)
		must.NoError(t, err)

		must.NoError(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, signIn))
		test.EqOp(t, 1, inner.calls)
		test.SliceEmpty(t, store.RecordCalls())
	})

	T.Run("records nothing for a sign-in with nothing to key a row on", func(t *testing.T) {
		t.Parallel()

		noFamily := signInFor("user_1")
		noFamily.FamilyID = ""

		nobody := signInFor("")

		noPrincipal := signInFor("user_1")
		noPrincipal.Principal = nil

		store := &devicesmock.StoreMock{}

		hooks, err := devices.NewHooks(nil, store, readsLaptop)
		must.NoError(t, err)

		for _, signIn := range []*signin.SignIn{nil, noFamily, nobody, noPrincipal} {
			must.NoError(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, signIn))
		}

		test.SliceEmpty(t, store.RecordCalls())
	})

	// A sign-in the wrapped hooks refused is rolled back, so there is no login
	// to record a device for.
	T.Run("records nothing when the wrapped hooks refuse", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{}

		hooks, err := devices.NewHooks(&innerHooks{err: errInnerRefused}, store, readsLaptop)
		must.NoError(t, err)

		test.ErrorIs(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, signInFor("user_1")), errInnerRefused)
		test.SliceEmpty(t, store.RecordCalls())
	})

	// A login whose device could not be recorded would be a row on the screen
	// that silently stopped saying where, so the write's error is the hook's and
	// the mint rolls back.
	T.Run("fails the sign-in when the device cannot be recorded", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{
			RecordFunc: func(context.Context, database.Tx, tenancy.Scope, *devices.Sighting) error {
				return errStoreFailed
			},
		}

		hooks, err := devices.NewHooks(nil, store, readsLaptop)
		must.NoError(t, err)

		test.ErrorIs(t, hooks.AfterIssueToken(t.Context(), nil, hookScope, signInFor("user_1")), errStoreFailed)
	})
}

func TestHooks_AfterRevokeSignIns(T *testing.T) {
	T.Parallel()

	revocation := &signin.Revocation{
		Reason:    signin.RevocationSignOut,
		SubjectID: "user_1",
		ActorID:   "user_1",
		FamilyIDs: []string{"family_1", "family_2"},
	}

	T.Run("deletes the ended logins' rows on the revocation's transaction, after the wrapped hooks", func(t *testing.T) {
		t.Parallel()

		inner := &innerHooks{}

		store := &devicesmock.StoreMock{
			DeleteForFamiliesFunc: func(
				_ context.Context,
				_ database.Tx,
				scope tenancy.Scope,
				userID string,
				familyIDs []string,
			) (int64, error) {
				test.EqOp(t, 1, inner.calls)
				test.EqOp(t, hookScope, scope)
				test.EqOp(t, "user_1", userID)
				test.Eq(t, []string{"family_1", "family_2"}, familyIDs)

				return 2, nil
			},
		}

		hooks, err := devices.NewHooks(inner, store, readsLaptop)
		must.NoError(t, err)

		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, revocation))
		test.SliceLen(t, 1, store.DeleteForFamiliesCalls())
	})

	T.Run("deletes nothing when the wrapped hooks refuse", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{}

		hooks, err := devices.NewHooks(&innerHooks{err: errInnerRefused}, store, readsLaptop)
		must.NoError(t, err)

		test.ErrorIs(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, revocation), errInnerRefused)
		test.SliceEmpty(t, store.DeleteForFamiliesCalls())
	})

	// A delete that fails fails the revocation: signin does not end a login it
	// could not record ending.
	T.Run("fails the revocation when the delete fails", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{
			DeleteForFamiliesFunc: func(context.Context, database.Tx, tenancy.Scope, string, []string) (int64, error) {
				return 0, errStoreFailed
			},
		}

		hooks, err := devices.NewHooks(nil, store, readsLaptop)
		must.NoError(t, err)

		test.ErrorIs(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, revocation), errStoreFailed)
	})

	T.Run("deletes nothing for a revocation naming nobody or no login", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{}

		hooks, err := devices.NewHooks(nil, store, readsLaptop)
		must.NoError(t, err)

		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, nil))
		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, &signin.Revocation{FamilyIDs: []string{"family_1"}}))
		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), nil, hookScope, &signin.Revocation{SubjectID: "user_1"}))
		test.SliceEmpty(t, store.DeleteForFamiliesCalls())
	})
}
