package objectstorage

import (
	"testing"

	"github.com/shoenig/test"
)

func TestBackblazeB2Config_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &BackblazeB2Config{
			ApplicationKeyID: t.Name(),
			ApplicationKey:   t.Name(),
			Region:           t.Name(),
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing application key ID", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &BackblazeB2Config{
			ApplicationKey: t.Name(),
			Region:         t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing application key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &BackblazeB2Config{
			ApplicationKeyID: t.Name(),
			Region:           t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing region", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &BackblazeB2Config{
			ApplicationKeyID: t.Name(),
			ApplicationKey:   t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}
