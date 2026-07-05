package config

import (
	"testing"

	"github.com/primandproper/platform-go/v4/uploads/objectstorage"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Storage: objectstorage.Config{
				FilesystemConfig: &objectstorage.FilesystemConfig{RootDirectory: "/blah"},
				BucketName:       "blahs",
				Provider:         "blahs",
			},
			Debug: false,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty storage", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})
}
