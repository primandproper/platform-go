package encoding

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cbormode"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/panicking"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

const (
	// ContentTypeHeaderKey is the HTTP standard header name for content type.
	ContentTypeHeaderKey = "Content-type"

	o11yName = "server_encoder_decoder"

	contentTypeKey = "content_type"

	contentTypeXML   = "application/xml"
	contentTypeJSON  = "application/json"
	contentTypeTOML  = "application/toml"
	contentTypeYAML  = "application/yaml"
	contentTypeCBOR  = "application/cbor"
	contentTypeEmoji = "application/emoji"
)

var (
	defaultContentType = ContentTypeJSON
)

type (
	// ServerEncoderDecoder is an interface that allows for multiple implementations of HTTP response formats.
	ServerEncoderDecoder interface {
		RespondWithData(ctx context.Context, res http.ResponseWriter, val any)
		EncodeResponseWithStatus(ctx context.Context, res http.ResponseWriter, val any, statusCode int)
		DecodeRequest(ctx context.Context, req *http.Request, dest any) error
		DecodeBytes(ctx context.Context, payload []byte, dest any) error
		MustEncode(ctx context.Context, v any) []byte
		MustEncodeJSON(ctx context.Context, v any) []byte
	}

	// EncoderDecoder is our concrete implementation of ServerEncoderDecoder,
	// speaking one ContentType for its whole life. It is exported, and returned
	// by NewServerEncoderDecoder, so a caller can depend on the encoder it built
	// rather than on the ServerEncoderDecoder seam.
	EncoderDecoder struct {
		o11y        observability.Observer
		panicker    panicking.Panicker
		contentType ContentType
	}

	decoder interface {
		Decode(v any) error
	}
)

var _ ServerEncoderDecoder = (*EncoderDecoder)(nil)

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

// decoderFor returns the streaming decoder for a content type.
//
// JSON is a case like any other rather than the default: a content type this
// package does not implement is ErrUnsupportedContentType, not a JSON decoder
// quietly pointed at somebody else's wire format.
func decoderFor(ct ContentType, r io.Reader) (decoder, error) {
	switch ct {
	case ContentTypeJSON:
		dec := json.NewDecoder(r)

		// Unknown fields are rejected rather than ignored, so a typo'd or stale
		// field in a payload surfaces as a decode error instead of a zero value.
		dec.DisallowUnknownFields()

		return dec, nil
	case ContentTypeXML:
		return xml.NewDecoder(r), nil
	case ContentTypeTOML:
		return newTomlDecoder(r), nil
	case ContentTypeYAML:
		return yaml.NewDecoder(r), nil
	case ContentTypeCBOR:
		return cbormode.NewDecoder(r), nil
	case ContentTypeEmoji:
		return newEmojiDecoder(r), nil
	default:
		return nil, errors.Wrapf(ErrUnsupportedContentType, "decoding %q", ct)
	}
}

// DecodeBytes decodes bytes into values.
func (e *EncoderDecoder) DecodeBytes(ctx context.Context, data []byte, dest any) error {
	_, op := e.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(data)))
	defer op.End()

	d, err := decoderFor(e.contentType, bytes.NewReader(data))
	if err != nil {
		return observability.PrepareError(err, op.Span(), "decoding content")
	}

	return d.Decode(dest)
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
func (e *EncoderDecoder) encodeResponse(ctx context.Context, res http.ResponseWriter, v any, statusCode int) {
	_, op := e.o11y.Begin(ctx, observability.WithValue(keys.ResponseStatusKey, statusCode))
	defer op.End()

	// Resolved before anything is written. A response this encoder cannot
	// produce is a server fault, and a 200 with an empty body would tell the
	// client the opposite — which is not recoverable once the status is out.
	marshalFunc, err := marshalFuncFor(e.contentType)
	if err != nil {
		op.Acknowledge(err, "encoding response")
		res.WriteHeader(http.StatusInternalServerError)

		return
	}

	// choose the encoder from the configured content type, not the writer's pre-set header,
	// so a configured encoder is honored even when the handler never sets a header.
	res.Header().Set(ContentTypeHeaderKey, e.contentType.String())
	res.WriteHeader(statusCode)

	out, err := marshalFunc(v)
	if err != nil {
		op.Acknowledge(err, "encoding response")

		return
	}

	if _, err = res.Write(out); err != nil {
		op.Acknowledge(err, "writing response")
	}
}

func (e *EncoderDecoder) MustEncodeJSON(ctx context.Context, v any) []byte {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	out, err := json.Marshal(v)
	if err != nil {
		e.panicker.Panic(errors.Wrap(err, "encoding JSON content"))
	}

	return out
}

// MustEncode encodes data or else.
func (e *EncoderDecoder) MustEncode(ctx context.Context, v any) []byte {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	marshalFunc, err := marshalFuncFor(e.contentType)
	if err != nil {
		e.panicker.Panic(errors.Wrapf(err, "encoding %s content", e.contentType))
	}

	out, err := marshalFunc(v)
	if err != nil {
		e.panicker.Panic(errors.Wrapf(err, "encoding %s content", e.contentType))
	}

	return out
}

// RespondWithData encodes successful responses with data.
func (e *EncoderDecoder) RespondWithData(ctx context.Context, res http.ResponseWriter, v any) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	e.encodeResponse(ctx, res, v, http.StatusOK)
}

// EncodeResponseWithStatus encodes responses and writes the provided status to the response.
func (e *EncoderDecoder) EncodeResponseWithStatus(ctx context.Context, res http.ResponseWriter, v any, statusCode int) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	e.encodeResponse(ctx, res, v, statusCode)
}

// DecodeRequest decodes request bodies into values.
func (e *EncoderDecoder) DecodeRequest(ctx context.Context, req *http.Request, v any) error {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	defer func() {
		if err := req.Body.Close(); err != nil {
			op.Logger().Error("closing request body", err)
		}
	}()

	// contentTypeFromRequestHeader only ever yields a supported ContentType — an
	// unlabeled or unrecognized request body is JSON by documented, inbound-only
	// design — so decoderFor cannot fail here. The error is still checked rather
	// than dropped, because the day that stops being true it should say so.
	d, err := decoderFor(contentTypeFromRequestHeader(req.Header.Get(ContentTypeHeaderKey)), req.Body)
	if err != nil {
		return observability.PrepareError(err, op.Span(), "decoding request")
	}

	return d.Decode(v)
}

// NewServerEncoderDecoder provides a ServerEncoderDecoder.
//
// As with NewClientEncoder, an unsupported ContentType is reported from every
// operation as ErrUnsupportedContentType rather than silently served as JSON.
// Resolve configuration through ParseContentType, which refuses it up front.
func NewServerEncoderDecoder(contentType ContentType, opts ...Option) *EncoderDecoder {
	cfg := newOptions(opts)

	return &EncoderDecoder{
		// An encoder/decoder speaks one content type for its whole life, and every
		// operation below is about it, so it is stated once here. Previously only
		// DecodeBytes recorded it, which is exactly the "set at some call sites and
		// forgotten at the rest" case this constructor exists for.
		o11y: observability.NewObserverWithValues(o11yName, cfg.logger, cfg.tracerProvider,
			map[string]any{contentTypeKey: contentType.String()}),
		panicker:    panicking.NewProductionPanicker(),
		contentType: contentType,
	}
}
