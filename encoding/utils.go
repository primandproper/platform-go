package encoding

import (
	"bytes"
	"context"
	"io"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"
)

func Decode(data []byte, ct *contentType, dest any) error {
	if ct == nil {
		ct = ContentTypeJSON
	}

	if err := NewServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct).DecodeBytes(context.Background(), data, dest); err != nil {
		return err
	}

	return nil
}

// MustEncode encodes a given piece of data to a given encoding.
func MustEncode(data any, ct *contentType) []byte {
	if ct == nil {
		ct = ContentTypeJSON
	}

	var b bytes.Buffer
	if err := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct).Encode(context.Background(), &b, data); err != nil {
		panic(err)
	}

	return b.Bytes()
}

// MustDecode encodes a given piece of data to a given encoding.
func MustDecode(data []byte, ct *contentType, dest any) {
	if ct == nil {
		ct = ContentTypeJSON
	}

	if err := Decode(data, ct, dest); err != nil {
		panic(err)
	}
}

// MustEncodeJSON JSON encodes a piece of data.
func MustEncodeJSON(data any) []byte {
	return MustEncode(data, ContentTypeJSON)
}

func DecodeJSON(data []byte, dest any) error {
	return Decode(data, ContentTypeJSON, dest)
}

// MustDecodeJSON JSON encodes a piece of data.
func MustDecodeJSON(data []byte, dest any) {
	MustDecode(data, ContentTypeJSON, dest)
}

// MustJSONIntoReader JSON encodes a piece of data.
func MustJSONIntoReader(data any) io.Reader {
	return bytes.NewReader(MustEncode(data, ContentTypeJSON))
}
