package plans

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// baseTime is the instant these tests resolve, and the windows are drawn around
// it. It is fixed rather than time.Now, because a resolver whose answer is the
// primary key of a durable total is one whose tests should be able to name the
// answer.
var baseTime = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// cycling is one agreement over a window, active and unarchived.
func cycling(start, end time.Time) *billing.Subscription {
	s := subscription(capitalism.SubscriptionStatusActive, "pro")
	s.CurrentPeriodStart = start
	s.CurrentPeriodEnd = end

	return s
}

func TestNewPeriodResolver_Refusals(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewPeriodResolver(nil, &testReader{}, testScope, Sole)
		test.ErrorIs(t, err, ErrNilStore)
	})

	T.Run("refuses a nil cycle", func(t *testing.T) {
		t.Parallel()

		// There is no default for the same reason Choose has none: an account
		// holding two current subscriptions has two cycles, and which one a
		// shared meter bills against is the deployment's answer.
		_, err := NewPeriodResolver(storeReturning(), &testReader{}, testScope, nil)
		test.ErrorIs(t, err, ErrNilCycle)
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		_, err := NewPeriodResolver(storeReturning(), nil, testScope, Sole)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		_, err := NewPeriodResolver(storeReturning(), &testReader{}, tenancy.Scope{}, Sole)
		test.Error(t, err)
	})
}

func TestSole(T *testing.T) {
	T.Parallel()

	T.Run("takes the one covering agreement", func(t *testing.T) {
		t.Parallel()

		only := cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour))

		chosen, ok := Sole([]*billing.Subscription{only})
		test.True(t, ok)
		test.EqOp(t, only, chosen)
	})

	T.Run("takes two agreements on one cycle as one window", func(t *testing.T) {
		t.Parallel()

		// A plan and its add-on renew together. There is nothing to choose
		// between, because they name the same window.
		plan := cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour))
		addOn := cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour))

		chosen, ok := Sole([]*billing.Subscription{plan, addOn})
		test.True(t, ok)
		test.EqOp(t, plan, chosen)
	})

	T.Run("declines two agreements on different cycles", func(t *testing.T) {
		t.Parallel()

		// Resolving this by position would file one meter's usage under one
		// subscription's window this month and the other's the month the paging
		// order changes, and neither invoice would say so.
		_, ok := Sole([]*billing.Subscription{
			cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour)),
			cycling(baseTime.Add(-time.Hour), baseTime.Add(2*time.Hour)),
		})
		test.False(t, ok)
	})

	T.Run("declines an empty page", func(t *testing.T) {
		t.Parallel()

		_, ok := Sole(nil)
		test.False(t, ok)
	})
}

