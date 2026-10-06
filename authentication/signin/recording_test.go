package signin_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v15/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	recordedUserID   = "user-1"
	recordedCallerID = "caller-1"
)

var recordingScope = tenancy.Of("acme")

// recordingCaller is the principal a context carries in these tests, for the
// hooks that read their actor off it.
type recordingCaller struct{}

func (recordingCaller) UserID() string          { return recordedCallerID }
func (recordingCaller) Scope() tenancy.Scope    { return recordingScope }
func (recordingCaller) ActiveAccountID() string { return "" }

func aCaller(context.Context) (callers.Principal, bool) { return recordingCaller{}, true }

func noCaller(context.Context) (callers.Principal, bool) { return nil, false }

// recordingLedger is everything the two halves were handed, in order.
type recordingLedger struct {
	refuse error
	// catalog is the dispatcher's, and nil means a deployment that has opted
	// every event in, credential events included; see optedIn.
	catalog    webhooks.Catalog
	scopes     []tenancy.Scope
	entries    []*audit.Entry
	published  []outbox.Message
	deliveries []*webhooks.Delivery
}

// credentialEvents are the events EventCatalog marks Internal.
var credentialEvents = []webhooks.EventType{
	signin.EventUserAuthenticated,
	signin.EventPasswordUpdated,
	signin.EventPasswordAttached,
	signin.EventTOTPSecretRefreshed,
	signin.EventTOTPSecretVerified,
	signin.EventVerificationEmailRequested,
	signin.EventMagicLinkRequested,
	signin.EventRecoveryCodeUsed,
	signin.EventRecoveryCodesReplaced,
	signin.EventSignInsRevoked,
}

// subscribableEvents are the events EventCatalog offers a subscriber.
var subscribableEvents = []webhooks.EventType{
	signin.EventEmailAddressVerified,
	signin.EventSignInAccountSwitched,
}

// optedIn is the catalog of a deployment that cleared Internal on its merged
// copy of EventCatalog, so every hook's event reaches a subscriber.
func optedIn() webhooks.Catalog {
	catalog := signin.EventCatalog()
	for eventType, definition := range catalog {
		definition.Internal = false
		catalog[eventType] = definition
	}

	return catalog
}

func newSignInRecordingHooks(
	t *testing.T,
	l *recordingLedger,
	principals callers.PrincipalExtractor,
	opts ...recording.Option,
) *signin.RecordingHooks {
	t.Helper()

	entries := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, scope tenancy.Scope, entries ...*audit.Entry) error {
			if l.refuse != nil {
				return l.refuse
			}

			l.scopes = append(l.scopes, scope)
			l.entries = append(l.entries, entries...)

			return nil
		},
	}

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(_ context.Context, _ database.Tx, msgs ...outbox.Message) error {
			l.published = append(l.published, msgs...)

			return nil
		},
	}

	dispatcher := &webhooksmock.DispatcherMock{
		CatalogFunc: func() webhooks.Catalog {
			if l.catalog != nil {
				return l.catalog
			}

			return optedIn()
		},
		DispatchFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, delivery *webhooks.Delivery) error {
			l.deliveries = append(l.deliveries, delivery)

			return nil
		},
	}

	emitter, err := webhooks.NewEmitter(enqueuer, dispatcher, "events")
	must.NoError(t, err)

	recorder, err := recording.New(entries, emitter, principals, opts...)
	must.NoError(t, err)

	hooks, err := signin.NewRecordingHooks(recorder)
	must.NoError(t, err)

	return hooks
}

// only is the one entry and the one delivery a recording hook produced.
func (l *recordingLedger) only(t *testing.T) (*audit.Entry, *webhooks.Delivery) {
	t.Helper()

	must.SliceLen(t, 1, l.entries)
	must.SliceLen(t, 1, l.deliveries)

	return l.entries[0], l.deliveries[0]
}

func decodePayload[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var payload T
	must.NoError(t, json.Unmarshal(delivery.Payload, &payload))

	return &payload
}

// assertAboutTheUser checks the shape every entry here shares, and that the
// user is named nowhere in its metadata.
func assertAboutTheUser(t *testing.T, entry *audit.Entry, eventType webhooks.EventType) {
	t.Helper()

	test.EqOp(t, signin.ResourceTypeUser, entry.ResourceType)
	test.EqOp(t, recordedUserID, entry.ResourceID)
	test.EqOp(t, eventType.String(), entry.Metadata["event"])

	for key, value := range entry.Metadata {
		test.NotEqOp(t, recordedUserID, value, test.Sprintf("metadata %q names the user, which audit.Erasure cannot see", key))
	}
}

