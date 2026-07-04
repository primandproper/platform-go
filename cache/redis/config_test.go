package redis

import (
	"testing"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{
			QueueAddresses: []string{"localhost:6379"},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty addresses", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{
			QueueAddresses: []string{},
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with nil addresses", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_clusterMode(T *testing.T) {
	T.Parallel()

	T.Run("single address is not cluster", func(t *testing.T) {
		t.Parallel()
		test.False(t, (&Config{QueueAddresses: []string{"localhost:6379"}}).clusterMode())
	})

	T.Run("multiple addresses imply cluster", func(t *testing.T) {
		t.Parallel()
		test.True(t, (&Config{QueueAddresses: []string{"a:6379", "b:6379"}}).clusterMode())
	})

	T.Run("single-seed cluster honored via explicit flag", func(t *testing.T) {
		t.Parallel()
		// Without the explicit flag this single-address config would be misclassified
		// as single-node and multi-slot ops would fail CROSSSLOT.
		test.True(t, (&Config{QueueAddresses: []string{"seed:6379"}, Cluster: true}).clusterMode())
	})
}