func TestPeriodResolver_Resolve(T *testing.T) {
	T.Parallel()

	T.Run("answers with the window the provider says is paid for", func(t *testing.T) {
		t.Parallel()

		var (
			start = baseTime.AddDate(0, 0, -3)
			end   = baseTime.AddDate(0, 1, -3)
		)

		resolver, err := NewPeriodResolver(storeReturning(cycling(start, end)), &testReader{}, testScope, Sole)
		must.NoError(t, err)

		bounds, err := resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		must.NoError(t, err)
		test.True(t, bounds.Start.Equal(start))
		test.True(t, bounds.End.Equal(end))
		test.True(t, bounds.Contains(baseTime))
	})

	T.Run("answers in UTC", func(t *testing.T) {
		t.Parallel()

		// The bounds become part of a durable total's primary key, so the zone
		// they arrive in cannot be the zone whatever wrote the row was in.
		zone := time.FixedZone("UTC-5", -5*60*60)

		resolver, err := NewPeriodResolver(storeReturning(
			cycling(baseTime.Add(-time.Hour).In(zone), baseTime.Add(time.Hour).In(zone)),
		), &testReader{}, testScope, Sole)
		must.NoError(t, err)

		bounds, err := resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		must.NoError(t, err)
		test.EqOp(t, time.UTC, bounds.Start.Location())
		test.EqOp(t, time.UTC, bounds.End.Location())
	})

	T.Run("refuses the calendar periods", func(T *testing.T) {
		T.Parallel()

		for _, period := range []metering.Period{metering.PeriodDay, metering.PeriodMonth, "fortnight"} {
			T.Run(string(period), func(t *testing.T) {
				t.Parallel()

				resolver, err := NewPeriodResolver(storeReturning(
					cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour)),
				), &testReader{}, testScope, Sole)
				must.NoError(t, err)

				_, err = resolver.Resolve(t.Context(), testAccount, period, baseTime)
				test.ErrorIs(t, err, ErrNotBillingPeriod)
			})
		}
	})

	T.Run("reports no billing period for an account with nothing current", func(t *testing.T) {
		t.Parallel()

		resolver, err := NewPeriodResolver(storeReturning(), &testReader{}, testScope, Sole)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.ErrorIs(t, err, ErrNoBillingPeriod)
	})

	T.Run("reports no billing period for an instant no window covers", func(t *testing.T) {
		t.Parallel()

		// Usage is recorded at the instant it occurred, which a queue
		// redelivery can put before the current window opened. The subscription
		// is current as of the store's clock and still says nothing about then.
		resolver, err := NewPeriodResolver(storeReturning(
			cycling(baseTime, baseTime.AddDate(0, 1, 0)),
		), &testReader{}, testScope, Sole)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount,
			metering.PeriodBillingPeriod, baseTime.Add(-time.Hour))
		test.ErrorIs(t, err, ErrNoBillingPeriod)
	})

	T.Run("reports no billing period when the cycle declines to pick", func(t *testing.T) {
		t.Parallel()

		resolver, err := NewPeriodResolver(storeReturning(
			cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour)),
			cycling(baseTime.Add(-time.Hour), baseTime.Add(2*time.Hour)),
		), &testReader{}, testScope, Sole)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.ErrorIs(t, err, ErrNoBillingPeriod)
	})

	T.Run("shows the cycle only the windows that cover the instant", func(t *testing.T) {
		t.Parallel()

		var (
			covering = cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour))
			upcoming = cycling(baseTime.Add(time.Hour), baseTime.Add(2*time.Hour))
			seen     []*billing.Subscription
		)

		resolver, err := NewPeriodResolver(storeReturning(upcoming, covering), &testReader{}, testScope,
			func(subscriptions []*billing.Subscription) (*billing.Subscription, bool) {
				seen = subscriptions

				return Sole(subscriptions)
			})
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		must.NoError(t, err)
		must.SliceLen(t, 1, seen)
		test.EqOp(t, covering, seen[0])
	})

	T.Run("lets a deployment write its own rule", func(t *testing.T) {
		t.Parallel()

		// The reading Sole refuses: two cycles at once, billed against the
		// agreement opened first. It is a rule this module holds no opinion
		// about, which is the whole reason Cycle is an argument.
		oldest := func(subscriptions []*billing.Subscription) (*billing.Subscription, bool) {
			if len(subscriptions) == 0 {
				return nil, false
			}

			return subscriptions[0], true
		}

		first := cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour))

		resolver, err := NewPeriodResolver(storeReturning(
			first,
			cycling(baseTime.Add(-time.Hour), baseTime.Add(2*time.Hour)),
		), &testReader{}, testScope, oldest)
		must.NoError(t, err)

		bounds, err := resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		must.NoError(t, err)
		test.True(t, bounds.End.Equal(first.CurrentPeriodEnd))
	})

	T.Run("refuses a cycle that picks a window the instant is outside", func(t *testing.T) {
		t.Parallel()

		elsewhere := func([]*billing.Subscription) (*billing.Subscription, bool) {
			return cycling(baseTime.Add(time.Hour), baseTime.Add(2*time.Hour)), true
		}

		resolver, err := NewPeriodResolver(storeReturning(
			cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour)),
		), &testReader{}, testScope, elsewhere)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.Error(t, err)

		// Not "this account has no billing period": the account has one, and
		// what went wrong is the rule that chose.
		test.False(t, errors.Is(err, ErrNoBillingPeriod))
	})

	T.Run("refuses a cycle that reports a choice it did not make", func(t *testing.T) {
		t.Parallel()

		nothing := func([]*billing.Subscription) (*billing.Subscription, bool) {
			return nil, true
		}

		resolver, err := NewPeriodResolver(storeReturning(
			cycling(baseTime.Add(-time.Hour), baseTime.Add(time.Hour)),
		), &testReader{}, testScope, nothing)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.Error(t, err)
	})

	T.Run("passes a read failure through as a failure", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the database is unwell")

		store := &billingmock.SubscriptionStoreMock{
			ListCurrentSubscriptionsFunc: func(
				_ context.Context,
				_ database.SQLQueryExecutor,
				_ tenancy.Scope,
				_ string,
				_ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[billing.Subscription], error) {
				return nil, boom
			},
		}

		resolver, err := NewPeriodResolver(store, &testReader{}, testScope, Sole)
		must.NoError(t, err)

		_, err = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.ErrorIs(t, err, boom)

		// An outage is not "no billing period". Collapsing the two would file a
		// month of usage under a window nobody chose, on the day the database
		// was unwell.
		test.False(t, errors.Is(err, ErrNoBillingPeriod))
	})

	T.Run("reads through the executor and the scope it was built with", func(t *testing.T) {
		t.Parallel()

		var (
			reader database.SQLQueryExecutor = &testReader{}
			seenQ  database.SQLQueryExecutor
			seen   tenancy.Scope
		)

		store := &billingmock.SubscriptionStoreMock{
			ListCurrentSubscriptionsFunc: func(
				_ context.Context,
				q database.SQLQueryExecutor,
				scope tenancy.Scope,
				_ string,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[billing.Subscription], error) {
				seenQ, seen = q, scope

				return filtering.NewQueryFilteredResultWithoutCounts[billing.Subscription](nil,
					func(s *billing.Subscription) string { return s.ID }, filter), nil
			},
		}

		resolver, err := NewPeriodResolver(store, reader, testScope, Sole)
		must.NoError(t, err)

		_, _ = resolver.Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		test.EqOp(t, reader, seenQ)
		test.EqOp(t, testScope, seen)
	})
}

