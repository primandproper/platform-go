package sync

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/billing/standing"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testScope = tenancy.Of("acme")

const (
	testAccount        = "account-1"
	testProduct        = "product-1"
	testExternalID     = "sub_KLWtQ1"
	testSubscriptionID = "subscription-1"
)

// periodStart and periodEnd are the window a delivery reports, and they are the
// whole point of most of these cases: what the store is handed has to be these
// two instants and not something derived from a clock.
var (
	periodStart = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	periodEnd   = time.Date(2027, 9, 1, 12, 0, 0, 0, time.UTC)
)

// stubExecutor stands in for the executor a Tx runs on.
//
// Nothing executes through it: the store beneath the syncer is a mock and a
// Place is a test's own function, so the only thing the transaction is used for
// is being passed down and compared.
type stubExecutor struct{}

var _ database.SQLQueryExecutor = (*stubExecutor)(nil)

func (*stubExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the syncer's store is a mock; nothing runs on this")
}

func testTx() database.Tx { return database.NewTxForTesting(&stubExecutor{}) }

// fixedPlace is the Place most cases want: the one account and the one product
// this deployment has.
func fixedPlace() Place {
	return func(
		context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
	) (*Placement, error) {
		return &Placement{AccountID: testAccount, ProductID: testProduct}, nil
	}
}

// storedSubscription is the row a store holds for the agreement these cases
// report on.
func storedSubscription(status capitalism.SubscriptionStatus) *billing.Subscription {
	return &billing.Subscription{
		ID:                     testSubscriptionID,
		BelongsToAccount:       testAccount,
		ProductID:              testProduct,
		ExternalSubscriptionID: testExternalID,
		Status:                 status,
		CurrentPeriodStart:     periodStart,
		CurrentPeriodEnd:       periodEnd,
		Scope:                  testScope,
	}
}

// delivery is one subscription event, in a status, over a window.
func delivery(status capitalism.SubscriptionStatus, start, end *time.Time) *capitalism.Event {
	return &capitalism.Event{
		ID:   "evt_1",
		Type: "customer.subscription.updated",
		Subscription: &capitalism.SubscriptionState{
			ID:                 testExternalID,
			CustomerID:         "cus_1",
			Status:             status,
			ProviderStatus:     string(status),
			CurrentPeriodStart: start,
			CurrentPeriodEnd:   end,
		},
	}
}

// absent is a store holding no agreement for the delivery's identifier.
func absent() *billingmock.SubscriptionStoreMock {
	return &billingmock.SubscriptionStoreMock{
		GetSubscriptionByExternalIDFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return nil, billing.ErrSubscriptionNotFound
		},
	}
}

// holding is a store holding one agreement in a status.
func holding(status capitalism.SubscriptionStatus) *billingmock.SubscriptionStoreMock {
	stored := storedSubscription(status)

	return &billingmock.SubscriptionStoreMock{
		GetSubscriptionByExternalIDFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return stored, nil
		},
	}
}

