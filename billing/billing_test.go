package billing

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestKind_Valid(t *testing.T) {
	t.Parallel()

	test.True(t, KindRecurring.Valid())
	test.True(t, KindOneTime.Valid())
	test.False(t, Kind("subscription").Valid())
	test.False(t, Kind("").Valid())
	test.EqOp(t, "recurring", KindRecurring.String())
}

func TestTransactionStatus_Valid(t *testing.T) {
	t.Parallel()

	for _, status := range []TransactionStatus{
		TransactionPending, TransactionSucceeded, TransactionFailed, TransactionRefunded,
	} {
		test.True(t, status.Valid(), test.Sprintf("%s should be valid", status))
	}

	test.False(t, TransactionStatus("settled").Valid())
	test.False(t, TransactionStatus("").Valid())
	test.EqOp(t, "refunded", TransactionRefunded.String())
}

// TestNormalizeCurrency pins the one write this package makes to a caller's
// value: "usd" and "USD" are one currency, and a ledger holding both would be a
// ledger whose totals depend on which handler wrote each row.
func TestNormalizeCurrency(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "USD", NormalizeCurrency("usd"))
	test.EqOp(t, "USD", NormalizeCurrency("  USD  "))
	test.EqOp(t, "JPY", NormalizeCurrency("jPy"))
	test.EqOp(t, "", NormalizeCurrency("   "))
}

func TestProduct_Recurring(t *testing.T) {
	t.Parallel()

	test.True(t, (&Product{Kind: KindRecurring}).Recurring())
	test.False(t, (&Product{Kind: KindOneTime}).Recurring())
	test.False(t, (*Product)(nil).Recurring())
}

func TestProduct_validate(T *testing.T) {
	T.Parallel()

	valid := func() *Product {
		return &Product{
			Name:                  "pro",
			Kind:                  KindRecurring,
			Currency:              "USD",
			AmountCents:           2_500,
			BillingIntervalMonths: 1,
		}
	}

	T.Run("accepts a well-formed product", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, valid().validate())
	})

	T.Run("refuses what it must", func(T *testing.T) {
		T.Parallel()

		cases := map[string]struct {
			mutate func(*Product)
			want   error
		}{
			"no name":            {func(p *Product) { p.Name = "" }, ErrEmptyProductName},
			"unknown kind":       {func(p *Product) { p.Kind = "monthly" }, ErrInvalidKind},
			"short currency":     {func(p *Product) { p.Currency = "US" }, ErrInvalidCurrency},
			"no currency":        {func(p *Product) { p.Currency = "" }, ErrInvalidCurrency},
			"negative price":     {func(p *Product) { p.AmountCents = -1 }, ErrNegativeAmount},
			"recurring, no term": {func(p *Product) { p.BillingIntervalMonths = 0 }, ErrEmptyBillingInterval},
			"one-time with term": {func(p *Product) { p.Kind = KindOneTime }, ErrUnexpectedBillingInterval},
		}

		for name, tc := range cases {
			T.Run(name, func(t *testing.T) {
				t.Parallel()

				product := valid()
				tc.mutate(product)

				test.ErrorIs(t, product.validate(), tc.want)
			})
		}
	})

	T.Run("refuses a nil product", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, (*Product)(nil).validate(), platformerrors.ErrNilInputParameter)
	})
}

