package meteringcfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/analytics"
	analyticsnoop "github.com/primandproper/platform-go/v13/analytics/noop"
	"github.com/primandproper/platform-go/v13/capitalism"
	capitalismnoop "github.com/primandproper/platform-go/v13/capitalism/noop"
	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	"github.com/primandproper/platform-go/v13/metering"

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
}

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
}