func recordedUser() *identity.User { return &identity.User{ID: recordedUserID} }

func TestNewRecordingHooks(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil recorder by name", func(t *testing.T) {
		t.Parallel()

		hooks, err := signin.NewRecordingHooks(nil)
		test.Nil(t, hooks)
		test.ErrorIs(t, err, signin.ErrNilRecorder)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestEventCatalog(T *testing.T) {
	T.Parallel()

	T.Run("defines every event this package emits, each described", func(t *testing.T) {
		t.Parallel()

		catalog := signin.EventCatalog()
		test.MapLen(t, len(credentialEvents)+len(subscribableEvents), catalog)

		for _, eventType := range slices.Concat(credentialEvents, subscribableEvents) {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}
	})

	T.Run("marks every credential event Internal, and nothing else", func(t *testing.T) {
		t.Parallel()

		catalog := signin.EventCatalog()
		for _, eventType := range credentialEvents {
			test.True(t, catalog[eventType].Internal, test.Sprintf("%s is not Internal", eventType))
			test.False(t, catalog.Subscribable(eventType), test.Sprintf("%s is subscribable", eventType))
		}

		for _, eventType := range subscribableEvents {
			test.True(t, catalog.Subscribable(eventType), test.Sprintf("%s is not subscribable", eventType))
		}
	})

	T.Run("hands each caller its own copy", func(t *testing.T) {
		t.Parallel()

		signin.EventCatalog()[signin.EventUserAuthenticated] = webhooks.EventDefinition{}
		test.NotEqOp(t, "", signin.EventCatalog()[signin.EventUserAuthenticated].Description)
	})
}

func TestRecordingHooks_AfterAuthenticate(T *testing.T) {
	T.Parallel()

	principal := func() *identity.Principal {
		return &identity.Principal{User: recordedUser(), ActiveAccountID: "account-1"}
	}

	T.Run("records the login as the user it proved, though the context names nobody", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, noCaller)

		must.NoError(t, hooks.AfterAuthenticate(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Authentication{
			Principal:      principal(),
			CredentialKind: signin.CredentialKindPassword,
			Administrative: true,
		}))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventUserAuthenticated)
		test.Eq(t, audit.Actor{ID: recordedUserID, Type: audit.ActorUser}, entry.Actor)
		test.EqOp(t, audit.EventOther, entry.EventType)
		test.EqOp(t, string(signin.CredentialKindPassword), entry.Metadata["credentialKind"])
		test.EqOp(t, "true", entry.Metadata["administrative"])
		test.EqOp(t, "account-1", entry.Metadata["accountID"])
		test.MapNotContainsKey(t, entry.Metadata, "impersonatorScope")

		test.EqOp(t, signin.EventUserAuthenticated, delivery.EventType)
		payload := decodePayload[signin.AuthenticationEvent](t, delivery)
		test.EqOp(t, recordedUserID, payload.UserID)
		test.EqOp(t, "account-1", payload.AccountID)
		test.EqOp(t, signin.CredentialKindPassword, payload.CredentialKind)
		test.True(t, payload.Administrative)
		test.EqOp(t, "", payload.ActorID)
	})

	T.Run("records an impersonation as the operator's act, filed under the user", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)

		operatorScope := tenancy.Of("staff")

		must.NoError(t, hooks.AfterAuthenticate(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Authentication{
			Principal:      principal(),
			CredentialKind: signin.CredentialKindImpersonation,
			ActorID:        "operator-1",
			ActorScope:     operatorScope,
		}))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventUserAuthenticated)
		test.Eq(t, audit.Actor{ID: recordedUserID, Type: audit.ActorUser, Impersonator: "operator-1"}, entry.Actor)
		test.EqOp(t, operatorScope.String(), entry.Metadata["impersonatorScope"])
		test.EqOp(t, string(signin.CredentialKindImpersonation), entry.Metadata["credentialKind"])
		test.EqOp(t, "false", entry.Metadata["administrative"])

		payload := decodePayload[signin.AuthenticationEvent](t, delivery)
		test.EqOp(t, "operator-1", payload.ActorID)
		test.EqOp(t, operatorScope, payload.ActorScope)
	})

	T.Run("refuses nothing to record", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)

		for name, auth := range map[string]*signin.Authentication{
			"nil authentication": nil,
			"nil principal":      {},
			"nil user":           {Principal: &identity.Principal{}},
		} {
			err := hooks.AfterAuthenticate(t.Context(), database.NewTxForTesting(nil), recordingScope, auth)
			test.ErrorIs(t, err, signin.ErrNilHookArgument, test.Sprintf("case %q", name))
		}

		test.SliceEmpty(t, l.entries)
		test.SliceEmpty(t, l.deliveries)
	})

	T.Run("a refused entry fails the login", func(t *testing.T) {
		t.Parallel()

		refused := platformerrors.New("refused")
		l := &recordingLedger{refuse: refused}
		hooks := newSignInRecordingHooks(t, l, aCaller)

		err := hooks.AfterAuthenticate(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Authentication{
			Principal:      principal(),
			CredentialKind: signin.CredentialKindPassword,
		})
		test.ErrorIs(t, err, refused)
		test.SliceEmpty(t, l.deliveries)
	})
}

