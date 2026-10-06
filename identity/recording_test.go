package identity

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/searchsync"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v15/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The secrets the fixtures below carry, which no audit entry or event may
// mention.
const (
	recordingPasswordHash       = "argon2$secret-hash"
	recordingTwoFactorSecret    = "totp-secret"
	recordingVerificationToken  = "verification-secret"
	recordingVerificationDigest = "verification-digest"
	recordingInvitationToken    = "invitation-secret"
	recordingInvitationDigest   = "invitation-digest"
)

// recordingOperator is the caller every recorded write here is attributed to.
type recordingOperator struct{}

func (recordingOperator) UserID() string          { return "operator-1" }
func (recordingOperator) Scope() tenancy.Scope    { return testScope }
func (recordingOperator) ActiveAccountID() string { return "" }

func recordingPrincipal(context.Context) (callers.Principal, bool) { return recordingOperator{}, true }

// recordingLedger is everything the two halves were handed, in order.
type recordingLedger struct {
	refuse error
	// catalog is the dispatcher's, and nil means optedIn.
	catalog    webhooks.Catalog
	entries    []*audit.Entry
	deliveries []*webhooks.Delivery
	published  []outbox.Message
	// anonymous builds the Recorder over an extractor that finds nobody, as a
	// registration's own request does.
	anonymous bool
}

// optedIn is EventCatalog with Internal cleared on every entry, the catalog of a
// deployment that chose to deliver the credential events, so that a test reading
// what a hook dispatched has a delivery to read.
func optedIn() webhooks.Catalog {
	catalog := EventCatalog()
	for eventType, definition := range catalog {
		definition.Internal = false
		catalog[eventType] = definition
	}

	return catalog
}

// newRecordingHooksForTest builds RecordingHooks over mocks that write into the
// ledger, with the dispatcher's catalog the ledger's.
func newRecordingHooksForTest(t *testing.T, l *recordingLedger, opts ...recording.Option) *RecordingHooks {
	t.Helper()

	entries := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
			if l.refuse != nil {
				return l.refuse
			}

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

	principals := callers.PrincipalExtractor(recordingPrincipal)
	if l.anonymous {
		principals = func(context.Context) (callers.Principal, bool) { return nil, false }
	}

	recorder, err := recording.New(entries, emitter, principals, opts...)
	must.NoError(t, err)

	hooks, err := NewRecordingHooks(recorder)
	must.NoError(t, err)

	return hooks
}

// delivery is the one event the ledger holds, which every operation here
// produces exactly one of.
func (l *recordingLedger) delivery(t *testing.T) *webhooks.Delivery {
	t.Helper()

	must.SliceLen(t, 1, l.deliveries)

	return l.deliveries[0]
}

// noSecrets fails if any entry, any delivery or anything published to the
// outbox mentions a credential the fixtures carry.
//
// The events are held to it as well as the entries, and the verification and
// invitation tokens are the two it exists for: both reach the hooks, and an
// event goes wherever a deployment's catalog sends it, so a field carrying
// either back onto a payload is a link handed to every subscriber.
func (l *recordingLedger) noSecrets(t *testing.T) {
	t.Helper()

	entries, err := json.Marshal(l.entries)
	must.NoError(t, err)

	published, err := json.Marshal(l.published)
	must.NoError(t, err)

	rendered := []string{string(entries), string(published)}
	for _, delivery := range l.deliveries {
		rendered = append(rendered, string(delivery.Payload))
	}

	for _, secret := range []string{
		recordingPasswordHash, recordingTwoFactorSecret,
		recordingVerificationToken, recordingVerificationDigest,
		recordingInvitationToken, recordingInvitationDigest,
	} {
		for _, r := range rendered {
			test.StrNotContains(t, r, secret)
		}
	}
}