func newTestSyncer(t *testing.T, store billing.SubscriptionStore, opts ...Option) *Syncer {
	t.Helper()

	s, err := New(store, fixedPlace(), opts...)
	must.NoError(t, err)

	return s
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		s, err := New(&billingmock.SubscriptionStoreMock{}, fixedPlace())
		must.NoError(t, err)
		must.NotNil(t, s)
		test.NotNil(t, s.instruments)
		test.NotNil(t, s.deliveries)
	})

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		s, err := New(nil, fixedPlace())
		test.Nil(t, s)
		test.ErrorIs(t, err, ErrNilStore)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a nil place", func(t *testing.T) {
		t.Parallel()

		var absentPlace Place

		s, err := New(&billingmock.SubscriptionStoreMock{}, absentPlace)
		test.Nil(t, s)
		test.ErrorIs(t, err, ErrNilPlace)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// Half a standing seam is refused at wiring time rather than panicking
	// inside somebody's webhook endpoint on the first delivery that moves a row.
	T.Run("refuses a standing writer with no classifier", func(t *testing.T) {
		t.Parallel()

		s, err := New(&billingmock.SubscriptionStoreMock{}, fixedPlace(),
			WithStanding(&identitymock.StoreMock{}, nil))
		test.Nil(t, s)
		test.ErrorIs(t, err, ErrNilClassify)
	})

	T.Run("refuses a classifier with no standing writer", func(t *testing.T) {
		t.Parallel()

		s, err := New(&billingmock.SubscriptionStoreMock{}, fixedPlace(),
			WithStanding(nil, standing.Strict))
		test.Nil(t, s)
		test.ErrorIs(t, err, ErrNilBillingWriter)
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		var absentOption Option

		s, err := New(&billingmock.SubscriptionStoreMock{}, fixedPlace(), absentOption)
		must.NoError(t, err)
		must.NotNil(t, s)
	})
}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithLogger", func(t *testing.T) {
		t.Parallel()

		s := &Syncer{}

		WithLogger(logging.EnsureLogger(nil))(s)
		test.NotNil(t, s.logger)
	})

	T.Run("WithTracerProvider", func(t *testing.T) {
		t.Parallel()

		s := &Syncer{}

		provider := tracingnoop.NewTracerProvider()
		WithTracerProvider(provider)(s)
		test.True(t, s.tracerProvider == provider)
	})

	T.Run("WithMetricsProvider", func(t *testing.T) {
		t.Parallel()

		s := &Syncer{}

		WithMetricsProvider(metrics.EnsureMetricsProvider(nil))(s)
		test.NotNil(t, s.metricsProvider)
	})

	T.Run("WithPillars", func(t *testing.T) {
		t.Parallel()

		s := &Syncer{}

		WithPillars(nil)(s)
		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("WithStanding", func(t *testing.T) {
		t.Parallel()

		s := &Syncer{}
		writer := &identitymock.StoreMock{}

		WithStanding(writer, standing.Strict)(s)
		test.NotNil(t, s.accounts)
		test.NotNil(t, s.classify)
	})
}

func TestSyncer_Apply_refusals(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil transaction", func(t *testing.T) {
		t.Parallel()

		result, err := newTestSyncer(t, absent()).
			Apply(t.Context(), nil, testScope, delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("refuses a nil event", func(t *testing.T) {
		t.Parallel()

		result, err := newTestSyncer(t, absent()).Apply(t.Context(), testTx(), testScope, nil)
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrNilEvent)
	})

	T.Run("refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		result, err := newTestSyncer(t, absent()).Apply(t.Context(), testTx(), tenancy.Scope{},
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.Error(t, err)
	})

	// Nothing to look the agreement up by, and nothing a later delivery could
	// match a row written anyway against.
	T.Run("refuses a subscription with no provider-side id", func(t *testing.T) {
		t.Parallel()

		store := absent()
		event := delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd)
		event.Subscription.ID = ""

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope, event)
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrUnidentifiedSubscription)
		test.SliceLen(t, 0, store.GetSubscriptionByExternalIDCalls())
	})
}

