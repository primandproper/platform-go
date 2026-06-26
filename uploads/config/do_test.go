package config

import (
	"testing"

	"github.com/primandproper/platform-go/uploads/objectstorage"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterStorageConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, &Config{
			Storage: objectstorage.Config{
				BucketName: t.Name(),
				Provider:   objectstorage.MemoryProvider,
			},
		})

		RegisterStorageConfig(i)

		storageCfg, err := do.Invoke[*objectstorage.Config](i)
		must.NoError(t, err)
		test.NotNil(t, storageCfg)
		test.EqOp(t, t.Name(), storageCfg.BucketName)
	})
}