func decodeRecorded[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var event T
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

// recordingUser is a user carrying every credential a row can, so a case can
// check none of them reaches an entry.
func recordingUser(id string) *User {
	return &User{
		ID:                                  id,
		Scope:                               testScope,
		Username:                            id,
		EmailAddress:                        id + "@example.com",
		AccountStatus:                       StatusGood,
		HashedPassword:                      recordingPasswordHash,
		TwoFactorSecret:                     recordingTwoFactorSecret,
		EmailAddressVerificationToken:       recordingVerificationToken,
		EmailAddressVerificationTokenDigest: recordingVerificationDigest,
		ServiceRoles:                        []string{"support"},
	}
}

func recordingAccount(id, owner string) *Account {
	return &Account{ID: id, Scope: testScope, Name: "Acme", OwnerUserID: owner}
}

func recordingMembership(id, user, account string) *Membership {
	return &Membership{
		ID:               id,
		Scope:            testScope,
		BelongsToUser:    user,
		BelongsToAccount: account,
		Roles:            []string{"member"},
	}
}

func recordingInvitation(status InvitationStatus, toUser *string) *Invitation {
	return &Invitation{
		ID:               "invitation-1",
		Scope:            testScope,
		BelongsToAccount: "account-1",
		FromUser:         "user-1",
		ToUser:           toUser,
		ToEmail:          "guest@example.com",
		Status:           status,
		Token:            recordingInvitationToken,
		TokenDigest:      recordingInvitationDigest,
	}
}

// recordingCall is one Hooks method, called with fixtures.
type recordingCall func(ctx context.Context, h Hooks, tx database.Tx) error

// recordingCalls is every Hooks method, keyed by name, each called the way the
// Service would call it.
func recordingCalls() map[string]recordingCall {
	guest := "user-2"

	return map[string]recordingCall{
		"AfterRegister": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterRegister(ctx, tx, testScope, &Registration{
				User:                          recordingUser("user-1"),
				Account:                       recordingAccount("account-1", "user-1"),
				Membership:                    recordingMembership("membership-1", "user-1", "account-1"),
				EmailAddressVerificationToken: recordingVerificationToken,
			})
		},
		"AfterRegisterWithInvitation": func(ctx context.Context, h Hooks, tx database.Tx) error {
			invitation := recordingInvitation(InvitationAccepted, &guest)

			return h.AfterRegisterWithInvitation(ctx, tx, testScope, &InvitedRegistration{
				User:                          recordingUser(guest),
				Invitation:                    invitation,
				Membership:                    recordingMembership("membership-2", guest, "account-1"),
				EmailAddressVerificationToken: recordingVerificationToken,
			})
		},
		"AfterInvite": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterInvite(ctx, tx, testScope, recordingInvitation(InvitationPending, nil))
		},
		"AfterAcceptInvitation": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterAcceptInvitation(ctx, tx, testScope, &Acceptance{
				Invitation: recordingInvitation(InvitationAccepted, &guest),
				Membership: recordingMembership("membership-2", guest, "account-1"),
			})
		},
		"AfterRejectInvitation": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterRejectInvitation(ctx, tx, testScope, recordingInvitation(InvitationRejected, nil))
		},
		"AfterCancelInvitation": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterCancelInvitation(ctx, tx, testScope, recordingInvitation(InvitationCancelled, nil))
		},
		"AfterCreateAccount": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterCreateAccount(ctx, tx, testScope,
				recordingAccount("account-2", "user-1"), recordingMembership("membership-3", "user-1", "account-2"))
		},
		"AfterTransferAccountOwnership": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterTransferAccountOwnership(ctx, tx, testScope, recordingAccount("account-1", guest), "user-1")
		},
		"AfterSetDefaultAccount": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterSetDefaultAccount(ctx, tx, testScope,
				recordingMembership("membership-3", "user-1", "account-2"), "account-1")
		},
		"AfterArchiveUser": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterArchiveUser(ctx, tx, testScope, recordingUser("user-1").Redacted(), []*Membership{
				recordingMembership("membership-1", "user-1", "account-1"),
				recordingMembership("membership-3", "user-1", "account-2"),
			})
		},
		"AfterArchiveAccount": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterArchiveAccount(ctx, tx, testScope, recordingAccount("account-1", "user-1"), []*Membership{
				recordingMembership("membership-1", "user-1", "account-1"),
				recordingMembership("membership-2", guest, "account-1"),
			})
		},
		"AfterUpdateUserAccountStatus": func(ctx context.Context, h Hooks, tx database.Tx) error {
			before := recordingUser("user-1")
			after := recordingUser("user-1")
			after.AccountStatus = StatusBanned
			after.AccountStatusExplanation = "spam"

			return h.AfterUpdateUserAccountStatus(ctx, tx, testScope, before, after)
		},
		"AfterSetUserServiceRoles": func(ctx context.Context, h Hooks, tx database.Tx) error {
			user := recordingUser("user-1").Redacted()
			user.ServiceRoles = []string{"support", "admin"}

			return h.AfterSetUserServiceRoles(ctx, tx, testScope, user, []string{"support"})
		},
		"AfterUpdateProfile": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterUpdateProfile(ctx, tx, testScope, recordingUser("user-1").Redacted(),
				[]string{"lastName", "emailAddress"})
		},
		"AfterUpdateAccount": func(ctx context.Context, h Hooks, tx database.Tx) error {
			before := recordingAccount("account-1", "user-1")
			after := recordingAccount("account-1", "user-1")
			after.Name = "Renamed"
			stamped := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			after.LastUpdatedAt = &stamped

			return h.AfterUpdateAccount(ctx, tx, testScope, before, after)
		},
		"AfterRecordAgreement": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterRecordAgreement(ctx, tx, testScope, recordingUser("user-1").Redacted(),
				[]Agreement{TermsOfService, PrivacyPolicy})
		},
		"AfterSetMembershipRoles": func(ctx context.Context, h Hooks, tx database.Tx) error {
			membership := recordingMembership("membership-2", guest, "account-1")
			membership.Roles = []string{"admin", "member"}

			return h.AfterSetMembershipRoles(ctx, tx, testScope, membership, []string{"member"})
		},
		"AfterRemoveMembership": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterRemoveMembership(ctx, tx, testScope,
				recordingMembership("membership-1", "user-1", "account-1"), "account-2")
		},
		"AfterUpdateUserPassword": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterUpdateUserPassword(ctx, tx, testScope, recordingUser("user-1").Redacted(), true)
		},
		"AfterSetUserRequiresPasswordChange": func(ctx context.Context, h Hooks, tx database.Tx) error {
			user := recordingUser("user-1").Redacted()
			user.RequiresPasswordChange = false

			return h.AfterSetUserRequiresPasswordChange(ctx, tx, testScope, user)
		},
		"AfterUpdateUserTwoFactorSecret": func(ctx context.Context, h Hooks, tx database.Tx) error {
			proven := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

			return h.AfterUpdateUserTwoFactorSecret(ctx, tx, testScope, recordingUser("user-1").Redacted(), &proven)
		},
		"AfterMarkUserTwoFactorSecretVerified": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterMarkUserTwoFactorSecretVerified(ctx, tx, testScope, recordingUser("user-1").Redacted())
		},
		"AfterSetUserEmailAddressVerificationToken": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterSetUserEmailAddressVerificationToken(ctx, tx, testScope, recordingUser("user-1").Redacted())
		},
		"AfterMarkUserEmailAddressVerified": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterMarkUserEmailAddressVerified(ctx, tx, testScope, recordingUser("user-1").Redacted())
		},
		"AfterMarkUserEmailAddressUnverified": func(ctx context.Context, h Hooks, tx database.Tx) error {
			return h.AfterMarkUserEmailAddressUnverified(ctx, tx, testScope, recordingUser("user-1").Redacted())
		},
	}
}