// TestSyncer_Apply_acknowledges covers the three deliveries that are not
// failures: not a subscription, not a placeable status, and not news.
func TestSyncer_Apply_acknowledges(T *testing.T) {
	T.Parallel()

	T.Run("an event carrying no subscription reads nothing", func(t *testing.T) {
		t.Parallel()

		store := absent()

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope, &capitalism.Event{
			ID:      "evt_2",
			Type:    "payment_intent.succeeded",
			Payload: []byte(`{}`),
		})
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeIgnored, result.Outcome)
		test.Nil(t, result.Subscription)
		test.SliceLen(t, 0, store.GetSubscriptionByExternalIDCalls())
	})

	// The reading the hand-written version gets wrong. A status nobody placed is
	// not Active, and it is not any other standing either.
	T.Run("a status-less delivery changes nothing", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		event := delivery(capitalism.SubscriptionStatusUnknown, &periodStart, &periodEnd)
		event.Subscription.ProviderStatus = ""

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope, event)
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUnplaced, result.Outcome)
		test.Nil(t, result.Subscription)

		// Not read, not created, not updated, and its status not moved: the
		// store is untouched by a delivery nobody could place.
		test.SliceLen(t, 0, store.GetSubscriptionByExternalIDCalls())
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
		test.SliceLen(t, 0, store.UpdateSubscriptionCalls())
		test.SliceLen(t, 0, store.SetSubscriptionStatusCalls())
	})

	// The same refusal for a status a provider added after this module was
	// built, which is what the unplaced path is actually for.
	T.Run("a status no adapter could place changes nothing", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		event := delivery(capitalism.SubscriptionStatusUnknown, &periodStart, &periodEnd)
		event.Subscription.ProviderStatus = "gracefully_lapsing"

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope, event)
		must.NoError(t, err)
		test.EqOp(t, OutcomeUnplaced, result.Outcome)
		test.SliceLen(t, 0, store.SetSubscriptionStatusCalls())
	})

	// A status-less delivery must not write an account's standing either, which
	// is the half of the same mistake that locks somebody out.
	T.Run("a status-less delivery writes no standing", func(t *testing.T) {
		t.Parallel()

		accounts := &identitymock.StoreMock{}
		event := delivery(capitalism.SubscriptionStatusUnknown, &periodStart, &periodEnd)

		result, err := newTestSyncer(t, holding(capitalism.SubscriptionStatusActive),
			WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope, event)
		must.NoError(t, err)
		test.EqOp(t, OutcomeUnplaced, result.Outcome)
		test.Nil(t, result.Standing)
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionCalls())
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionEndedCalls())
	})

	T.Run("a delivery reporting what the row already says writes nothing", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUnchanged, result.Outcome)
		must.NotNil(t, result.Subscription)
		test.EqOp(t, testSubscriptionID, result.Subscription.ID)
		test.SliceLen(t, 0, store.SetSubscriptionStatusCalls())
		test.SliceLen(t, 0, store.UpdateSubscriptionCalls())
	})

	// The same delivery arriving twice, seen through a column that did not keep
	// the whole instant. SQLite holds these to the second, so the row a
	// redelivery is compared against differs from the event in the fraction the
	// column dropped — and an exact comparison would call that a renewal, write
	// the row, and re-stamp the standing that the unchanged reading exists to
	// leave alone.
	T.Run("a redelivery whose period lost its fraction in the column is unchanged", func(t *testing.T) {
		t.Parallel()

		var (
			store    = holding(capitalism.SubscriptionStatusActive)
			accounts = &identitymock.StoreMock{}
		)

		// What the provider sent; what the row kept is periodStart, truncated.
		var (
			sentStart = periodStart.Add(789 * time.Millisecond)
			sentEnd   = periodEnd.Add(123 * time.Millisecond)
		)

		result, err := newTestSyncer(t, store, WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusActive, &sentStart, &sentEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUnchanged, result.Outcome)
		test.SliceLen(t, 0, store.UpdateSubscriptionCalls())
		test.SliceLen(t, 0, store.SetSubscriptionStatusCalls())

		// The half the exact comparison would have got wrong quietly.
		test.Nil(t, result.Standing)
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionCalls())
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionEndedCalls())
	})

	// The store's own guard, reached when a concurrent delivery moved the status
	// between this sync's read and its write. ErrStatusUnchanged is the
	// acknowledgement it is spelled as an error.
	T.Run("a store answering ErrStatusUnchanged is acknowledged", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return platformerrors.Wrapf(billing.ErrStatusUnchanged, "subscription %q", testSubscriptionID)
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusPastDue, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUnchanged, result.Outcome)
		test.SliceLen(t, 1, store.SetSubscriptionStatusCalls())

		// No read-back: nothing was written, so the row the reader already holds
		// is the row the transaction will commit.
		test.SliceLen(t, 0, store.GetSubscriptionCalls())
	})
}

