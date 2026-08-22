package algolia

import (
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"

	"github.com/shoenig/test"
)

type example struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestNewIndexManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		im, err := NewIndexManager[example](&Config{}, "test", cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	T.Run("with timeout configured", func(t *testing.T) {
		t.Parallel()

		im, err := NewIndexManager[example](&Config{Timeout: 5 * time.Second}, "test", cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		im, err := NewIndexManager[example](nil, "test", cbnoop.NewCircuitBreaker())
		test.Error(t, err)
		test.Nil(t, im)
	})
}
