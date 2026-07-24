package encoding

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"

	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	"github.com/primandproper/platform-go/v6/panicking"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

const (
	// ContentTypeHeaderKey is the HTTP standard header name for content type.
	ContentTypeHeaderKey = "Content-type"

	o11yName = "server_encoder_decoder"

	contentTypeXML   = "application/xml"
	contentTypeJSON  = "application/json"
	contentTypeTOML  = "application/toml"
	contentTypeYAML  = "application/yaml"
	contentTypeEmoji = "application/emoji"
)

var (
	defaultContentType = ContentTypeJSON
)

type (
	// ServerEncoderDecoder is an interface that allows for multiple implementations of HTTP response formats.
	ServerEncoderDecoder interface {
		EncodeResponseWithStatus(ctx context.Context, res http.ResponseWriter, val any, statusCode int)
		DecodeRequest(ctx context.Context, req *http.Request, dest any) error
		DecodeBytes(ctx context.Context, payload []byte, dest any) error
		MustEncode(ctx context.Context, v any) []byte
		MustEncodeJSON(ctx context.Context, v any) []byte
	}

	// serverEncoderDecoder is our concrete implementation of EncoderDecoder.
	serverEncoderDecoder struct {
		o11y        observability.Observer
		panicker    panicking.Panicker
		contentType ContentType
	}

	encoder interface {
		Encode(any) error
	}

	decoder interface {
		Decode(v any) error
	}
)

type tomlDecoder struct {
	reader io.Reader
}

func newTomlDecoder(reader io.Reader) decoder {
	return &tomlDecoder{reader: reader}
}

func (t *tomlDecoder) Decode(v any) error {
	x, err := io.ReadAll(t.reader)
	if err != nil {
		return err
	}

	return toml.Unmarshal(x, v)
}

// DecodeBytes decodes bytes into values.
func (e *serverEncoderDecoder) DecodeBytes(ctx context.Context, data []byte, dest any) error {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(data)).Set("content_type", ContentTypeToString(e.contentType))

	var d decoder
	switch e.contentType {
	case ContentTypeXML:
		d = xml.NewDecoder(bytes.NewReader(data))
	case ContentTypeTOML:
		d = newTomlDecoder(bytes.NewReader(data))
	case ContentTypeYAML:
		d = yaml.NewDecoder(bytes.NewReader(data))
	case ContentTypeEmoji:
		d = newEmojiDecoder(bytes.NewReader(data))
	default:
		dec := json.NewDecoder(bytes.NewReader(data))

		// if the below line is commented, it means you eat at weenie hut jr's.
		dec.DisallowUnknownFields()

		d = dec
	}

	return d.Decode(dest)
}

type emojiEncoder struct {
	w io.Writer
}

func newEmojiEncoder(w io.Writer) encoder {
	return &emojiEncoder{w: w}
}

func (e *emojiEncoder) Encode(a any) error {
	encodedContent, err := marshalEmoji(a)
	if err != nil {
		return err
	}

	_, err = e.w.Write(encodedContent)

	return err
}

type emojiDecoder struct {
	r io.Reader
}

func newEmojiDecoder(r io.Reader) decoder {
	return &emojiDecoder{r: r}
}

func (e *emojiDecoder) Decode(v any) error {
	encodedContent, err := io.ReadAll(e.r)
	if err != nil {
		return err
	}

	return unmarshalEmoji(encodedContent, v)
}

// encodeResponse encodes responses.
func (e *serverEncoderDecoder) encodeResponse(ctx context.Context, res http.ResponseWriter, v any, statusCode int) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.ResponseStatusKey, statusCode)

	// choose the encoder from the configured content type, not the writer's pre-set header,
	// so a configured encoder is honored even when the handler never sets a header.
	var enc encoder
	switch e.contentType {
	case ContentTypeXML:
		res.Header().Set(ContentTypeHeaderKey, contentTypeXML)
		enc = xml.NewEncoder(res)
	case ContentTypeTOML:
		res.Header().Set(ContentTypeHeaderKey, contentTypeTOML)
		enc = toml.NewEncoder(res)
	case ContentTypeYAML:
		res.Header().Set(ContentTypeHeaderKey, contentTypeYAML)
		enc = yaml.NewEncoder(res)
	case ContentTypeEmoji:
		res.Header().Set(ContentTypeHeaderKey, contentTypeEmoji)
		enc = newEmojiEncoder(res)
	default:
		res.Header().Set(ContentTypeHeaderKey, contentTypeJSON)
		enc = json.NewEncoder(res)
	}

	res.WriteHeader(statusCode)
	if err := enc.Encode(v); err != nil {
		op.Acknowledge(err, "encoding response")
	}
}

func (e *serverEncoderDecoder) MustEncodeJSON(ctx context.Context, v any) []byte {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		e.panicker.Panic(errors.Wrap(err, "encoding JSON content"))
	}

	return b.Bytes()
}

// MustEncode encodes data or else.
func (e *serverEncoderDecoder) MustEncode(ctx context.Context, v any) []byte {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	var (
		enc encoder
		b   bytes.Buffer
	)

	switch e.contentType {
	case ContentTypeXML:
		enc = xml.NewEncoder(&b)
	case ContentTypeTOML:
		enc = toml.NewEncoder(&b)
	case ContentTypeYAML:
		enc = yaml.NewEncoder(&b)
	case ContentTypeEmoji:
		enc = newEmojiEncoder(&b)
	default:
		enc = json.NewEncoder(&b)
	}

	if err := enc.Encode(v); err != nil {
		e.panicker.Panic(errors.Wrapf(err, "encoding %s content", ContentTypeToString(e.contentType)))
	}

	return b.Bytes()
}

// RespondWithData encodes successful responses with data.
func (e *serverEncoderDecoder) RespondWithData(ctx context.Context, res http.ResponseWriter, v any) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	e.encodeResponse(ctx, res, v, http.StatusOK)
}

// EncodeResponseWithStatus encodes responses and writes the provided status to the response.
func (e *serverEncoderDecoder) EncodeResponseWithStatus(ctx context.Context, res http.ResponseWriter, v any, statusCode int) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	e.encodeResponse(ctx, res, v, statusCode)
}

// DecodeRequest decodes request bodies into values.
func (e *serverEncoderDecoder) DecodeRequest(ctx context.Context, req *http.Request, v any) error {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	var d decoder
	switch contentTypeFromString(req.Header.Get(ContentTypeHeaderKey)) {
	case ContentTypeXML:
		d = xml.NewDecoder(req.Body)
	case ContentTypeTOML:
		d = newTomlDecoder(req.Body)
	case ContentTypeYAML:
		d = yaml.NewDecoder(req.Body)
	case ContentTypeEmoji:
		d = newEmojiDecoder(req.Body)
	default:
		dec := json.NewDecoder(req.Body)

		// if the below line is commented, it means you eat at weenie hut jr's.
		dec.DisallowUnknownFields()

		d = dec
	}

	defer func() {
		if err := req.Body.Close(); err != nil {
			op.Logger().Error("closing request body", err)
		}
	}()

	return d.Decode(v)
}

// NewServerEncoderDecoder provides a ServerEncoderDecoder.
func NewServerEncoderDecoder(logger logging.Logger, tracerProvider tracing.TracerProvider, contentType ContentType) ServerEncoderDecoder {
	return &serverEncoderDecoder{
		o11y:        observability.NewObserver(o11yName, logger, tracerProvider),
		panicker:    panicking.NewProductionPanicker(),
		contentType: contentType,
	}
}
