package redisclient

import (
	"testing"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("a single address yields a single-node client", func(t *testing.T) {
		t.Parallel()

		c, err := New(Config{Addresses: []string{"localhost:6379"}})
		must.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		_, ok := c.(*redis.Client)
		test.True(t, ok)
	})

	T.Run("two or more addresses yield a cluster client", func(t *testing.T) {
		t.Parallel()

		c, err := New(Config{Addresses: []string{"localhost:6379", "localhost:6380"}})
		must.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		_, ok := c.(*redis.ClusterClient)
		test.True(t, ok)
	})

	T.Run("Cluster forces cluster mode for a single seed address", func(t *testing.T) {
		t.Parallel()

		// A cluster reached through one configuration endpoint. Misreading it as
		// single-node fails multi-key commands with CROSSSLOT.
		c, err := New(Config{Addresses: []string{"localhost:6379"}, Cluster: true})
		must.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		_, ok := c.(*redis.ClusterClient)
		test.True(t, ok)
	})

	T.Run("no addresses is an error, not a nil client", func(t *testing.T) {
		t.Parallel()

		c, err := New(Config{})
		test.ErrorIs(t, err, errors.ErrEmptyInputParameter)
		test.Nil(t, c)
	})

	T.Run("carries the shared timeouts", func(t *testing.T) {
		t.Parallel()

		c, err := New(Config{Addresses: []string{"localhost:6379"}, Username: "u", Password: "p"})
		must.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		single, ok := c.(*redis.Client)
		must.True(t, ok)

		// Without these a Redis that has stopped answering is an unbounded stall
		// rather than an error the caller can act on.
		opts := single.Options()
		test.EqOp(t, defaultTimeout, opts.DialTimeout)
		test.EqOp(t, defaultTimeout, opts.ReadTimeout)
		test.EqOp(t, defaultTimeout, opts.WriteTimeout)
		test.EqOp(t, "u", opts.Username)
		test.EqOp(t, "p", opts.Password)
	})
}