func TestSyncer_Apply_opens(T *testing.T) {
	T.Parallel()

	// The period is copied rather than derived, which is the whole reason
	// capitalism carries it.
	T.Run("opens an agreement over the reported period", func(t *testing.T) {
		t.Parallel()

		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, scope tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			created := *subscription
			created.ID = testSubscriptionID
			created.Scope = scope

			return &created, nil
		}

		tx := testTx()

		result, err := newTestSyncer(t, store).Apply(t.Context(), tx, testScope,
			delivery(capitalism.SubscriptionStatusTrialing, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeCreated, result.Outcome)

		// The lookup runs on the caller's transaction rather than a replica, so
		// a subscription that transaction wrote a moment ago is one this finds.
		must.SliceLen(t, 1, store.GetSubscriptionByExternalIDCalls())
		test.True(t, store.GetSubscriptionByExternalIDCalls()[0].Q == database.SQLQueryExecutor(tx))

		must.SliceLen(t, 1, store.CreateSubscriptionCalls())
		written := store.CreateSubscriptionCalls()[0].Subscription
		test.EqOp(t, testAccount, written.BelongsToAccount)
		test.EqOp(t, testProduct, written.ProductID)
		test.EqOp(t, testExternalID, written.ExternalSubscriptionID)
		test.EqOp(t, capitalism.SubscriptionStatusTrialing, written.Status)
		test.True(t, periodStart.Equal(written.CurrentPeriodStart))
		test.True(t, periodEnd.Equal(written.CurrentPeriodEnd))
	})

	// A period reported in another zone is stored in UTC, because two machines
	// decoding the same delivery must not store two different instants.
	T.Run("stores the reported period in UTC", func(t *testing.T) {
		t.Parallel()

		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		zone := time.FixedZone("UTC-7", -7*60*60)
		start := periodStart.In(zone)
		end := periodEnd.In(zone)

		_, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &start, &end))
		must.NoError(t, err)

		must.SliceLen(t, 1, store.CreateSubscriptionCalls())
		written := store.CreateSubscriptionCalls()[0].Subscription
		test.EqOp(t, time.UTC, written.CurrentPeriodStart.Location())
		test.True(t, periodStart.Equal(written.CurrentPeriodStart))
	})

	// The refusal this package exists for: no period, and none invented.
	T.Run("refuses to open an agreement with no reported period", func(t *testing.T) {
		t.Parallel()

		store := absent()
		placed := 0
		s, err := New(store, func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
		) (*Placement, error) {
			placed++

			return &Placement{AccountID: testAccount, ProductID: testProduct}, nil
		})
		must.NoError(t, err)

		result, err := s.Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, nil, nil))
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrNoPaidPeriod)
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
		test.EqOp(t, 0, placed)
	})

	// A perpetual entitlement — a start and no end — is the half-bounded period
	// capitalism documents, and it is refused rather than given an end.
	T.Run("refuses to open an agreement with a half-bounded period", func(t *testing.T) {
		t.Parallel()

		store := absent()

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, nil))
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrNoPaidPeriod)
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
	})

	T.Run("hands the place the transaction and the scope", func(t *testing.T) {
		t.Parallel()

		tx := testTx()
		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		var (
			gotExecutor database.SQLQueryExecutor
			gotScope    tenancy.Scope
			gotState    *capitalism.SubscriptionState
		)

		s, err := New(store, func(
			_ context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			state *capitalism.SubscriptionState,
		) (*Placement, error) {
			gotExecutor, gotScope, gotState = q, scope, state

			return &Placement{AccountID: testAccount, ProductID: testProduct}, nil
		})
		must.NoError(t, err)

		_, err = s.Apply(t.Context(), tx, testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		must.NoError(t, err)

		// The caller's transaction, so a placement reading this deployment's own
		// tables sees what that transaction has already written.
		test.True(t, gotExecutor == database.SQLQueryExecutor(tx))
		test.EqOp(t, testScope, gotScope)
		must.NotNil(t, gotState)
		test.EqOp(t, testExternalID, gotState.ID)
	})

	T.Run("returns a place's refusal", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("no account holds that customer")
		store := absent()

		s, err := New(store, func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
		) (*Placement, error) {
			return nil, expected
		})
		must.NoError(t, err)

		result, err := s.Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, expected)
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
	})

	T.Run("refuses a place that answered with nothing", func(t *testing.T) {
		t.Parallel()

		store := absent()

		s, err := New(store, func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
		) (*Placement, error) {
			return nil, nil
		})
		must.NoError(t, err)

		result, err := s.Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, ErrNoPlacement)
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
	})

	T.Run("returns the store's refusal", func(t *testing.T) {
		t.Parallel()

		store := absent()
		store.CreateSubscriptionFunc = func(
			context.Context, database.Tx, tenancy.Scope, *billing.Subscription,
		) (*billing.Subscription, error) {
			return nil, billing.ErrSubscriptionExists
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, billing.ErrSubscriptionExists)
	})

	T.Run("returns a read failure that is not an absence", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("the database went away")
		store := &billingmock.SubscriptionStoreMock{
			GetSubscriptionByExternalIDFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
			) (*billing.Subscription, error) {
				return nil, expected
			},
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, expected)
		test.SliceLen(t, 0, store.CreateSubscriptionCalls())
	})
}