func TestPeriodResolver_UnderTheCalendarResolver(T *testing.T) {
	T.Parallel()

	T.Run("answers the billing period through the delegation", func(t *testing.T) {
		t.Parallel()

		var (
			start = baseTime.AddDate(0, 0, -3)
			end   = baseTime.AddDate(0, 1, -3)
		)

		billingPeriods, err := NewPeriodResolver(storeReturning(cycling(start, end)),
			&testReader{}, testScope, Sole)
		must.NoError(t, err)

		bounds, err := metering.NewCalendarPeriodResolver(billingPeriods).
			Resolve(t.Context(), testAccount, metering.PeriodBillingPeriod, baseTime)
		must.NoError(t, err)
		test.True(t, bounds.Start.Equal(start))
		test.True(t, bounds.End.Equal(end))
	})

	T.Run("leaves the calendar periods where they were, unread", func(T *testing.T) {
		T.Parallel()

		for _, period := range []metering.Period{metering.PeriodDay, metering.PeriodMonth} {
			T.Run(string(period), func(t *testing.T) {
				t.Parallel()

				// A day-bucketed meter costs no subscription read, which is why
				// this is delegated to rather than wrapped around.
				store := &billingmock.SubscriptionStoreMock{
					ListCurrentSubscriptionsFunc: func(
						context.Context,
						database.SQLQueryExecutor,
						tenancy.Scope,
						string,
						*filtering.QueryFilter,
					) (*filtering.QueryFilteredResult[billing.Subscription], error) {
						t.Error("the calendar periods must not read subscriptions")

						return nil, nil
					},
				}

				billingPeriods, err := NewPeriodResolver(store, &testReader{}, testScope, Sole)
				must.NoError(t, err)

				bounds, err := metering.NewCalendarPeriodResolver(billingPeriods).
					Resolve(t.Context(), testAccount, period, baseTime)
				must.NoError(t, err)
				test.True(t, bounds.Contains(baseTime))
			})
		}
	})
}

var _ metering.PeriodResolver = (*PeriodResolver)(nil)
