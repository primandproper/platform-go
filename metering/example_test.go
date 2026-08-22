package metering_test

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/metering"
)

// A meter says what is being counted and how its records combine. The
// aggregation is not cosmetic: it decides what every stored total for the meter
// means, which is why registering the same name twice is refused rather than
// treated as an update.
func ExampleRegistry() {
	registry := metering.NewRegistry()

	meters := []metering.Meter{
		{Name: "api_requests", Unit: "requests", Aggregation: metering.AggregationSum, Period: metering.PeriodMonth},
		// Storage is a high-water mark, not a sum: a gigabyte held all month is
		// one gigabyte, not thirty.
		{Name: "storage_bytes", Unit: "bytes", Aggregation: metering.AggregationMax, Period: metering.PeriodMonth},
		// Seats are a gauge: what matters is the latest reading.
		{Name: "seats", Unit: "seats", Aggregation: metering.AggregationLast, Period: metering.PeriodMonth},
	}

	for i := range meters {
		if err := registry.RegisterMeter(meters[i]); err != nil {
			panic(err)
		}
	}

	fmt.Println(registry.MeterNames())
	// Output: [api_requests seats storage_bytes]
}

// A quota is a limit on a meter, and its behavior decides what happens at the
// limit. Overage is a first-class behavior rather than an error path, because a
// limit is usually where the price changes and not where the service stops.
func ExampleQuota() {
	registry := metering.NewRegistry()

	if err := registry.RegisterMeter(metering.Meter{
		Name: "llm_tokens", Unit: "tokens",
		Aggregation: metering.AggregationSum, Period: metering.PeriodMonth,
	}); err != nil {
		panic(err)
	}

	if err := registry.RegisterQuota(metering.Quota{
		Meter:    "llm_tokens",
		Limit:    5_000_000,
		Behavior: metering.BehaviorAllowOverage,
		Period:   metering.PeriodMonth,
	}); err != nil {
		panic(err)
	}

	quota, _ := registry.Quota("llm_tokens")
	fmt.Println(quota.Limit, quota.Behavior)
	// Output: 5000000 allow_overage
}

// A quota over a window its meter does not bucket by is refused at registration.
// Answering it would mean summing across buckets, which is a table scan on the
// read path this package exists to keep cheap.
func ExampleQuota_periodMismatch() {
	registry := metering.NewRegistry()

	if err := registry.RegisterMeter(metering.Meter{
		Name: "api_requests", Aggregation: metering.AggregationSum, Period: metering.PeriodMonth,
	}); err != nil {
		panic(err)
	}

	err := registry.RegisterQuota(metering.Quota{
		Meter: "api_requests", Limit: 1000,
		Behavior: metering.BehaviorBlock, Period: metering.PeriodDay,
	})

	fmt.Println(err != nil)
	// Output: true
}

// The default resolver answers the calendar periods in UTC and refuses the
// billing period, which requires knowing when a subject's subscription cycle
// starts — a fact the billing provider holds and this library will not guess.
func ExampleNewCalendarPeriodResolver() {
	resolver := metering.NewCalendarPeriodResolver(nil)

	bounds, err := resolver.Resolve(context.Background(), "account-1",
		metering.PeriodMonth, time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		panic(err)
	}

	// Half-open: usage at Start is in the window, usage at End is in the next
	// one — because inclusive ends have no correct answer at midnight.
	fmt.Println(bounds.Start.Format(time.RFC3339), bounds.End.Format(time.RFC3339))

	_, err = resolver.Resolve(context.Background(), "account-1", metering.PeriodBillingPeriod, time.Now())
	fmt.Println(err != nil)
	// Output:
	// 2026-08-01T00:00:00Z 2026-09-01T00:00:00Z
	// true
}