func TestSyncer_Apply_moves(T *testing.T) {
	T.Parallel()

	T.Run("moves a status and reads the row back", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return nil
		}
		store.GetSubscriptionFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			moved := storedSubscription(capitalism.SubscriptionStatusPastDue)
			moved.LastUpdatedAt = pointer.To(periodStart.Add(time.Hour))

			return moved, nil
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusPastDue, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUpdated, result.Outcome)

		must.SliceLen(t, 1, store.SetSubscriptionStatusCalls())
		test.EqOp(t, testSubscriptionID, store.SetSubscriptionStatusCalls()[0].SubscriptionID)
		test.EqOp(t, capitalism.SubscriptionStatusPastDue, store.SetSubscriptionStatusCalls()[0].Status)

		// The row as the transaction will commit it, stamp included, rather
		// than the pre-image with one field edited.
		must.SliceLen(t, 1, store.GetSubscriptionCalls())
		must.NotNil(t, result.Subscription)
		test.EqOp(t, capitalism.SubscriptionStatusPastDue, result.Subscription.Status)
		test.NotNil(t, result.Subscription.LastUpdatedAt)

		// A status move is one statement: nothing here rewrites the period.
		test.SliceLen(t, 0, store.UpdateSubscriptionCalls())
	})

	// The renewal. A row left on last month's window lapses out of the current
	// reads while the customer is paying.
	T.Run("advances a paid period that moved", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.UpdateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		renewedStart := periodEnd
		renewedEnd := periodEnd.AddDate(1, 0, 0)

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &renewedStart, &renewedEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUpdated, result.Outcome)

		must.SliceLen(t, 1, store.UpdateSubscriptionCalls())
		written := store.UpdateSubscriptionCalls()[0].Subscription
		test.True(t, renewedStart.Equal(written.CurrentPeriodStart))
		test.True(t, renewedEnd.Equal(written.CurrentPeriodEnd))

		// The account and the product are the row's, not the delivery's: neither
		// is a thing a processor moves.
		test.EqOp(t, testAccount, written.BelongsToAccount)
		test.EqOp(t, testProduct, written.ProductID)

		// One statement, not two. A period write followed by a status write
		// leaves a window in which the row says the new period at the old
		// status.
		test.SliceLen(t, 0, store.SetSubscriptionStatusCalls())
	})

	// The counterweight to the redelivery the acknowledgements pin: the
	// comparison is coarsened to what a column can hold, not softened into a
	// tolerance, so the smallest move any dialect can store is still a move.
	T.Run("advances a period that moved by the smallest storable step", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.UpdateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		var (
			movedStart = periodStart.Add(time.Second)
			movedEnd   = periodEnd.Add(time.Second)
		)

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &movedStart, &movedEnd))
		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, OutcomeUpdated, result.Outcome)

		// Written as it arrived. The truncation decides whether the row moved,
		// never what is stored in it.
		must.SliceLen(t, 1, store.UpdateSubscriptionCalls())
		written := store.UpdateSubscriptionCalls()[0].Subscription
		test.True(t, movedStart.Equal(written.CurrentPeriodStart))
		test.True(t, movedEnd.Equal(written.CurrentPeriodEnd))
	})

	T.Run("carries a status move into the period write", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusTrialing)
		store.UpdateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		renewedStart := periodEnd
		renewedEnd := periodEnd.AddDate(1, 0, 0)

		_, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &renewedStart, &renewedEnd))
		must.NoError(t, err)

		must.SliceLen(t, 1, store.UpdateSubscriptionCalls())
		test.EqOp(t, capitalism.SubscriptionStatusActive,
			store.UpdateSubscriptionCalls()[0].Subscription.Status)
	})

	// Half a window cannot replace a whole one without inventing the other end,
	// which is the same refusal the create path makes.
	T.Run("leaves a stored period alone for a half-bounded delivery", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return nil
		}
		store.GetSubscriptionFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return storedSubscription(capitalism.SubscriptionStatusCanceled), nil
		}

		moved := periodEnd.AddDate(1, 0, 0)

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusCanceled, &moved, nil))
		must.NoError(t, err)
		test.EqOp(t, OutcomeUpdated, result.Outcome)
		test.SliceLen(t, 0, store.UpdateSubscriptionCalls())
		test.SliceLen(t, 1, store.SetSubscriptionStatusCalls())
	})

	T.Run("returns a status write's refusal", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("the row is locked by somebody else")
		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return expected
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusUnpaid, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, expected)
	})

	// The row is written by then, so the refusal has to reach the caller and take
	// the transaction down with it rather than answering with a row nobody read.
	T.Run("returns a read-back failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("the read-back went nowhere")
		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return nil
		}
		store.GetSubscriptionFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return nil, expected
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusUnpaid, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, expected)
	})

	T.Run("returns a period write's refusal", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.UpdateSubscriptionFunc = func(
			context.Context, database.Tx, tenancy.Scope, *billing.Subscription,
		) (*billing.Subscription, error) {
			return nil, billing.ErrSubscriptionNotFound
		}

		renewedStart := periodEnd
		renewedEnd := periodEnd.AddDate(1, 0, 0)

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &renewedStart, &renewedEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, billing.ErrSubscriptionNotFound)
	})
}

