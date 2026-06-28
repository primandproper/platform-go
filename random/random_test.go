package random

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type erroneousReader struct{}

func (r *erroneousReader) Read(p []byte) (n int, err error) {
	return -1, errors.New("blah")
}

// newRecordingGenerator builds a standardGenerator with a RecordingObserver swapped
// in, so a test can both drive the generator and assert which fields it observed.
func newRecordingGenerator(t *testing.T) (*standardGenerator, *observability.RecordingObserver) {
	t.Helper()

	s, ok := NewGenerator(nil, tracingnoop.NewTracerProvider()).(*standardGenerator)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	s.o11y = obs

	return s, obs
}

func TestGenerateBase32EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, err := GenerateBase32EncodedString(ctx, 32)
		test.NoError(t, err)
		test.NotEq(t, "", actual)
	})
}

func TestGenerateBase64EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, err := GenerateBase64EncodedString(ctx, 32)
		test.NoError(t, err)
		test.NotEq(t, "", actual)
	})
}

func TestGenerateRawBytes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, err := GenerateRawBytes(ctx, 32)
		test.NoError(t, err)
		test.SliceNotEmpty(t, actual)
	})
}

func TestStandardSecretGenerator_GenerateBase32EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		value, err := s.GenerateBase32EncodedString(ctx, exampleLength)

		test.NotEq(t, "", value)
		test.Greater(t, exampleLength, len(value))
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
	})

	T.Run("with error reading from secure PRNG", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		s.randReader = &erroneousReader{}
		value, err := s.GenerateBase32EncodedString(ctx, exampleLength)

		test.EqOp(t, "", value)
		test.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestStandardSecretGenerator_GenerateBase64EncodedString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		value, err := s.GenerateBase64EncodedString(ctx, exampleLength)

		test.NotEq(t, "", value)
		test.Greater(t, exampleLength, len(value))
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
	})

	T.Run("with error reading from secure PRNG", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		s.randReader = &erroneousReader{}
		value, err := s.GenerateBase64EncodedString(ctx, exampleLength)

		test.EqOp(t, "", value)
		test.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestStandardSecretGenerator_GenerateRawBytes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		value, err := s.GenerateRawBytes(ctx, exampleLength)

		test.SliceNotEmpty(t, value)
		test.EqOp(t, exampleLength, len(value))
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
	})

	T.Run("with error reading from secure PRNG", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleLength := 123

		s, obs := newRecordingGenerator(t)
		s.randReader = &erroneousReader{}
		value, err := s.GenerateRawBytes(ctx, exampleLength)

		test.SliceEmpty(t, value)
		test.Error(t, err)

		// GenerateRawBytes attaches the length to the span via PrepareError rather
		// than routing through op.Error, so the value is still observed even on the
		// failure path.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: exampleLength,
		})
	})
}

func TestMustGenerateRawBytes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		result := MustGenerateRawBytes(ctx, 32)
		test.SliceNotEmpty(t, result)
	})
}

func TestGenerateHexEncodedString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		result, err := GenerateHexEncodedString(ctx, 32)
		test.NoError(t, err)
		test.NotEq(t, "", result)
	})
}

//nolint:paralleltest // mutates package-level defaultGenerator; subtests must run sequentially
func TestMustGenerateHexEncodedString(T *testing.T) {
	T.Run("standard", func(t *testing.T) { //nolint:paralleltest // mutates package-level defaultGenerator; subtests must run sequentially
		ctx := t.Context()

		var result string
		test.NotPanic(t, func() {
			result = MustGenerateHexEncodedString(ctx, 32)
		})
		test.NotEq(t, "", result)
		test.EqOp(t, 64, len(result))
	})

	T.Run("panics on error", func(t *testing.T) { //nolint:paralleltest // mutates package-level defaultGenerator; subtests must run sequentially
		ctx := t.Context()

		original := defaultGenerator.(*standardGenerator).randReader
		defaultGenerator.(*standardGenerator).randReader = &erroneousReader{}
		t.Cleanup(func() {
			defaultGenerator.(*standardGenerator).randReader = original
		})

		test.Panic(t, func() {
			_ = MustGenerateHexEncodedString(ctx, 32)
		})
	})
}
