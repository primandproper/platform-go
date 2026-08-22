package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v13/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewGenerator(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil generator", func(t *testing.T) {
		t.Parallel()

		g := NewGenerator()
		must.NotNil(t, g)
	})
}

func TestGenerator_GenerateHexEncodedString(T *testing.T) {
	T.Parallel()

	T.Run("returns no value and ErrNoRandomness", func(t *testing.T) {
		t.Parallel()

		g := NewGenerator()
		s, err := g.GenerateHexEncodedString(t.Context(), 32)

		test.ErrorIs(t, err, random.ErrNoRandomness)
		test.EqOp(t, "", s)
	})
}

func TestGenerator_GenerateBase32EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("returns no value and ErrNoRandomness", func(t *testing.T) {
		t.Parallel()

		g := NewGenerator()
		s, err := g.GenerateBase32EncodedString(t.Context(), 32)

		test.ErrorIs(t, err, random.ErrNoRandomness)
		test.EqOp(t, "", s)
	})
}

func TestGenerator_GenerateBase64EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("returns no value and ErrNoRandomness", func(t *testing.T) {
		t.Parallel()

		g := NewGenerator()
		s, err := g.GenerateBase64EncodedString(t.Context(), 32)

		test.ErrorIs(t, err, random.ErrNoRandomness)
		test.EqOp(t, "", s)
	})
}

func TestGenerator_GenerateRawBytes(T *testing.T) {
	T.Parallel()

	T.Run("returns no value and ErrNoRandomness", func(t *testing.T) {
		t.Parallel()

		g := NewGenerator()
		b, err := g.GenerateRawBytes(t.Context(), 32)

		test.ErrorIs(t, err, random.ErrNoRandomness)
		test.Nil(t, b)
	})
}