func TestSyncer_Apply_standing(T *testing.T) {
	T.Parallel()

	T.Run("records the standing beside the row it opened", func(t *testing.T) {
		t.Parallel()

		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			created := *subscription
			created.ID = testSubscriptionID

			return &created, nil
		}

		accounts := &identitymock.StoreMock{
			RecordAccountSubscriptionFunc: func(
				context.Context, database.Tx, tenancy.Scope, string, identity.BillingStatus, string,
			) error {
				return nil
			},
		}

		result, err := newTestSyncer(t, store, WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusTrialing, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result.Standing)
		test.EqOp(t, identity.BillingTrial, *result.Standing)

		must.SliceLen(t, 1, accounts.RecordAccountSubscriptionCalls())
		recorded := accounts.RecordAccountSubscriptionCalls()[0]
		test.EqOp(t, testAccount, recorded.AccountID)
		test.EqOp(t, identity.BillingTrial, recorded.Status)
		test.EqOp(t, testProduct, recorded.PlanID)
	})

	// A terminal status is the other identity write, and choosing the wrong one
	// leaves an account on a plan it stopped paying for.
	T.Run("records an ended subscription through the ending write", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return nil
		}
		store.GetSubscriptionFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return storedSubscription(capitalism.SubscriptionStatusCanceled), nil
		}

		accounts := &identitymock.StoreMock{
			RecordAccountSubscriptionEndedFunc: func(
				context.Context, database.Tx, tenancy.Scope, string, identity.BillingStatus,
			) error {
				return nil
			},
		}

		result, err := newTestSyncer(t, store, WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusCanceled, &periodStart, &periodEnd))
		must.NoError(t, err)
		must.NotNil(t, result.Standing)
		test.EqOp(t, identity.BillingUnpaid, *result.Standing)

		must.SliceLen(t, 1, accounts.RecordAccountSubscriptionEndedCalls())
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionCalls())
		test.EqOp(t, identity.BillingUnpaid, accounts.RecordAccountSubscriptionEndedCalls()[0].Status)
	})

	// A redelivery reported no news, so identity's reconciliation stamp has no
	// news to record either.
	T.Run("writes no standing for a delivery that changed nothing", func(t *testing.T) {
		t.Parallel()

		accounts := &identitymock.StoreMock{}

		result, err := newTestSyncer(t, holding(capitalism.SubscriptionStatusActive),
			WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		must.NoError(t, err)
		test.EqOp(t, OutcomeUnchanged, result.Outcome)
		test.Nil(t, result.Standing)
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionCalls())
	})

	// standing's ruling: a status the deployment has not ruled on leaves the
	// account where it is. The subscription row still moves.
	T.Run("leaves the account alone where the classifier declines", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		store.SetSubscriptionStatusFunc = func(
			context.Context, database.Tx, tenancy.Scope, string, capitalism.SubscriptionStatus,
		) error {
			return nil
		}
		store.GetSubscriptionFunc = func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return storedSubscription(capitalism.SubscriptionStatusPaused), nil
		}

		accounts := &identitymock.StoreMock{}
		undecided := func(capitalism.SubscriptionStatus) (identity.BillingStatus, bool) {
			return "", false
		}

		result, err := newTestSyncer(t, store, WithStanding(accounts, undecided)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusPaused, &periodStart, &periodEnd))
		must.NoError(t, err)
		test.EqOp(t, OutcomeUpdated, result.Outcome)
		test.Nil(t, result.Standing)
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionCalls())
		test.SliceLen(t, 0, accounts.RecordAccountSubscriptionEndedCalls())
		test.SliceLen(t, 1, store.SetSubscriptionStatusCalls())
	})

	// The standing write rides the caller's transaction, so its refusal has to
	// reach the caller: swallowing it is what leaves a subscription row and an
	// account standing that disagree.
	T.Run("returns a standing write's refusal", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("no account by that id")
		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		accounts := &identitymock.StoreMock{
			RecordAccountSubscriptionFunc: func(
				context.Context, database.Tx, tenancy.Scope, string, identity.BillingStatus, string,
			) error {
				return expected
			},
		}

		result, err := newTestSyncer(t, store, WithStanding(accounts, standing.Strict)).
			Apply(t.Context(), testTx(), testScope,
				delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		test.Nil(t, result)
		test.ErrorIs(t, err, expected)
	})

	T.Run("writes no standing without one to write to", func(t *testing.T) {
		t.Parallel()

		store := absent()
		store.CreateSubscriptionFunc = func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, subscription *billing.Subscription,
		) (*billing.Subscription, error) {
			return subscription, nil
		}

		result, err := newTestSyncer(t, store).Apply(t.Context(), testTx(), testScope,
			delivery(capitalism.SubscriptionStatusActive, &periodStart, &periodEnd))
		must.NoError(t, err)
		test.EqOp(t, OutcomeCreated, result.Outcome)
		test.Nil(t, result.Standing)
	})
}
