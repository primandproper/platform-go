package multisource

import (
	"testing"

	analyticscfg "github.com/primandproper/platform-go/v13/analytics/config"
	"github.com/primandproper/platform-go/v13/analytics/posthog"
	"github.com/primandproper/platform-go/v13/analytics/segment"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewMultiSourceEventReporterFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("with no proxy sources", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, nil)
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapEmpty(t, reporter.reporters)
	})

	T.Run("with valid segment source", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
		}

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 1, reporter.reporters)
	})

	// Substituting a noop lasted the whole process lifetime, so every event for
	// that source was dropped until someone redeployed.
	T.Run("with an invalid source", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderSegment,
				Segment:  &segment.Config{},
			},
		}

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		test.Error(t, err)
		test.Nil(t, reporter)
	})

	T.Run("with unrecognized provider reports rather than substituting a noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"web": {
				Provider: "bogus",
			},
		}

		// A source whose provider is a typo drops every event it is handed. That is
		// worth failing startup over, since nothing downstream would notice.
		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		test.Error(t, err)
		test.Nil(t, reporter)
	})

	T.Run("with multiple posthog sources sharing an API key reuses one reporter", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: t.Name()},
			},
			"web": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: t.Name()},
			},
		}

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 2, reporter.reporters)

		// Same API key -> the two sources share a single client instance.
		test.EqOp(t, reporter.reporters["ios"], reporter.reporters["web"])
	})

	T.Run("with posthog sources having distinct API keys creates distinct reporters", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: "ios-project-key"},
			},
			"web": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: "web-project-key"},
			},
		}

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 2, reporter.reporters)

		// Distinct API keys -> each source gets its own client so credentials aren't discarded.
		test.NotEqOp(t, reporter.reporters["ios"], reporter.reporters["web"])
	})

	T.Run("with empty proxy sources map", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{}

		reporter, err := NewMultiSourceEventReporterFromConfig(ctx, sources)
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapEmpty(t, reporter.reporters)
	})
}
