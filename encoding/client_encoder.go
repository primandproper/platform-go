package encoding

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"io"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/BurntSushi/toml"
	"github.com/keith-turner/ecoji/v2"
	"gopkg.in/yaml.v3"
)

type (
	// ClientEncoder is an encoder for a service client.
	ClientEncoder interface {
		ContentType() string
		Unmarshal(ctx context.Context, data []byte, v any) error
		Encode(ctx context.Context, dest io.Writer, v any) error
		EncodeReader(ctx context.Context, data any) (io.Reader, error)
	}

	// clientEncoder is our concrete implementation of ClientEncoder.
	clientEncoder struct {
		o11y        observability.Observer
		contentType *contentType
	}
)

func (e *clientEncoder) Unmarshal(ctx context.Context, data []byte, v any) error {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set("data_length", len(data))
	var unmarshalFunc func(data []byte, v any) error

	switch e.contentType {
	case ContentTypeXML:
		unmarshalFunc = xml.Unmarshal
	case ContentTypeEmoji:
		unmarshalFunc = unmarshalEmoji
	case ContentTypeTOML:
		unmarshalFunc = toml.Unmarshal
	case ContentTypeYAML:
		unmarshalFunc = yaml.Unmarshal
	default:
		unmarshalFunc = json.Unmarshal
	}

	if err := unmarshalFunc(data, v); err != nil {
		return observability.PrepareError(err, op.Span(), "unmarshaling JSON content")
	}

	op.Logger().Debug("unmarshalled")

	return nil
}

func (e *clientEncoder) Encode(ctx context.Context, dest io.Writer, data any) error {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	var err error

	switch e.contentType {
	case ContentTypeXML:
		err = xml.NewEncoder(dest).Encode(data)
	case ContentTypeTOML:
		err = toml.NewEncoder(dest).Encode(data)
	case ContentTypeYAML:
		err = yaml.NewEncoder(dest).Encode(data)
	case ContentTypeEmoji:
		emojiEncoded, emojiEncodeErr := marshalEmoji(data)
		if emojiEncodeErr != nil {
			return emojiEncodeErr
		}

		_, err = dest.Write(emojiEncoded)
	default:
		err = json.NewEncoder(dest).Encode(data)
	}

	if err != nil {
		return observability.PrepareError(err, op.Span(), "encoding content")
	}

	return nil
}

func marshalEmoji(v any) ([]byte, error) {
	var gobWriter bytes.Buffer
	if err := gob.NewEncoder(&gobWriter).Encode(v); err != nil {
		return nil, errors.Wrap(err, "encoding to gob")
	}

	r := bytes.NewBuffer(gobWriter.Bytes())
	w := bytes.NewBuffer([]byte{})

	// lord help me, I don't know why it's 76 here
	if err := ecoji.EncodeV2(r, w, 76); err != nil {
		return nil, errors.Wrap(err, "encoding to emoji")
	}

	return w.Bytes(), nil
}

func unmarshalEmoji(data []byte, v any) error {
	w := bytes.NewBuffer([]byte{})

	if err := ecoji.Decode(bytes.NewReader(data), w); err != nil {
		return errors.Wrap(err, "decoding emoji")
	}

	return gob.NewDecoder(w).Decode(v)
}

func tomlMarshalFunc(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(v); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

func (e *clientEncoder) EncodeReader(ctx context.Context, data any) (io.Reader, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	var marshalFunc func(v any) ([]byte, error)

	switch e.contentType {
	case ContentTypeXML:
		marshalFunc = xml.Marshal
	case ContentTypeTOML:
		marshalFunc = tomlMarshalFunc
	case ContentTypeYAML:
		marshalFunc = yaml.Marshal
	case ContentTypeEmoji:
		marshalFunc = marshalEmoji
	default:
		marshalFunc = json.Marshal
	}

	out, err := marshalFunc(data)
	if err != nil {
		return nil, observability.PrepareError(err, op.Span(), "marshaling to XML")
	}

	return bytes.NewReader(out), nil
}

// ProvideClientEncoder provides a ClientEncoder.
func ProvideClientEncoder(logger logging.Logger, tracerProvider tracing.TracerProvider, encoding *contentType) ClientEncoder {
	return &clientEncoder{
		o11y:        observability.NewObserver("client_encoder", logger, tracerProvider),
		contentType: encoding,
	}
}
