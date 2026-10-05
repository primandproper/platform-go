package meteringcfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/metering"

	"github.com/primandproper/primitives-go/v2/analytics"
	analyticsnoop "github.com/primandproper/primitives-go/v2/analytics/noop"
	"github.com/primandproper/primitives-go/v2/capitalism"
	capitalismnoop "github.com/primandproper/primitives-go/v2/capitalism/noop"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

func testPeriodResolver() metering.PeriodResolver {
	return metering.PeriodResolverFunc(
		func(context.Context, string, metering.Period, time.Time) (metering.Bounds, error) {
			return metering.Bounds{}, nil
		},
	)
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		store, err := do.Invoke[metering.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestRegisterRecorder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, metering.NewRegistry())
		do.ProvideValue[metering.PeriodResolver](i, testPeriodResolver())
		do.ProvideValue[analytics.EventReporter](i, analyticsnoop.NewEventReporter())

		RegisterStore(i)
		RegisterRecorder(i)

		recorder, err := do.Invoke[*metering.DurableRecorder](i)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("builds without a registered analytics reporter", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, metering.NewRegistry())
		do.ProvideValue[metering.PeriodResolver](i, testPeriodResolver())

		RegisterStore(i)
		RegisterRecorder(i)

		recorder, err := do.Invoke[*metering.DurableRecorder](i)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("a registered reporter that fails to build is an error", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, metering.NewRegistry())
		do.ProvideValue[metering.PeriodResolver](i, testPeriodResolver())

		// The distinction InvokeOptional exists to preserve: nobody registered
		// one is fine, the registered one failing to build is not.
		do.Provide(i, func(do.Injector) (analytics.EventReporter, error) {
			return nil, errTestReporter
		})

		RegisterStore(i)
		RegisterRecorder(i)

		_, err := do.Invoke[*metering.DurableRecorder](i)
		test.ErrorIs(t, err, errTestReporter)
	})
}

// errTestReporter is the failure a deliberately broken provider returns.
var errTestReporter = errors.New("reporter unavailable")

func TestRegisterEnforcer(T *testing.T) {
	T.Parallel()

	T.Run("builds without a registered totals cache", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		registry := metering.NewRegistry()
		do.ProvideValue(i, registry)
		do.ProvideValue[metering.PeriodResolver](i, testPeriodResolver())
		do.ProvideValue[metering.QuotaSource](i, metering.NewRegistryQuotaSource(registry))

		RegisterStore(i)
		RegisterEnforcer(i)

		enforcer, err := do.Invoke[metering.Enforcer](i)
		must.NoError(t, err)
		test.NotNil(t, enforcer)
	})

	T.Run("builds without a registered quota source", func(t *testing.T) {
		t.Parallel()

		// Absent one, the Registry's static quotas serve every subject, which is
		// what NewEnforcer documents and what a deployment with no plan catalog
		// runs on.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, metering.NewRegistry())
		do.ProvideValue[metering.PeriodResolver](i, testPeriodResolver())

		RegisterStore(i)
		RegisterEnforcer(i)

		enforcer, err := do.Invoke[metering.Enforcer](i)
		must.NoError(t, err)
		test.NotNil(t, enforcer)
	})
}

func TestRegisterFlusher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue[metering.ProviderMapper](i, metering.ProviderMapperFunc(
			func(context.Context, string, string) (metering.ProviderRef, error) {
				return metering.ProviderRef{}, nil
			},
		))
		do.ProvideValue[capitalism.UsageReporter](i, capitalismnoop.NewUsageReporter())

		RegisterStore(i)
		RegisterFlusher(i)

		flusher, err := do.Invoke[*metering.Flusher](i)
		must.NoError(t, err)
		test.NotNil(t, flusher)
	})

	T.Run("settles usage unposted when no provider mapper is registered", func(t *testing.T) {
		t.Parallel()

		// The deployment that meters and does not bill yet registers no mapper,
		// and gets metering.Unbilled rather than a flusher that refuses to build.
		client := newClient(t)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, client)
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, newRegistry(t))
		do.ProvideValue[metering.PeriodResolver](i, metering.NewCalendarPeriodResolver(nil))
		do.ProvideValue[capitalism.UsageReporter](i, capitalismnoop.NewUsageReporter())

		RegisterStore(i)
		RegisterRecorder(i)
		RegisterFlusher(i)

		recorder, err := do.Invoke[*metering.DurableRecorder](i)
		must.NoError(t, err)

		flusher, err := do.Invoke[*metering.Flusher](i)
		must.NoError(t, err)

		must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return recorder.Record(t.Context(), tx, tenancy.Global(), metering.Usage{
				Subject: "account-1", Meter: "api_requests", Quantity: 5, IdempotencyKey: "req-1",
			})
		}))

		result, err := flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 1, result.Skipped)
		test.EqOp(t, 0, result.Flushed)
		test.EqOp(t, 0, result.Failed)
	})
}