func TestRecordingHooks_RecordNothing(T *testing.T) {
	T.Parallel()

	T.Run("issuing a token and a failed sign-in record nothing, by decision", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)
		tx := database.NewTxForTesting(nil)

		must.NoError(t, hooks.AfterIssueToken(t.Context(), tx, recordingScope, &signin.SignIn{TokenID: "jti-1"}))
		must.NoError(t, hooks.AfterFailedSignIn(t.Context(), tx, recordingScope, &signin.FailedSignIn{Handle: "ada", UserID: recordedUserID}))

		test.SliceEmpty(t, l.entries)
		test.SliceEmpty(t, l.deliveries)
	})
}

func TestRecordingHooks_CredentialWrites(T *testing.T) {
	T.Parallel()

	type userHook func(*signin.RecordingHooks, context.Context, database.Tx, tenancy.Scope, *identity.User) error

	cases := map[webhooks.EventType]userHook{
		signin.EventPasswordUpdated:            (*signin.RecordingHooks).AfterUpdatePassword,
		signin.EventTOTPSecretRefreshed:        (*signin.RecordingHooks).AfterRefreshTOTPSecret,
		signin.EventPasswordAttached:           (*signin.RecordingHooks).AfterAttachPassword,
		signin.EventVerificationEmailRequested: (*signin.RecordingHooks).AfterRequestVerificationEmail,
		signin.EventMagicLinkRequested:         (*signin.RecordingHooks).AfterRequestMagicLink,
		signin.EventTOTPSecretVerified:         (*signin.RecordingHooks).AfterVerifyTOTPSecret,
		signin.EventRecoveryCodesReplaced:      (*signin.RecordingHooks).AfterReplaceRecoveryCodes,
	}

	for eventType, hook := range cases {
		T.Run(eventType.String()+" records the user, as the caller the context names", func(t *testing.T) {
			t.Parallel()

			l := &recordingLedger{}
			hooks := newSignInRecordingHooks(t, l, aCaller)

			must.NoError(t, hook(hooks, t.Context(), database.NewTxForTesting(nil), recordingScope, recordedUser()))

			entry, delivery := l.only(t)
			assertAboutTheUser(t, entry, eventType)
			test.EqOp(t, audit.EventUpdated, entry.EventType)
			test.EqOp(t, recordedCallerID, entry.Actor.ID)
			test.MapLen(t, 1, entry.Metadata)

			test.EqOp(t, eventType, delivery.EventType)
			test.EqOp(t, recordedUserID, decodePayload[signin.UserEvent](t, delivery).UserID)
		})

		T.Run(eventType.String()+" with nobody signed in is unattributed, by name", func(t *testing.T) {
			t.Parallel()

			l := &recordingLedger{}
			hooks := newSignInRecordingHooks(t, l, noCaller)

			must.NoError(t, hook(hooks, t.Context(), database.NewTxForTesting(nil), recordingScope, recordedUser()))

			entry, _ := l.only(t)
			test.EqOp(t, audit.ActorUnattributed, entry.Actor.ID)
		})

		T.Run(eventType.String()+" refuses a nil user", func(t *testing.T) {
			t.Parallel()

			l := &recordingLedger{}
			hooks := newSignInRecordingHooks(t, l, aCaller)

			test.ErrorIs(t, hook(hooks, t.Context(), database.NewTxForTesting(nil), recordingScope, nil), identity.ErrNilUser)
			test.SliceEmpty(t, l.entries)
		})
	}
}