// recordingBySubject is recordingcfg.FileBySubject's rule, spelled here so the
// tests need not build a Recorder through the config package.
func recordingBySubject(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
	if entry.SubjectID != "" {
		return tenancy.Of(entry.SubjectID)
	}

	return scope
}

// runRecording calls one Hooks method on fresh RecordingHooks inside a real
// transaction and hands back what it recorded.
func runRecording(t *testing.T, env *storeEnv, name string, opts ...recording.Option) *recordingLedger {
	t.Helper()

	l := &recordingLedger{}
	runRecordingInto(t, env, name, l, opts...)

	return l
}

// runRecordingInto is runRecording writing into a ledger the caller built, so
// the caller can choose the dispatcher's catalog.
func runRecordingInto(t *testing.T, env *storeEnv, name string, l *recordingLedger, opts ...recording.Option) {
	t.Helper()

	call, ok := recordingCalls()[name]
	must.True(t, ok, must.Sprintf("no recording call for %s", name))

	hooks := newRecordingHooksForTest(t, l, opts...)

	must.NoError(t, env.inTx(t, func(tx database.Tx) error { return call(t.Context(), hooks, tx) }))
}

func TestNewRecordingHooks(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil recorder by name", func(t *testing.T) {
		t.Parallel()

		hooks, err := NewRecordingHooks(nil)
		test.Nil(t, hooks)
		test.ErrorIs(t, err, ErrNilRecorder)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestEventCatalog(T *testing.T) {
	T.Parallel()

	T.Run("describes every event", func(t *testing.T) {
		t.Parallel()

		for eventType, description := range EventCatalog() {
			test.NotEqOp(t, "", description.Description, test.Sprintf("%s has no description", eventType))
		}
	})

	T.Run("marks every credential event Internal, and nothing else", func(t *testing.T) {
		t.Parallel()

		credential := map[webhooks.EventType]bool{
			EventUserPasswordChanged:                true,
			EventUserPasswordChangeRequirementSet:   true,
			EventUserTwoFactorSecretIssued:          true,
			EventUserTwoFactorSecretVerified:        true,
			EventUserEmailAddressVerificationIssued: true,
		}

		catalog := EventCatalog()
		for eventType := range credential {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
		}

		for eventType, definition := range catalog {
			test.EqOp(t, credential[eventType], definition.Internal, test.Sprintf("%s Internal", eventType))
			test.EqOp(t, !credential[eventType], catalog.Subscribable(eventType), test.Sprintf("%s subscribable", eventType))
		}
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventUserRegistered)
		test.True(t, EventCatalog().Known(EventUserRegistered))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every Hooks method records, and the catalog is exactly what they emit", func(t *testing.T) {
		t.Parallel()

		calls := recordingCalls()

		hooksType := reflect.TypeFor[Hooks]()
		methods := make([]string, 0, hooksType.NumMethod())
		for method := range hooksType.Methods() {
			methods = append(methods, method.Name)
		}

		names := make([]string, 0, len(calls))
		for name := range calls {
			names = append(names, name)
		}

		slices.Sort(names)
		test.Eq(t, methods, names)

		emitted := map[webhooks.EventType]bool{}

		for _, name := range names {
			l := runRecording(t, env, name)

			test.SliceNotEmpty(t, l.entries, test.Sprintf("%s recorded no entry", name))
			delivery := l.delivery(t)
			test.True(t, EventCatalog().Known(delivery.EventType), test.Sprintf("%s emitted %s", name, delivery.EventType))
			test.NotEqOp(t, "", delivery.OrderingKey)
			l.noSecrets(t)

			for _, entry := range l.entries {
				test.EqOp(t, "operator-1", entry.Actor.ID)
				test.EqOp(t, testScope, entry.Scope)
			}

			emitted[delivery.EventType] = true
		}

		for eventType := range EventCatalog() {
			test.True(t, emitted[eventType], test.Sprintf("%s is in the catalog and nothing emits it", eventType))
		}
	})

	T.Run("under EventCatalog every event is published, and only the subscribable ones are delivered", func(t *testing.T) {
		t.Parallel()

		catalog := EventCatalog()

		for name := range recordingCalls() {
			l := &recordingLedger{catalog: catalog}
			runRecordingInto(t, env, name, l)

			test.SliceNotEmpty(t, l.published, test.Sprintf("%s published nothing", name))

			for _, delivery := range l.deliveries {
				test.True(t, catalog.Subscribable(delivery.EventType), test.Sprintf("%s delivered %s", name, delivery.EventType))
			}

			opted := runRecording(t, env, name)
			eventType := opted.delivery(t).EventType
			test.EqOp(t, catalog.Subscribable(eventType), len(l.deliveries) > 0, test.Sprintf("%s emitted %s", name, eventType))
		}
	})

	T.Run("a registration records three rows and emits one event, without the token", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterRegister")

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, ResourceTypeUser, l.entries[0].ResourceType)
		test.EqOp(t, "user-1", l.entries[0].ResourceID)
		test.EqOp(t, ResourceTypeAccount, l.entries[1].ResourceType)
		test.EqOp(t, "account-1", l.entries[1].ResourceID)
		test.EqOp(t, ResourceTypeMembership, l.entries[2].ResourceType)
		test.EqOp(t, "membership-1", l.entries[2].ResourceID)
		test.Eq(t, map[string]string{metadataAccountID: "account-1"}, l.entries[2].Metadata)

		for _, entry := range l.entries {
			test.EqOp(t, audit.EventCreated, entry.EventType)
		}

		delivery := l.delivery(t)
		test.EqOp(t, EventUserRegistered, delivery.EventType)
		test.EqOp(t, "user-1", delivery.OrderingKey)
		test.Eq(t, &UserEvent{
			UserID:       "user-1",
			AccountID:    "account-1",
			MembershipID: "membership-1",
		}, decodeRecorded[UserEvent](t, delivery))
		test.StrNotContains(t, string(delivery.Payload), recordingPasswordHash)
		test.StrNotContains(t, string(delivery.Payload), recordingVerificationToken)
	})

	// UserEvent knows nothing of search and serves every user event, so it
	// cannot be a searchsync.Change. The envelope Emit wraps it in is one, and
	// a rule keyed by the payload's JSON field names matches it unchanged.
	T.Run("a registration feeds an index rule keyed by the payload's field names", func(t *testing.T) {
		t.Parallel()

		effect, err := searchsync.NewSideEffect([]searchsync.Rule{
			{EventType: EventUserRegistered.String(), Topic: "users-index", IDKey: "userID", Op: searchsync.OpUpsert},
			{EventType: EventUserRegistered.String(), Topic: "accounts-index", IDKey: "accountID", Op: searchsync.OpUpsert},
		})
		must.NoError(t, err)

		l := runRecording(t, env, "AfterRegister")
		must.SliceLen(t, 1, l.published)

		derived, err := effect(t.Context(), nil, l.published)
		must.NoError(t, err)
		must.SliceLen(t, 2, derived)

		test.EqOp(t, "users-index", derived[0].Topic)
		test.EqOp(t, "user-1", derived[0].Key)
		test.EqOp(t, "accounts-index", derived[1].Topic)
		test.EqOp(t, "account-1", derived[1].Key)
	})

	T.Run("a registration whose request carries nobody is the registrant's", func(t *testing.T) {
		t.Parallel()

		for name, registrant := range map[string]string{"AfterRegister": "user-1", "AfterRegisterWithInvitation": "user-2"} {
			l := &recordingLedger{anonymous: true}
			runRecordingInto(t, env, name, l)

			must.SliceLen(t, 3, l.entries, must.Sprintf("%s", name))

			for _, entry := range l.entries {
				test.Eq(t, audit.Actor{ID: registrant, Type: audit.ActorUser}, entry.Actor, test.Sprintf("%s", name))
			}

			test.EqOp(t, EventUserRegistered, l.delivery(t).EventType)
		}
	})

	T.Run("a registration whose request carries an operator is the operator's", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"AfterRegister", "AfterRegisterWithInvitation"} {
			l := runRecording(t, env, name)

			must.SliceLen(t, 3, l.entries, must.Sprintf("%s", name))

			for _, entry := range l.entries {
				test.EqOp(t, "operator-1", entry.Actor.ID, test.Sprintf("%s", name))
				test.EqOp(t, audit.ActorUser, entry.Actor.Type, test.Sprintf("%s", name))
			}
		}
	})

	T.Run("a registration by invitation records the invitation as accepted, not created", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterRegisterWithInvitation")

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, ResourceTypeUser, l.entries[0].ResourceType)
		test.EqOp(t, audit.EventCreated, l.entries[0].EventType)
		test.EqOp(t, ResourceTypeInvitation, l.entries[1].ResourceType)
		test.EqOp(t, audit.EventUpdated, l.entries[1].EventType)
		test.Eq(t, map[string]string{metadataAccountID: "account-1", metadataStatus: "accepted"}, l.entries[1].Metadata)
		test.EqOp(t, ResourceTypeMembership, l.entries[2].ResourceType)

		event := decodeRecorded[UserEvent](t, l.delivery(t))
		test.EqOp(t, EventUserRegistered, l.delivery(t).EventType)
		test.EqOp(t, "invitation-1", event.InvitationID)
		test.EqOp(t, "account-1", event.AccountID)
		test.StrNotContains(t, string(l.delivery(t).Payload), recordingVerificationToken)
	})

	T.Run("an issued invitation's token reaches neither the entry nor the event", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterInvite")

		must.SliceLen(t, 1, l.entries)
		test.EqOp(t, audit.EventCreated, l.entries[0].EventType)
		test.Eq(t, map[string]string{metadataAccountID: "account-1", metadataStatus: "pending"}, l.entries[0].Metadata)

		event := decodeRecorded[InvitationEvent](t, l.delivery(t))
		test.Nil(t, event.ToUser)
		test.StrNotContains(t, string(l.delivery(t).Payload), recordingInvitationToken)
		test.StrNotContains(t, string(l.delivery(t).Payload), "guest@example.com")
		test.StrNotContains(t, string(l.delivery(t).Payload), recordingInvitationDigest)
	})

	T.Run("no invitation's event carries a token", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"AfterInvite", "AfterAcceptInvitation", "AfterRejectInvitation", "AfterCancelInvitation"} {
			l := runRecording(t, env, name)
			test.StrNotContains(t, string(l.delivery(t).Payload), recordingInvitationToken)
		}
	})

	T.Run("an unanswered invitation is filed on its account's chain, not the write's", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"AfterInvite", "AfterRejectInvitation", "AfterCancelInvitation"} {
			l := runRecording(t, env, name, recording.WithScopeResolver(recordingBySubject))

			must.SliceLen(t, 1, l.entries, must.Sprintf("%s", name))
			test.EqOp(t, ResourceTypeInvitation, l.entries[0].ResourceType, test.Sprintf("%s", name))
			test.EqOp(t, tenancy.Of("account-1"), l.entries[0].Scope, test.Sprintf("%s", name))
		}
	})

	T.Run("an answered invitation is filed on its recipient's chain", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterAcceptInvitation", recording.WithScopeResolver(recordingBySubject))

		must.SliceLen(t, 2, l.entries)
		test.EqOp(t, ResourceTypeInvitation, l.entries[0].ResourceType)
		test.EqOp(t, tenancy.Of("user-2"), l.entries[0].Scope)
	})

	T.Run("an archival records an entry per ended membership, each on its member's chain", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterArchiveAccount", recording.WithScopeResolver(recordingBySubject))

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, ResourceTypeAccount, l.entries[0].ResourceType)
		test.EqOp(t, audit.EventArchived, l.entries[0].EventType)
		// The account's own end is on the account's chain, beside its members'.
		test.EqOp(t, tenancy.Of("account-1"), l.entries[0].Scope)
		test.EqOp(t, "membership-1", l.entries[1].ResourceID)
		test.EqOp(t, tenancy.Of("user-1"), l.entries[1].Scope)
		test.EqOp(t, "membership-2", l.entries[2].ResourceID)
		test.EqOp(t, tenancy.Of("user-2"), l.entries[2].Scope)

		for _, entry := range l.entries[1:] {
			test.EqOp(t, audit.EventArchived, entry.EventType)
			test.EqOp(t, "account-1", entry.Metadata[metadataAccountID])
		}

		event := decodeRecorded[AccountEvent](t, l.delivery(t))
		test.EqOp(t, EventAccountArchived, l.delivery(t).EventType)
		must.SliceLen(t, 2, event.EndedMemberships)
		test.EqOp(t, "user-2", event.EndedMemberships[1].UserID)
	})

	T.Run("every entry about an account is filed on the account's chain", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{
			"AfterRegister",
			"AfterCreateAccount",
			"AfterTransferAccountOwnership",
			"AfterArchiveAccount",
			"AfterUpdateAccount",
		} {
			l := runRecording(t, env, name, recording.WithScopeResolver(recordingBySubject))

			var accounts int
			for _, entry := range l.entries {
				if entry.ResourceType != ResourceTypeAccount || entry.Scope != tenancy.Of(entry.ResourceID) {
					continue
				}

				accounts++
			}

			test.EqOp(t, 1, accounts, test.Sprintf("%s", name))
		}
	})

	T.Run("an account's entry beside a membership's goes on a chain of its own", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterCreateAccount", recording.WithScopeResolver(recordingBySubject))

		must.SliceLen(t, 2, l.entries)
		test.EqOp(t, ResourceTypeAccount, l.entries[0].ResourceType)
		test.EqOp(t, tenancy.Of("account-2"), l.entries[0].Scope)
		test.EqOp(t, ResourceTypeMembership, l.entries[1].ResourceType)
		test.EqOp(t, tenancy.Of("user-1"), l.entries[1].Scope)
	})

	T.Run("a transfer is on the account's chain and both owners'", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterTransferAccountOwnership", recording.WithScopeResolver(recordingBySubject))

		must.SliceLen(t, 3, l.entries)

		for i, scope := range []tenancy.Scope{tenancy.Of("account-1"), tenancy.Of("user-2"), tenancy.Of("user-1")} {
			test.EqOp(t, scope, l.entries[i].Scope)
			test.EqOp(t, ResourceTypeAccount, l.entries[i].ResourceType)
			test.EqOp(t, "account-1", l.entries[i].ResourceID)
			test.EqOp(t, audit.EventUpdated, l.entries[i].EventType)
			test.Eq(t, map[string]string{metadataPreviousOwnerUserID: "user-1"}, l.entries[i].Metadata)
		}

		// Filed by write, the three are the same write's, on its chain.
		for _, entry := range runRecording(t, env, "AfterTransferAccountOwnership").entries {
			test.EqOp(t, testScope, entry.Scope)
		}
	})

	T.Run("a user's archival names the memberships it ended", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterArchiveUser")

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, ResourceTypeUser, l.entries[0].ResourceType)
		test.EqOp(t, audit.EventArchived, l.entries[0].EventType)
		test.EqOp(t, ResourceTypeMembership, l.entries[1].ResourceType)
		test.EqOp(t, ResourceTypeMembership, l.entries[2].ResourceType)

		event := decodeRecorded[UserEvent](t, l.delivery(t))
		must.SliceLen(t, 2, event.EndedMemberships)
		test.EqOp(t, "account-2", event.EndedMemberships[1].AccountID)
	})

	T.Run("previous values the column no longer holds go in the metadata", func(t *testing.T) {
		t.Parallel()

		for name, want := range map[string]map[string]string{
			"AfterSetDefaultAccount":         {metadataAccountID: "account-2", metadataPreviousAccountID: "account-1"},
			"AfterRemoveMembership":          {metadataAccountID: "account-1", metadataNewDefaultAccountID: "account-2"},
			"AfterSetUserServiceRoles":       {metadataPreviousRoles: "support", metadataNewRoles: "admin,support"},
			"AfterSetMembershipRoles":        {metadataAccountID: "account-1", metadataPreviousRoles: "member", metadataNewRoles: "admin,member"},
			"AfterUpdateUserPassword":        {metadataSatisfiedRequiredChange: "true"},
			"AfterUpdateUserTwoFactorSecret": {metadataReplacedVerifiedSecret: "true"},
			"AfterSetUserRequiresPasswordChange": {
				metadataRequiresPasswordChange: "false",
			},
		} {
			l := runRecording(t, env, name)
			must.SliceLen(t, 1, l.entries)
			test.Eq(t, want, l.entries[0].Metadata, test.Sprintf("%s", name))
			test.MapEmpty(t, l.entries[0].Changes, test.Sprintf("%s", name))
		}
	})

	T.Run("an event carries the values the entry's metadata does", func(t *testing.T) {
		t.Parallel()

		roles := decodeRecorded[MembershipEvent](t, runRecording(t, env, "AfterSetMembershipRoles").delivery(t))
		test.Eq(t, []string{"admin", "member"}, roles.Roles)
		test.Eq(t, []string{"member"}, roles.PreviousRoles)

		transfer := decodeRecorded[AccountEvent](t, runRecording(t, env, "AfterTransferAccountOwnership").delivery(t))
		test.EqOp(t, "user-2", transfer.OwnerUserID)
		test.EqOp(t, "user-1", transfer.PreviousOwnerUserID)

		password := decodeRecorded[UserEvent](t, runRecording(t, env, "AfterUpdateUserPassword").delivery(t))
		test.True(t, password.SatisfiedRequiredChange)

		released := decodeRecorded[UserEvent](t, runRecording(t, env, "AfterSetUserRequiresPasswordChange").delivery(t))
		must.NotNil(t, released.RequiresPasswordChange)
		test.False(t, *released.RequiresPasswordChange)

		secret := decodeRecorded[UserEvent](t, runRecording(t, env, "AfterUpdateUserTwoFactorSecret").delivery(t))
		test.True(t, secret.ReplacedVerifiedSecret)
	})

	T.Run("a profile save records names, never values", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterUpdateProfile")

		must.SliceLen(t, 1, l.entries)
		test.MapEmpty(t, l.entries[0].Changes)
		test.Eq(t, map[string]string{metadataChanged: "emailAddress,lastName"}, l.entries[0].Metadata)
		test.Eq(t, []string{"emailAddress", "lastName"}, decodeRecorded[UserEvent](t, l.delivery(t)).Changed)
		test.StrNotContains(t, string(l.delivery(t).Payload), "user-1@example.com")
	})

	T.Run("an account save records the diff, and its event drops the stamp", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterUpdateAccount")

		must.SliceLen(t, 1, l.entries)
		test.EqOp(t, "Acme", l.entries[0].Changes["name"].Old)
		test.EqOp(t, "Renamed", l.entries[0].Changes["name"].New)
		test.MapContainsKey(t, l.entries[0].Changes, lastUpdatedAtField)
		test.Eq(t, []string{"name"}, decodeRecorded[AccountEvent](t, l.delivery(t)).Changed)
	})

	T.Run("a status change records the diff of both rows, credentials excluded", func(t *testing.T) {
		t.Parallel()

		l := runRecording(t, env, "AfterUpdateUserAccountStatus")

		must.SliceLen(t, 1, l.entries)
		test.EqOp(t, "spam", l.entries[0].Changes["accountStatusExplanation"].New)
		test.Eq[any](t, StatusGood, l.entries[0].Changes["accountStatus"].Old)
		test.Eq[any](t, StatusBanned, l.entries[0].Changes["accountStatus"].New)

		event := decodeRecorded[UserEvent](t, l.delivery(t))
		test.EqOp(t, StatusBanned, event.AccountStatus)
		test.EqOp(t, StatusGood, event.PreviousAccountStatus)
		test.Eq(t, []string{"accountStatus", "accountStatusExplanation"}, event.Changed)
	})

	T.Run("the credential hooks record that the credential moved and nothing else", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{
			"AfterMarkUserTwoFactorSecretVerified",
			"AfterSetUserEmailAddressVerificationToken",
			"AfterMarkUserEmailAddressVerified",
			"AfterMarkUserEmailAddressUnverified",
		} {
			l := runRecording(t, env, name)
			must.SliceLen(t, 1, l.entries)
			test.EqOp(t, ResourceTypeUser, l.entries[0].ResourceType)
			test.MapEmpty(t, l.entries[0].Metadata)
			test.MapEmpty(t, l.entries[0].Changes)
			test.Eq(t, &UserEvent{UserID: "user-1"}, decodeRecorded[UserEvent](t, l.delivery(t)))
		}
	})

	T.Run("nil arguments are refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooksForTest(t, &recordingLedger{})
		ctx := t.Context()

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			test.ErrorIs(t, hooks.AfterRegister(ctx, tx, testScope, nil), ErrNilUser)
			test.ErrorIs(t, hooks.AfterRegister(ctx, tx, testScope, &Registration{User: &User{}}), ErrNilAccount)
			test.ErrorIs(t, hooks.AfterInvite(ctx, tx, testScope, nil), ErrNilInvitation)
			test.ErrorIs(t, hooks.AfterSetMembershipRoles(ctx, tx, testScope, nil, nil), ErrNilMembership)
			test.ErrorIs(t, hooks.AfterUpdateAccount(ctx, tx, testScope, nil, &Account{}), ErrNilAccount)
			test.ErrorIs(t, hooks.AfterArchiveUser(ctx, tx, testScope, &User{}, []*Membership{nil}), ErrNilMembership)
			test.ErrorIs(t, hooks.AfterMarkUserEmailAddressVerified(ctx, tx, testScope, nil), ErrNilUser)

			return nil
		}))
	})

	T.Run("a refused recording fails the operation, and the registration with it", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{refuse: platformerrors.New("the log said no")}
		service, store := env.newService(t, newRecordingHooksForTest(t, l))

		user := newUser("ada")
		_, err := service.Register(t.Context(), testScope, user, newAccount("Ada's account", ""), []string{"admin"})
		test.ErrorIs(t, err, l.refuse)
		test.SliceEmpty(t, l.deliveries)

		_, err = store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		test.ErrorIs(t, err, ErrUserNotFound)
	})

	T.Run("a registration through the Service records what the store wrote", func(t *testing.T) {
		t.Parallel()

		l := &recordingLedger{}
		service, _ := env.newService(t, newRecordingHooksForTest(t, l))

		registration := registerAda(t, service, "ada")

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, registration.User.ID, l.entries[0].ResourceID)
		test.EqOp(t, registration.Account.ID, l.entries[1].ResourceID)
		test.EqOp(t, registration.Membership.ID, l.entries[2].ResourceID)
		test.EqOp(t, EventUserRegistered, l.delivery(t).EventType)
		l.noSecrets(t)
		test.StrNotContains(t, string(l.delivery(t).Payload), "argon2$ada")
	})
}

func TestInvitationEntry(T *testing.T) {
	T.Parallel()

	guest := "user-2"

	for name, tc := range map[string]struct {
		toUser  *string
		subject string
		status  InvitationStatus
	}{
		"pending":   {status: InvitationPending, subject: "account-1"},
		"cancelled": {status: InvitationCancelled, subject: "account-1"},
		"rejected":  {status: InvitationRejected, subject: "account-1"},
		"accepted":  {status: InvitationAccepted, toUser: &guest, subject: guest},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			entry := invitationEntry(recordingInvitation(tc.status, tc.toUser), audit.EventUpdated)

			test.EqOp(t, tc.subject, entry.SubjectID)
			test.EqOp(t, "invitation-1", entry.ResourceID)
			test.Eq(t, map[string]string{metadataAccountID: "account-1", metadataStatus: tc.status.String()}, entry.Metadata)
		})
	}
}
