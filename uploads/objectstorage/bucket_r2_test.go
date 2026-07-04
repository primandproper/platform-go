package objectstorage

import (
	"testing"

	"github.com/shoenig/test"
)

func TestR2Config_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &R2Config{
			AccountID:       t.Name(),
			AccessKeyID:     t.Name(),
			SecretAccessKey: t.Name(),
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing account ID", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &R2Config{
			AccessKeyID:     t.Name(),
			SecretAccessKey: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing access key ID", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &R2Config{
			AccountID:       t.Name(),
			SecretAccessKey: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing secret access key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &R2Config{
			AccountID:   t.Name(),
			AccessKeyID: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}