func TestRecordingHooks_CredentialEventsReachNoSubscriber(T *testing.T) {
	T.Parallel()

	T.Run("under EventCatalog a credential write is recorded and published, and delivered to nobody", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{catalog: signin.EventCatalog()}
		hooks := newSignInRecordingHooks(t, l, aCaller)
		tx := database.NewTxForTesting(nil)

		must.NoError(t, hooks.AfterAuthenticate(t.Context(), tx, recordingScope, &signin.Authentication{
			Principal:      &identity.Principal{User: recordedUser()},
			CredentialKind: signin.CredentialKindPassword,
		}))
		must.NoError(t, hooks.AfterUpdatePassword(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterAttachPassword(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterRefreshTOTPSecret(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterVerifyTOTPSecret(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterRequestVerificationEmail(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterRequestMagicLink(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterReplaceRecoveryCodes(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterRecoveryCodeUsed(t.Context(), tx, recordingScope, recordedUser(), 3))
		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), tx, recordingScope, &signin.Revocation{
			Reason: signin.RevocationSignOut, SubjectID: recordedUserID, FamilyIDs: []string{"family-1"},
		}))

		test.SliceLen(t, len(credentialEvents), l.entries)
		test.SliceLen(t, len(credentialEvents), l.published)
		test.SliceEmpty(t, l.deliveries)
	})
}

func TestRecordingHooks_AfterVerify(T *testing.T) {
	T.Parallel()

	T.Run("records an address proven through the link door", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, noCaller)

		must.NoError(t, hooks.AfterVerify(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Verification{
			User:               recordedUser(),
			EmailAddressProven: true,
			Promoted:           true,
		}))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventEmailAddressVerified)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "true", entry.Metadata["promoted"])

		payload := decodePayload[signin.VerificationEvent](t, delivery)
		test.EqOp(t, recordedUserID, payload.UserID)
		test.True(t, payload.Promoted)
	})

	T.Run("records nothing for CompleteVerification, which proves no address", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)

		must.NoError(t, hooks.AfterVerify(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Verification{
			User:     recordedUser(),
			Promoted: true,
		}))

		test.SliceEmpty(t, l.entries)
		test.SliceEmpty(t, l.deliveries)
	})

	T.Run("refuses nothing to record", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)
		tx := database.NewTxForTesting(nil)

		test.ErrorIs(t, hooks.AfterVerify(t.Context(), tx, recordingScope, nil), signin.ErrNilHookArgument)
		test.ErrorIs(t, hooks.AfterVerify(t.Context(), tx, recordingScope, &signin.Verification{EmailAddressProven: true}), identity.ErrNilUser)
		test.SliceEmpty(t, l.entries)
	})
}

func TestRecordingHooks_AfterRecoveryCodeUsed(T *testing.T) {
	T.Parallel()

	T.Run("records the code spent and how many are left", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, noCaller)

		must.NoError(t, hooks.AfterRecoveryCodeUsed(t.Context(), database.NewTxForTesting(nil), recordingScope, recordedUser(), 3))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventRecoveryCodeUsed)
		test.EqOp(t, "3", entry.Metadata["remaining"])

		payload := decodePayload[signin.RecoveryCodeEvent](t, delivery)
		test.EqOp(t, recordedUserID, payload.UserID)
		test.EqOp(t, 3, payload.Remaining)
	})

	T.Run("refuses a nil user", func(t *testing.T) {
		t.Parallel()

		hooks := newSignInRecordingHooks(t, &recordingLedger{}, aCaller)

		test.ErrorIs(t, hooks.AfterRecoveryCodeUsed(t.Context(), database.NewTxForTesting(nil), recordingScope, nil, 0), identity.ErrNilUser)
	})
}

func TestRecordingHooks_AfterRevokeSignIns(T *testing.T) {
	T.Parallel()

	T.Run("records one entry with the door and the count, and names each login on the event", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller)

		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.Revocation{
			Reason:    signin.RevocationSignOutEverywhere,
			SubjectID: recordedUserID,
			ActorID:   recordedUserID,
			FamilyIDs: []string{"family-1", "family-2"},
		}))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventSignInsRevoked)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, string(signin.RevocationSignOutEverywhere), entry.Metadata["reason"])
		test.EqOp(t, "2", entry.Metadata["revoked"])

		payload := decodePayload[signin.RevocationEvent](t, delivery)
		test.EqOp(t, recordedUserID, payload.UserID)
		test.EqOp(t, recordedUserID, payload.ActorID)
		test.EqOp(t, signin.RevocationSignOutEverywhere, payload.Reason)
		test.Eq(t, []string{"family-1", "family-2"}, payload.FamilyIDs)
	})

	T.Run("refuses a nil revocation", func(t *testing.T) {
		t.Parallel()

		hooks := newSignInRecordingHooks(t, &recordingLedger{}, aCaller)

		test.ErrorIs(t, hooks.AfterRevokeSignIns(t.Context(), database.NewTxForTesting(nil), recordingScope, nil), signin.ErrNilHookArgument)
	})
}

