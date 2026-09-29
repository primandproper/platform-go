package passkeys

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestService_SQLite runs the ceremony suite against SQLite. The same suite
// runs against real servers in containers_test.go.
func TestService_SQLite(T *testing.T) {
	T.Parallel()

	runServiceSuite(T, newSQLiteEnv(T))
}

// memorySessions is a webauthn.SessionStore in a map. The ceremony state's
// real homes are authentication/webauthnsessions and primitives-go's cache
// store, each held to its own conformance suite; what is under test here is the
// order the service runs a ceremony's steps in, which is one process.
type memorySessions struct {
	sessions map[string]webauthn.SessionData
	mu       sync.Mutex
}

var _ webauthn.SessionStore = (*memorySessions)(nil)

func (m *memorySessions) Save(_ context.Context, session *webauthn.SessionData, ttl time.Duration) error {
	if err := webauthn.ValidateSession(session, ttl); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[session.Challenge] = *session

	return nil
}

func (m *memorySessions) Consume(_ context.Context, challenge string) (*webauthn.SessionData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[challenge]
	if !ok {
		return nil, webauthn.ErrSessionNotFound
	}

	delete(m.sessions, challenge)

	return &session, nil
}

// recordingHooks counts what ran and refuses on demand.
type recordingHooks struct {
	registerErr error
	archiveErr  error
	failedErr   error

	registered []*Credential
	archived   []*Credential
	failed     []*FailedLogin

	mu sync.Mutex
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) AfterRegisterPasskey(_ context.Context, tx database.Tx, _ tenancy.Scope, c *Credential) error {
	if tx == nil {
		return errors.New("register hook handed no transaction")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.registered = append(h.registered, c)

	return h.registerErr
}

func (h *recordingHooks) AfterArchivePasskey(_ context.Context, tx database.Tx, _ tenancy.Scope, c *Credential) error {
	if tx == nil {
		return errors.New("archive hook handed no transaction")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.archived = append(h.archived, c)

	return h.archiveErr
}

func (h *recordingHooks) AfterFailedPasskeyLogin(_ context.Context, tx database.Tx, _ tenancy.Scope, a *FailedLogin) error {
	if tx == nil {
		return errors.New("failed-login hook handed no transaction")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.failed = append(h.failed, a)

	return h.failedErr
}

func (h *recordingHooks) failures() []*FailedLogin {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]*FailedLogin(nil), h.failed...)
}

// The two people the suite signs in. Each one's handle is their user id, which
// is the arrangement dinnerdonebetter uses; nothing here depends on it.
const (
	aliceID = "user_alice"
	bobID   = "user_bob"
)

// directory is the consumer's side of both resolvers.
var directory = map[string]UserIdentity{
	aliceID: {UserID: aliceID, Name: "alice", DisplayName: "Alice"},
	bobID:   {UserID: bobID, Name: "bob", DisplayName: "Bob"},
}

var errNoSuchUser = errors.New("no such user in the test directory")

func resolveHandle(_ context.Context, handle []byte) (UserIdentity, error) {
	identity, ok := directory[string(handle)]
	if !ok {
		return UserIdentity{}, errNoSuchUser
	}

	return identity, nil
}

func resolveUsername(_ context.Context, _ tenancy.Scope, username string) ([]byte, error) {
	for id := range directory {
		if directory[id].Name == username {
			return []byte(id), nil
		}
	}

	return nil, errors.Join(errNoSuchUser, ErrUnknownUsername)
}

// newTestRelyingParty builds a relying party for the virtual authenticator's
// origin, over ceremony state in memory.
func newTestRelyingParty(tb testing.TB) *webauthn.RelyingParty {
	tb.Helper()

	rp, err := webauthn.NewRelyingParty(tb.Context(), &webauthn.Config{
		RPID:          testRPID,
		RPDisplayName: "Example",
		RPOrigins:     []string{testOrigin},
	}, &memorySessions{sessions: map[string]webauthn.SessionData{}})
	must.NoError(tb, err)

	return rp
}

// serviceFixture is one service over a fresh table, and the hooks it runs.
type serviceFixture struct {
	env     *storeEnv
	store   *SQLStore
	service *Service
	hooks   *recordingHooks
}

// newService builds a service over a fresh table. The options it is given
// apply after the suite's defaults, so a case can replace any of them.
func (e *storeEnv) newService(t *testing.T, opts ...ServiceOption) *serviceFixture {
	t.Helper()

	store := e.newStore(t)
	rp := newTestRelyingParty(t)

	users, err := NewUserSource(store, resolveHandle)
	must.NoError(t, err)

	hooks := &recordingHooks{}

	defaults := []ServiceOption{
		WithHooks(hooks),
		WithEnrollmentGate(AdmitEveryEnrollment),
		WithUsernameResolver(resolveUsername),
	}

	service, err := NewService(e.client, store, rp, users, append(defaults, opts...)...)
	must.NoError(t, err)

	return &serviceFixture{env: e, store: store, service: service, hooks: hooks}
}

// register runs a whole registration ceremony for userID with a new
// authenticator, and reports the authenticator and what FinishRegistration
// answered.
func (f *serviceFixture) register(t *testing.T, userID string) (*virtualAuthenticator, *Credential, error) {
	t.Helper()

	handle := []byte(userID)
	device := newAuthenticator(t, handle)

	creation, err := f.service.BeginRegistration(t.Context(), f.env.reader(), testScope, handle)
	if err != nil {
		return device, nil, err
	}

	response := device.register(t, creation.Response.Challenge.String())

	var registered *Credential

	err = f.env.inTx(t, func(tx database.Tx) error {
		var txErr error
		registered, txErr = f.service.FinishRegistration(t.Context(), tx, testScope, handle, "Phone", response)

		return txErr
	})

	return device, registered, err
}

func (f *serviceFixture) mustRegister(t *testing.T, userID string) (*virtualAuthenticator, *Credential) {
	t.Helper()

	device, registered, err := f.register(t, userID)
	must.NoError(t, err)
	must.NotNil(t, registered)

	return device, registered
}

// login runs a named login for username with device.
func (f *serviceFixture) login(t *testing.T, username string, device *virtualAuthenticator) (*Login, error) {
	t.Helper()

	assertion, err := f.service.BeginLogin(t.Context(), f.env.reader(), testScope, username)
	must.NoError(t, err)

	return f.service.FinishLogin(t.Context(), testScope, username, device.assert(t, assertion.Response.Challenge.String()))
}

// archive revokes a passkey through the service, in a transaction of its own.
func (f *serviceFixture) archive(t *testing.T, rowID, userID string) (*Credential, error) {
	t.Helper()

	var archived *Credential

	err := f.env.inTx(t, func(tx database.Tx) error {
		var txErr error
		archived, txErr = f.service.ArchiveCredential(t.Context(), tx, testScope, rowID, userID)

		return txErr
	})

	return archived, err
}

func (f *serviceFixture) live(t *testing.T, userID string) []*Credential {
	t.Helper()

	credentials, err := f.service.ListCredentials(t.Context(), f.env.reader(), testScope, userID)
	must.NoError(t, err)

	return credentials
}

// shape renders an assertion's options with the challenge blanked, which is
// everything a caller of BeginLogin could compare two answers by.
func shape(t *testing.T, assertion *protocol.CredentialAssertion) string {
	t.Helper()

	blanked := *assertion
	blanked.Response.Challenge = nil

	rendered, err := json.Marshal(blanked)
	must.NoError(t, err)

	return string(rendered)
}

// runServiceSuite is every ceremony this service runs, against whatever
// database the environment holds.
func runServiceSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("registration stores the credential the ceremony produced", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, registered := f.mustRegister(t, aliceID)

		test.EqOp(t, aliceID, registered.BelongsToUser)
		test.EqOp(t, testScope, registered.Scope)
		test.EqOp(t, "Phone", registered.FriendlyName)
		test.Eq(t, device.credentialID, registered.CredentialID)
		test.Eq(t, []string{"internal", "hybrid"}, registered.Transports)
		test.EqOp(t, uint32(1), registered.SignCount)

		test.SliceLen(t, 1, f.hooks.registered)
		test.SliceLen(t, 1, f.live(t, aliceID))
	})

	t.Run("registration asks for a discoverable credential and excludes the enrolled ones", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, _ := f.mustRegister(t, aliceID)

		creation, err := f.service.BeginRegistration(t.Context(), env.reader(), testScope, []byte(aliceID))
		must.NoError(t, err)

		test.EqOp(t, protocol.ResidentKeyRequirementRequired, creation.Response.AuthenticatorSelection.ResidentKey)
		must.SliceLen(t, 1, creation.Response.CredentialExcludeList)
		test.Eq(t, device.credentialID, []byte(creation.Response.CredentialExcludeList[0].CredentialID))
	})

	t.Run("a refusing registration hook rolls the credential back", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		f.hooks.registerErr = errors.New("audit refused")

		_, _, err := f.register(t, aliceID)
		test.ErrorIs(t, err, f.hooks.registerErr)
		test.SliceEmpty(t, f.live(t, aliceID))
	})

	t.Run("the enrollment gate refuses at the beginning", func(t *testing.T) {
		t.Parallel()

		errReauthenticate := errors.New("re-authenticate first")
		f := env.newService(t, WithEnrollmentGate(func(context.Context, tenancy.Scope, string) error {
			return errReauthenticate
		}))

		_, err := f.service.BeginRegistration(t.Context(), env.reader(), testScope, []byte(aliceID))
		test.ErrorIs(t, err, errReauthenticate)
	})

	t.Run("the enrollment gate is asked again at the write", func(t *testing.T) {
		t.Parallel()

		errWindowClosed := errors.New("re-authentication window closed")

		var (
			mu    sync.Mutex
			calls int
		)

		f := env.newService(t, WithEnrollmentGate(func(_ context.Context, _ tenancy.Scope, userID string) error {
			mu.Lock()
			defer mu.Unlock()

			calls++
			if userID != aliceID {
				return errors.New("gate asked about the wrong user")
			}

			if calls > 1 {
				return errWindowClosed
			}

			return nil
		}))

		_, _, err := f.register(t, aliceID)
		test.ErrorIs(t, err, errWindowClosed)
		test.SliceEmpty(t, f.live(t, aliceID))
	})

	t.Run("a named login proves the user and writes the sign count back", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, registered := f.mustRegister(t, aliceID)

		proven, err := f.login(t, "alice", device)
		must.NoError(t, err)

		test.EqOp(t, aliceID, proven.Credential.BelongsToUser)
		test.EqOp(t, registered.ID, proven.Credential.ID)
		test.EqOp(t, uint32(2), proven.Credential.SignCount)
		test.NotNil(t, proven.Credential.LastUsedAt)
		test.True(t, proven.UserVerified)

		// Committed rather than merely returned: the caller's next transaction
		// cannot roll it back.
		stored, err := f.store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, device.credentialID)
		must.NoError(t, err)
		test.EqOp(t, uint32(2), stored.SignCount)
		test.SliceEmpty(t, f.hooks.failures())
	})

	t.Run("a discoverable login proves whoever the passkey names", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		f.mustRegister(t, aliceID)
		device, registered := f.mustRegister(t, bobID)

		assertion, err := f.service.BeginDiscoverableLogin(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, assertion.Response.AllowedCredentials)

		proven, err := f.service.FinishDiscoverableLogin(t.Context(), testScope,
			device.assert(t, assertion.Response.Challenge.String()))
		must.NoError(t, err)

		test.EqOp(t, bobID, proven.Credential.BelongsToUser)
		test.EqOp(t, registered.ID, proven.Credential.ID)
		test.True(t, proven.UserVerified)
	})

	t.Run("a login the authenticator did not verify the user for says so", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, registered := f.mustRegister(t, aliceID)
		device.unverified = true

		// Named: the relying party prefers verification and admits a
		// ceremony without it, which is the default this answer exists for.
		proven, err := f.login(t, "alice", device)
		must.NoError(t, err)

		test.EqOp(t, registered.ID, proven.Credential.ID)
		test.False(t, proven.UserVerified)

		// Discoverable.
		assertion, err := f.service.BeginDiscoverableLogin(t.Context())
		must.NoError(t, err)

		proven, err = f.service.FinishDiscoverableLogin(t.Context(), testScope,
			device.assert(t, assertion.Response.Challenge.String()))
		must.NoError(t, err)

		test.EqOp(t, registered.ID, proven.Credential.ID)
		test.False(t, proven.UserVerified)
	})

	t.Run("an unknown username is answered with the shape a known one gets", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		f.mustRegister(t, aliceID)

		known, err := f.service.BeginLogin(t.Context(), env.reader(), testScope, "alice")
		must.NoError(t, err)

		unknown, err := f.service.BeginLogin(t.Context(), env.reader(), testScope, "nobody")
		must.NoError(t, err)

		// A known user with no passkey is the same answer again.
		passkeyless, err := f.service.BeginLogin(t.Context(), env.reader(), testScope, "bob")
		must.NoError(t, err)

		test.SliceEmpty(t, known.Response.AllowedCredentials)
		test.EqOp(t, shape(t, known), shape(t, unknown))
		test.EqOp(t, shape(t, known), shape(t, passkeyless))
	})

	t.Run("an unknown username is refused at the finish like any failed login", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, _ := f.mustRegister(t, aliceID)

		// Alice's own passkey, offered against a username nobody holds, must
		// not sign her in: if it did, the difference between that and a known
		// username refusing her would be the oracle BeginLogin closed.
		_, err := f.login(t, "nobody", device)
		test.ErrorIs(t, err, ErrLoginFailed)
		test.False(t, errors.Is(err, ErrUnknownUsername), test.Sprint("the caller is not told the username names nobody"))

		failures := f.hooks.failures()
		must.SliceLen(t, 1, failures)
		test.EqOp(t, "", failures[0].UserID)
		test.Eq(t, device.credentialID, failures[0].CredentialID)
		test.ErrorIs(t, failures[0].Cause, ErrUnknownUsername)
	})

	// The refusals are made by different checks, and the finish is where the
	// difference between them would reach a caller: the message and the chain
	// are both what a transport might render or match. Alice's passkey offered
	// against a username nobody holds, against Bob holding a passkey of his
	// own, and against Bob holding none must read identically.
	t.Run("every refused login answers its caller alike", func(t *testing.T) {
		t.Parallel()

		withBob := env.newService(t)
		device, _ := withBob.mustRegister(t, aliceID)
		withBob.mustRegister(t, bobID)

		_, unknown := withBob.login(t, "nobody", device)
		_, others := withBob.login(t, "bob", device)

		passkeyless := env.newService(t)
		device, _ = passkeyless.mustRegister(t, aliceID)

		_, none := passkeyless.login(t, "bob", device)

		for _, err := range []error{unknown, others, none} {
			must.ErrorIs(t, err, ErrLoginFailed)
			test.EqOp(t, unknown.Error(), err.Error())
		}

		causes := withBob.hooks.failures()
		must.SliceLen(t, 2, causes)
		test.ErrorIs(t, causes[0].Cause, ErrUnknownUsername)
		test.False(t, errors.Is(causes[1].Cause, ErrUnknownUsername), test.Sprint("the hook is told why"))
	})

	t.Run("a passkey answering for somebody else is refused", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		f.mustRegister(t, aliceID)
		bobs, _ := f.mustRegister(t, bobID)

		_, err := f.login(t, "alice", bobs)
		test.ErrorIs(t, err, ErrLoginFailed)

		failures := f.hooks.failures()
		must.SliceLen(t, 1, failures)
		test.EqOp(t, aliceID, failures[0].UserID)

		stored, err := f.store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, bobs.credentialID)
		must.NoError(t, err)
		test.EqOp(t, uint32(1), stored.SignCount)
	})

	t.Run("a sign count that did not advance is refused and not written", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, _ := f.mustRegister(t, aliceID)

		_, err := f.login(t, "alice", device)
		must.NoError(t, err)

		// A clone of the key, whose counter is behind the original's.
		device.signCount = 0

		_, err = f.login(t, "alice", device)
		test.ErrorIs(t, err, ErrLoginFailed)
		test.ErrorIs(t, err, ErrSignCountRegressed)

		stored, err := f.store.GetCredentialByCredentialID(t.Context(), env.reader(), testScope, device.credentialID)
		must.NoError(t, err)
		test.EqOp(t, uint32(2), stored.SignCount)

		failures := f.hooks.failures()
		must.SliceLen(t, 1, failures)
		test.ErrorIs(t, failures[0].Cause, ErrSignCountRegressed)
	})

	t.Run("a failed-login hook that cannot record is surfaced in place of the refusal", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		device, _ := f.mustRegister(t, aliceID)
		f.hooks.failedErr = errors.New("lockout counter unavailable")

		_, err := f.login(t, "nobody", device)
		test.ErrorIs(t, err, f.hooks.failedErr)
		test.False(t, errors.Is(err, ErrLoginFailed))
	})

	t.Run("a user may archive a passkey that is not their last", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		_, first := f.mustRegister(t, aliceID)
		f.mustRegister(t, aliceID)

		archived, err := f.archive(t, first.ID, aliceID)
		must.NoError(t, err)
		test.NotNil(t, archived.ArchivedAt)
		test.SliceLen(t, 1, f.hooks.archived)
		test.SliceLen(t, 1, f.live(t, aliceID))
	})

	t.Run("the last passkey of a user with no other way in is kept", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t)
		_, only := f.mustRegister(t, aliceID)

		_, err := f.archive(t, only.ID, aliceID)
		test.ErrorIs(t, err, ErrLastCredential)
		test.SliceLen(t, 1, f.live(t, aliceID))
		test.SliceEmpty(t, f.hooks.archived)
	})

	t.Run("the last passkey of a user with a password may go", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t, WithAlternativeSignIn(
			func(_ context.Context, q database.SQLQueryExecutor, _ tenancy.Scope, userID string) (bool, error) {
				if q == nil {
					return false, errors.New("alternative check handed no executor")
				}

				return userID == aliceID, nil
			}))
		_, only := f.mustRegister(t, aliceID)

		_, err := f.archive(t, only.ID, aliceID)
		must.NoError(t, err)
		test.SliceEmpty(t, f.live(t, aliceID))
	})

	t.Run("the guard is off when it is turned off by name", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t, WithoutLastCredentialGuard())
		_, only := f.mustRegister(t, aliceID)

		_, err := f.archive(t, only.ID, aliceID)
		must.NoError(t, err)
		test.SliceEmpty(t, f.live(t, aliceID))
	})

	t.Run("a refusing archive hook rolls the archive back", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t, WithoutLastCredentialGuard())
		_, only := f.mustRegister(t, aliceID)
		f.hooks.archiveErr = errors.New("audit refused")

		_, err := f.archive(t, only.ID, aliceID)
		test.ErrorIs(t, err, f.hooks.archiveErr)
		test.SliceLen(t, 1, f.live(t, aliceID))
	})

	t.Run("somebody else's passkey is not found", func(t *testing.T) {
		t.Parallel()

		f := env.newService(t, WithoutLastCredentialGuard())
		_, alices := f.mustRegister(t, aliceID)

		_, err := f.archive(t, alices.ID, bobID)
		test.ErrorIs(t, err, ErrCredentialNotFound)
		test.SliceLen(t, 1, f.live(t, aliceID))
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)
	store := env.newStore(T)
	rp := newTestRelyingParty(T)

	users, usersErr := NewUserSource(store, resolveHandle)
	must.NoError(T, usersErr)

	gate := WithEnrollmentGate(AdmitEveryEnrollment)

	T.Run("every dependency is required", func(t *testing.T) {
		t.Parallel()

		_, err := NewService(nil, store, rp, users, gate)
		test.ErrorIs(t, err, ErrNilDatabaseClient)

		_, err = NewService(env.client, nil, rp, users, gate)
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewService(env.client, store, nil, users, gate)
		test.ErrorIs(t, err, ErrNilRelyingParty)

		_, err = NewService(env.client, store, rp, nil, gate)
		test.ErrorIs(t, err, ErrNilUserSource)
	})

	T.Run("a service with no enrollment gate is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewService(env.client, store, rp, users)
		test.ErrorIs(t, err, ErrNoEnrollmentGate)
	})

	T.Run("a named login needs a username resolver", func(t *testing.T) {
		t.Parallel()

		service, err := NewService(env.client, store, rp, users, gate)
		must.NoError(t, err)

		_, err = service.BeginLogin(t.Context(), env.reader(), testScope, "alice")
		test.ErrorIs(t, err, ErrNoUsernameResolver)

		// The discoverable login needs none.
		_, err = service.BeginDiscoverableLogin(t.Context())
		test.NoError(t, err)
	})

	T.Run("a named login names somebody", func(t *testing.T) {
		t.Parallel()

		service, err := NewService(env.client, store, rp, users, gate, WithUsernameResolver(resolveUsername))
		must.NoError(t, err)

		_, err = service.BeginLogin(t.Context(), env.reader(), testScope, "")
		test.ErrorIs(t, err, ErrEmptyUsername)
	})

	T.Run("a resolver's own failure is not read as an unknown username", func(t *testing.T) {
		t.Parallel()

		errDirectoryDown := errors.New("directory unavailable")
		service, err := NewService(env.client, store, rp, users, gate,
			WithUsernameResolver(func(context.Context, tenancy.Scope, string) ([]byte, error) {
				return nil, errDirectoryDown
			}))
		must.NoError(t, err)

		_, err = service.BeginLogin(t.Context(), env.reader(), testScope, "alice")
		test.ErrorIs(t, err, errDirectoryDown)
	})

	T.Run("a finish without a response is refused", func(t *testing.T) {
		t.Parallel()

		service, err := NewService(env.client, store, rp, users, gate, WithUsernameResolver(resolveUsername))
		must.NoError(t, err)

		_, err = service.FinishLogin(t.Context(), testScope, "alice", nil)
		test.ErrorIs(t, err, ErrEmptyCeremonyResponse)

		_, err = service.FinishDiscoverableLogin(t.Context(), testScope, nil)
		test.ErrorIs(t, err, ErrEmptyCeremonyResponse)
	})
}
