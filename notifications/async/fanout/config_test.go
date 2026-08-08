package fanout

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Enabled: true, Topic: "topic"}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with no topic", func(t *testing.T) {
		t.Parallel()

		// Only reachable when EnsureDefaults has not run: New calls it first, so
		// an unset topic is a default rather than a failure.
		cfg := &Config{Enabled: true}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills an unset topic", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultTopic, cfg.Topic)
	})

	T.Run("leaves a set topic alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Topic: "custom_topic"}
		cfg.EnsureDefaults()

		test.EqOp(t, "custom_topic", cfg.Topic)
	})
}