func TestRecordingHooks_AfterSwitchAccount(T *testing.T) {
	T.Parallel()

	T.Run("records the login moving, with the account it left", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, noCaller)

		must.NoError(t, hooks.AfterSwitchAccount(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.AccountSwitch{
			SubjectID:      recordedUserID,
			FamilyID:       "family-1",
			FromAccountID:  "account-1",
			ToAccountID:    "account-2",
			Administrative: true,
		}))

		entry, delivery := l.only(t)
		assertAboutTheUser(t, entry, signin.EventSignInAccountSwitched)
		test.EqOp(t, "account-2", entry.Metadata["accountID"])
		test.EqOp(t, "account-1", entry.Metadata["previousAccountID"])
		test.EqOp(t, "true", entry.Metadata["administrative"])

		payload := decodePayload[signin.AccountSwitchEvent](t, delivery)
		test.EqOp(t, recordedUserID, payload.UserID)
		test.EqOp(t, "family-1", payload.FamilyID)
		test.EqOp(t, "account-2", payload.AccountID)
		test.EqOp(t, "account-1", payload.PreviousAccountID)
		test.True(t, payload.Administrative)
	})

	T.Run("a login that began against no account has no previous account", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, noCaller)

		must.NoError(t, hooks.AfterSwitchAccount(t.Context(), database.NewTxForTesting(nil), recordingScope, &signin.AccountSwitch{
			SubjectID:   recordedUserID,
			FamilyID:    "family-1",
			ToAccountID: "account-2",
		}))

		entry, _ := l.only(t)
		test.MapNotContainsKey(t, entry.Metadata, "previousAccountID")
	})

	T.Run("refuses a nil switch", func(t *testing.T) {
		t.Parallel()

		hooks := newSignInRecordingHooks(t, &recordingLedger{}, aCaller)

		test.ErrorIs(t, hooks.AfterSwitchAccount(t.Context(), database.NewTxForTesting(nil), recordingScope, nil), signin.ErrNilHookArgument)
	})
}

func TestRecordingHooks_FiledBySubject(T *testing.T) {
	T.Parallel()

	T.Run("every entry names the user as its subject, so a resolver can file it under them", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &recordingLedger{}
		hooks := newSignInRecordingHooks(t, l, aCaller, recording.WithScopeResolver(bySubject))
		tx := database.NewTxForTesting(nil)

		must.NoError(t, hooks.AfterAuthenticate(t.Context(), tx, recordingScope, &signin.Authentication{
			Principal:      &identity.Principal{User: recordedUser()},
			CredentialKind: signin.CredentialKindMagicLink,
		}))
		must.NoError(t, hooks.AfterUpdatePassword(t.Context(), tx, recordingScope, recordedUser()))
		must.NoError(t, hooks.AfterRevokeSignIns(t.Context(), tx, recordingScope, &signin.Revocation{
			Reason: signin.RevocationSignOut, SubjectID: recordedUserID, FamilyIDs: []string{"family-1"},
		}))

		must.SliceLen(t, 3, l.scopes)

		for _, scope := range l.scopes {
			test.EqOp(t, tenancy.Of(recordedUserID), scope)
		}
	})
}

func TestRecordingHooks_ThroughTheService(T *testing.T) {
	T.Parallel()

	T.Run("a password login is recorded as the user, with nobody on the request", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		e := newEnv(t, signin.WithHooks(newSignInRecordingHooks(t, l, noCaller)))

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.SliceLen(t, 1, l.entries)
		test.Eq(t, audit.Actor{ID: e.user.ID, Type: audit.ActorUser}, l.entries[0].Actor)
		test.EqOp(t, e.user.ID, l.entries[0].ResourceID)
		test.EqOp(t, string(signin.CredentialKindPassword), l.entries[0].Metadata["credentialKind"])
		test.EqOp(t, testScope, l.scopes[0])
	})

	T.Run("an impersonation names the subject as actor and the operator as impersonator", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		e := newEnv(t,
			signin.WithImpersonationPolicy(admitAll),
			signin.WithHooks(newSignInRecordingHooks(t, l, aCaller)),
		)
		operator := e.newOperatorIn(t, staffScope)

		_, err := e.svc.IssueImpersonationToken(t.Context(), staffScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, l.entries)
		test.Eq(t, audit.Actor{ID: e.user.ID, Type: audit.ActorUser, Impersonator: operator.ID}, l.entries[0].Actor)
		test.EqOp(t, staffScope.String(), l.entries[0].Metadata["impersonatorScope"])
		test.EqOp(t, testScope, l.scopes[0])
	})
}