// Plans live in the application, not here. A QuotaSource maps a subject to a
// limit, which is where every application's opinions are — trials, grandfathered
// pricing, the limit somebody bumped by hand for one customer in 2023.
func ExampleQuotaSource() {
	plans := map[string]int64{"account-1": 1_000, "account-2": 1_000_000}

	quotas := metering.QuotaSourceFunc(func(_ context.Context, subject, meter string) (metering.Quota, error) {
		return metering.Quota{
			Meter:    meter,
			Limit:    plans[subject],
			Behavior: metering.BehaviorBlock,
			Period:   metering.PeriodMonth,
		}, nil
	})

	free, err := quotas.QuotaFor(context.Background(), "account-1", "api_requests")
	if err != nil {
		panic(err)
	}

	enterprise, err := quotas.QuotaFor(context.Background(), "account-2", "api_requests")
	if err != nil {
		panic(err)
	}

	fmt.Println(free.Limit, enterprise.Limit)
	// Output: 1000 1000000
}

// Between one set of limits for everybody and a QuotaSource written from
// scratch, NewPlanLimitSource supplies the ladder: a meter the table does not
// name is unlimited, a subject an EntitlementReader entitles gets their
// product's limit, and everybody else gets the unsubscribed one.
func ExampleNewPlanLimitSource() {
	registry := metering.NewRegistry()

	for _, name := range []string{"api_requests", "support_tickets"} {
		if err := registry.RegisterMeter(metering.Meter{
			Name: name, Aggregation: metering.AggregationSum, Period: metering.PeriodMonth,
		}); err != nil {
			panic(err)
		}
	}

	// The application's subscription lookup. In production this reads the column
	// the billing provider's webhook handler writes.
	subscriptions := metering.EntitlementReaderFunc(func(_ context.Context, subject string) (string, bool, error) {
		return map[string]string{"account-1": "prod_pro"}[subject], subject == "account-1", nil
	})

	quotas, err := metering.NewPlanLimitSource(registry, map[string]metering.PlanLimits{
		"api_requests": {
			ByProduct:    map[string]int64{"prod_pro": 1_000_000},
			Behavior:     metering.BehaviorAllowOverage,
			Unsubscribed: 1_000,
		},
	}, subscriptions)
	if err != nil {
		panic(err)
	}

	subscriber, err := quotas.QuotaFor(context.Background(), "account-1", "api_requests")
	if err != nil {
		panic(err)
	}

	lapsed, err := quotas.QuotaFor(context.Background(), "account-2", "api_requests")
	if err != nil {
		panic(err)
	}

	// A meter no plan varies is unlimited, and is answered without reading the
	// subscription at all.
	ungated, err := quotas.QuotaFor(context.Background(), "account-2", "support_tickets")
	if err != nil {
		panic(err)
	}

	fmt.Println(subscriber.Limit, lapsed.Limit, ungated.Limit == metering.Unlimited)
	// Output: 1000000 1000 true
}

// The key a flush carries to the provider is derived from the subject, meter,
// period, and sequence — so a retry of the same post computes the same key and
// the provider ignores it, while the next post computes a different one.
//
// It is exported so an operator reconciling an invoice by hand can compute what a
// given post's key would have been.
func ExampleFlushIdempotencyKey() {
	total := &metering.Total{
		Subject:     "account-1",
		Meter:       "api_requests",
		PeriodStart: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
	}

	first := metering.FlushIdempotencyKey(total)

	retried := *total
	after := *total
	after.FlushSequence = 1

	fmt.Println(first == metering.FlushIdempotencyKey(&retried))
	fmt.Println(first == metering.FlushIdempotencyKey(&after))
	// Output:
	// true
	// false
}

// A total tracks how much the provider has been told about, so each post carries
// only the delta. Posting the running total every flush would invoice the sum of
// every partial total ever posted.
func ExampleTotal_Delta() {
	total := &metering.Total{Quantity: 1_050, FlushedQuantity: 1_000}

	fmt.Println(total.Pending(), total.Delta())
	// Output: true 50
}
