package files

import (
	"context"
	"io"
	"os"

	"github.com/primandproper/platform-go/v2/encoding"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

// Decode reads all of r and unmarshals it into a T as content type ct — any encoding the encoding
// package supports (JSON, XML, TOML, YAML, Emoji). It builds a one-off encoder internally.
func Decode[T any](ctx context.Context, r io.Reader, ct encoding.ContentType) (T, error) {
	var v T

	data, err := io.ReadAll(r)
	if err != nil {
		return v, errors.Wrap(err, "reading input")
	}

	return decodeBytes[T](ctx, defaultReader.logger, defaultReader.tracerProvider, data, ct)
}

// DecodeFile opens name, reads it, and unmarshals it into a T as content type ct. The read is traced
// via the default Reader, and the encoder traces under the same tracer.
func DecodeFile[T any](ctx context.Context, name string, ct encoding.ContentType) (T, error) {
	var v T

	data, err := defaultReader.readFile(ctx, name)
	if err != nil {
		return v, err
	}

	return decodeBytes[T](ctx, defaultReader.logger, defaultReader.tracerProvider, data, ct)
}

// MustDecode is like Decode but panics on error.
func MustDecode[T any](ctx context.Context, r io.Reader, ct encoding.ContentType) T {
	v, err := Decode[T](ctx, r, ct)
	if err != nil {
		panic(err)
	}

	return v
}

// MustDecodeFile is like DecodeFile but panics on error.
func MustDecodeFile[T any](ctx context.Context, name string, ct encoding.ContentType) T {
	v, err := DecodeFile[T](ctx, name, ct)
	if err != nil {
		panic(err)
	}

	return v
}

// decodeBytes unmarshals data into a T using a one-off encoder for ct. Empty input is rejected
// before the encoder sees it, since no supported encoding treats it as a valid document.
func decodeBytes[T any](ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, data []byte, ct encoding.ContentType) (T, error) {
	var v T

	if len(data) == 0 {
		return v, errors.ErrEmptyInputParameter
	}

	enc := encoding.ProvideClientEncoder(logger, tracerProvider, ct)
	if err := enc.Unmarshal(ctx, data, &v); err != nil {
		return v, errors.Wrapf(err, "decoding %T", v)
	}

	return v, nil
}

// readFile reads the whole of name into memory under a span.
func (r *standardReader) readFile(ctx context.Context, name string) ([]byte, error) {
	_, op := r.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, name)

	data, err := os.ReadFile(name)
	if err != nil {
		return nil, op.Error(err, "reading file")
	}

	op.Set(keys.LengthKey, len(data))

	return data, nil
}