// TestSubscription_CurrentAt pins the boundary, which is exclusive at the end —
// the reading that leaves no instant at which an agreement is neither current
// nor lapsed.
func TestSubscription_CurrentAt(T *testing.T) {
	T.Parallel()

	start := testNow.Add(-time.Hour)
	end := testNow.Add(time.Hour)

	subscription := func() *Subscription {
		return &Subscription{CurrentPeriodStart: start, CurrentPeriodEnd: end}
	}

	T.Run("covers an instant inside the period", func(t *testing.T) {
		t.Parallel()

		test.True(t, subscription().CurrentAt(testNow))
	})

	T.Run("includes the start and excludes the end", func(t *testing.T) {
		t.Parallel()

		test.True(t, subscription().CurrentAt(start))
		test.False(t, subscription().CurrentAt(end))
	})

	T.Run("is not current before it starts", func(t *testing.T) {
		t.Parallel()

		test.False(t, subscription().CurrentAt(start.Add(-time.Nanosecond)))
	})

	T.Run("is never current once archived", func(t *testing.T) {
		t.Parallel()

		archived := subscription()
		archived.ArchivedAt = &testNow

		test.False(t, archived.CurrentAt(testNow))
	})

	T.Run("says nothing about the status", func(t *testing.T) {
		t.Parallel()

		// A past_due agreement inside its paid period is current by this
		// reading and may well not be entitled, which is the policy this
		// package deliberately does not hold.
		pastDue := subscription()
		pastDue.Status = capitalism.SubscriptionStatusPastDue

		test.True(t, pastDue.CurrentAt(testNow))
	})

	T.Run("is false for a nil subscription", func(t *testing.T) {
		t.Parallel()

		test.False(t, (*Subscription)(nil).CurrentAt(testNow))
	})
}

func TestSubscription_validate(T *testing.T) {
	T.Parallel()

	valid := func() *Subscription {
		return &Subscription{
			BelongsToAccount:   testAccount,
			ProductID:          "product-1",
			Status:             capitalism.SubscriptionStatusActive,
			CurrentPeriodStart: testNow,
			CurrentPeriodEnd:   testNow.Add(time.Hour),
		}
	}

	cases := map[string]struct {
		mutate func(*Subscription)
		want   error
	}{
		"no account":   {func(s *Subscription) { s.BelongsToAccount = "" }, ErrEmptyAccount},
		"no product":   {func(s *Subscription) { s.ProductID = "" }, ErrEmptyProduct},
		"unknown":      {func(s *Subscription) { s.Status = capitalism.SubscriptionStatusUnknown }, ErrInvalidStatus},
		"invented":     {func(s *Subscription) { s.Status = "half_paid" }, ErrInvalidStatus},
		"no period":    {func(s *Subscription) { s.CurrentPeriodEnd = time.Time{} }, ErrEmptyPeriod},
		"backwards":    {func(s *Subscription) { s.CurrentPeriodEnd = s.CurrentPeriodStart }, ErrBackwardsPeriod},
		"zero-lengthy": {func(s *Subscription) { s.CurrentPeriodStart = s.CurrentPeriodEnd }, ErrBackwardsPeriod},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			subscription := valid()
			tc.mutate(subscription)

			test.ErrorIs(t, subscription.validate(), tc.want)
		})
	}

	T.Run("accepts a well-formed subscription", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, valid().validate())
	})
}

func TestTransaction_validate(T *testing.T) {
	T.Parallel()

	valid := func() *Transaction {
		return &Transaction{
			BelongsToAccount: testAccount,
			Status:           TransactionSucceeded,
			Currency:         "USD",
			AmountCents:      999,
		}
	}

	T.Run("accepts a row naming neither a subscription nor a purchase", func(t *testing.T) {
		t.Parallel()

		// A refund of something no longer here is exactly that row.
		test.NoError(t, valid().validate())
	})

	T.Run("refuses a row naming both", func(t *testing.T) {
		t.Parallel()

		transaction := valid()
		transaction.SubscriptionID = "sub-1"
		transaction.PurchaseID = "pur-1"

		test.ErrorIs(t, transaction.validate(), ErrAmbiguousTransaction)
	})

	T.Run("refuses a negative amount", func(t *testing.T) {
		t.Parallel()

		transaction := valid()
		transaction.AmountCents = -1

		test.ErrorIs(t, transaction.validate(), ErrNegativeAmount)
	})

	T.Run("refuses a nil row", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, (*Transaction)(nil).validate(), ErrNilTransaction)
	})

	T.Run("refuses a row belonging to nobody", func(t *testing.T) {
		t.Parallel()

		transaction := valid()
		transaction.BelongsToAccount = ""

		test.ErrorIs(t, transaction.validate(), ErrEmptyAccount)
	})

	T.Run("refuses a status this package does not implement", func(t *testing.T) {
		t.Parallel()

		transaction := valid()
		transaction.Status = "reticulating"

		test.ErrorIs(t, transaction.validate(), ErrInvalidStatus)
	})

	T.Run("refuses a currency that is not three characters", func(t *testing.T) {
		t.Parallel()

		// The length check is the whole of it: this package stores what ISO 4217
		// codes look like, and does not carry a table of which ones exist.
		for _, currency := range []string{"", "US", "USDD"} {
			transaction := valid()
			transaction.Currency = currency

			test.ErrorIs(t, transaction.validate(), ErrInvalidCurrency,
				test.Sprintf("currency %q", currency))
		}
	})
}

func TestPurchase_validate(T *testing.T) {
	T.Parallel()

	valid := func() *Purchase {
		return &Purchase{
			BelongsToAccount: testAccount,
			ProductID:        "product-1",
			Currency:         "USD",
			AmountCents:      999,
		}
	}

	T.Run("accepts an outstanding sale", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, valid().validate())
	})

	T.Run("refuses a nil sale", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, (*Purchase)(nil).validate(), ErrNilPurchase)
	})

	T.Run("refuses a sale belonging to nobody", func(t *testing.T) {
		t.Parallel()

		purchase := valid()
		purchase.BelongsToAccount = ""

		test.ErrorIs(t, purchase.validate(), ErrEmptyAccount)
	})

	T.Run("refuses a sale of nothing", func(t *testing.T) {
		t.Parallel()

		// Unlike a ledger row, which may name neither a subscription nor a
		// purchase, a sale is always a sale of something.
		purchase := valid()
		purchase.ProductID = ""

		test.ErrorIs(t, purchase.validate(), ErrEmptyProduct)
	})

	T.Run("refuses a currency that is not three characters", func(t *testing.T) {
		t.Parallel()

		for _, currency := range []string{"", "US", "USDD"} {
			purchase := valid()
			purchase.Currency = currency

			test.ErrorIs(t, purchase.validate(), ErrInvalidCurrency,
				test.Sprintf("currency %q", currency))
		}
	})

	T.Run("refuses a negative amount", func(t *testing.T) {
		t.Parallel()

		// A refund is its own positive row rather than a negative one, so a
		// negative amount here is a mistake rather than a reversal.
		purchase := valid()
		purchase.AmountCents = -1

		test.ErrorIs(t, purchase.validate(), ErrNegativeAmount)
	})
}

func TestPurchase_Complete(t *testing.T) {
	t.Parallel()

	test.False(t, (&Purchase{}).Complete())
	test.True(t, (&Purchase{CompletedAt: &testNow}).Complete())
	test.False(t, (*Purchase)(nil).Complete())
}

func TestNewSQLStore_Refusals(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix that renders an illegal identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("no spaces allowed"))
		test.Error(t, err)
	})

	T.Run("builds with no observability at all", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client)
		must.NoError(t, err)
		test.EqOp(t, DefaultTablePrefix, store.TablePrefix())
	})
}

// TestBillingdbDialect covers the arm no supported dialect reaches: a dialect
// this module learns that the generated package was not generated for.
func TestBillingdbDialect(t *testing.T) {
	t.Parallel()

	for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
		got, err := billingdbDialect(d)
		must.NoError(t, err)
		test.NotEq(t, "", string(got))
	}

	_, err := billingdbDialect(dialect.Dialect("cockroach"))
	test.ErrorIs(t, err, dialect.ErrUnsupported)
}

// TestNullableRoundTrip pins the rule the four provider-identifier columns
// follow: the empty string is absence, and absence reads back as the empty
// string.
func TestNullableRoundTrip(t *testing.T) {
	t.Parallel()

	test.Nil(t, nullable(""))
	test.EqOp(t, "sub_1", *nullable("sub_1"))
	test.EqOp(t, "", text(nil))
	test.EqOp(t, "sub_1", text(nullable("sub_1")))

	test.Nil(t, months(0))
	test.EqOp(t, int64(12), *months(12))
	test.EqOp(t, int64(0), count(nil))
	test.EqOp(t, int64(12), count(months(12)))
}
