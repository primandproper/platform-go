package encoding

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/internal/cbormode"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"gopkg.in/yaml.v3"
)

// newRecordingServerEncoderDecoder builds an EncoderDecoder with a
// RecordingObserver swapped in, so a test can both drive the encoder and assert
// which fields it observed.
func newRecordingServerEncoderDecoder(t *testing.T, ct ContentType) (*EncoderDecoder, *observability.RecordingObserver) {
	t.Helper()

	ed := NewServerEncoderDecoder(ct)

	obs := observability.NewRecordingObserver()
	ed.o11y = obs

	return ed, obs
}

type example struct {
	Name string `json:"name" xml:"name"`
}

type broken struct {
	Name json.Number `json:"name" xml:"name"`
}

func init() {
	gob.Register(&example{})
	gob.Register(&broken{})
}

type errReader struct{}

func (r *errReader) Read([]byte) (int, error) {
	return 0, errors.New("read error")
}

type errWriter struct{}

func (w *errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write error")
}

type errorCloser struct {
	io.Reader
}

func (e *errorCloser) Close() error {
	return errors.New("close error")
}

func TestServerEncoderDecoder_encodeResponse(T *testing.T) {
	T.Parallel()

	testCases := map[string]struct {
		contentType      ContentType
		expectedResponse string
	}{
		// No trailing newline: JSON marshals to exactly json.Marshal's bytes.
		// TOML and YAML below keep theirs because those formats end a document
		// with one — that newline comes from the format, not from the encoder.
		"json": {
			contentType:      ContentTypeJSON,
			expectedResponse: `{"name":"name"}`,
		},
		"xml": {
			contentType:      ContentTypeXML,
			expectedResponse: "<example><name>name</name></example>",
		},
		"toml": {
			contentType:      ContentTypeTOML,
			expectedResponse: `Name = "name"` + "\n",
		},
		"yaml": {
			contentType:      ContentTypeYAML,
			expectedResponse: "name: name\n",
		},
		// A one-pair CBOR map: 0xa1, then the text strings "name" and "name".
		"cbor": {
			contentType:      ContentTypeCBOR,
			expectedResponse: "\xa1dnamedname",
		},
	}

	for testName, tc := range testCases {
		T.Run(testName, func(t *testing.T) {
			t.Parallel()

			ex := &example{Name: "name"}
			encoderDecoder := NewServerEncoderDecoder(tc.contentType)

			ctx := t.Context()
			res := httptest.NewRecorder()
			res.Header().Set(ContentTypeHeaderKey, tc.contentType.String())

			encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)
			actual := res.Body.String()
			test.EqOp(t, tc.expectedResponse, actual)
		})
	}

	T.Run("emoji", func(t *testing.T) {
		t.Parallel()

		ex := &example{Name: "name"}
		encoderDecoder := NewServerEncoderDecoder(ContentTypeEmoji)

		ctx := t.Context()
		res := httptest.NewRecorder()
		res.Header().Set(ContentTypeHeaderKey, ContentTypeEmoji.String())

		encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)
		actual := res.Body.String()
		test.NotEq(t, "", actual)
	})

	T.Run("defaults to JSON", func(t *testing.T) {
		t.Parallel()
		expectation := "name"
		ex := &example{Name: expectation}
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		ctx := t.Context()
		res := httptest.NewRecorder()

		encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)
		test.EqOp(t, fmt.Sprintf("{%q:%q}", "name", ex.Name), res.Body.String())
	})

	T.Run("honors configured content type without a pre-set header", func(t *testing.T) {
		t.Parallel()

		ex := &example{Name: "name"}
		encoderDecoder := NewServerEncoderDecoder(ContentTypeXML)

		ctx := t.Context()
		res := httptest.NewRecorder()

		// The handler never sets a content type header; the configured XML encoder must still win.
		encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)

		test.EqOp(t, "<example><name>name</name></example>", res.Body.String())
		test.EqOp(t, contentTypeXML, res.Header().Get(ContentTypeHeaderKey))
	})

	T.Run("observes response status", func(t *testing.T) {
		t.Parallel()

		ex := &example{Name: "name"}
		encoderDecoder, obs := newRecordingServerEncoderDecoder(t, ContentTypeJSON)

		ctx := t.Context()
		res := httptest.NewRecorder()

		encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.ResponseStatusKey: http.StatusOK,
		})
	})

	T.Run("with broken structure", func(t *testing.T) {
		t.Parallel()
		expectation := "name"
		ex := &broken{Name: json.Number(expectation)}
		encoderDecoder, obs := newRecordingServerEncoderDecoder(t, ContentTypeJSON)

		ctx := t.Context()
		res := httptest.NewRecorder()

		encoderDecoder.encodeResponse(ctx, res, ex, http.StatusOK)
		test.EqOp(t, "", res.Body.String())

		// Even though encoding failed, the status must still have been observed,
		// and the failure itself acknowledged on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.ResponseStatusKey: http.StatusOK,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestServerEncoderDecoder_MustEncodeJSON(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		expected := `{"name":"TestServerEncoderDecoder_MustEncodeJSON/standard"}`
		actual := string(encoderDecoder.MustEncodeJSON(ctx, &example{Name: t.Name()}))

		test.EqOp(t, expected, actual)
	})

	T.Run("with panic", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		defer func() {
			test.NotNil(t, recover())
		}()

		encoderDecoder.MustEncodeJSON(ctx, &broken{Name: json.Number(t.Name())})
	})
}

func TestServerEncoderDecoder_MustEncode(T *testing.T) {
	T.Parallel()

	testCases := map[string]struct {
		contentType ContentType
		expected    string
	}{
		"json": {
			contentType: ContentTypeJSON,
			expected:    `{"name":"TestServerEncoderDecoder_MustEncode/json"}`,
		},
		"xml": {
			contentType: ContentTypeXML,
			expected:    "<example><name>TestServerEncoderDecoder_MustEncode/xml</name></example>",
		},
		"toml": {
			contentType: ContentTypeTOML,
			expected:    "Name = \"TestServerEncoderDecoder_MustEncode/toml\"\n",
		},
		"yaml": {
			contentType: ContentTypeYAML,
			expected:    "name: TestServerEncoderDecoder_MustEncode/yaml\n",
		},
	}

	for name, tc := range testCases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			encoderDecoder := NewServerEncoderDecoder(tc.contentType)

			actual := string(encoderDecoder.MustEncode(ctx, &example{Name: t.Name()}))

			test.EqOp(t, tc.expected, actual)
		})
	}

	T.Run("emoji", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeEmoji)

		actual := string(encoderDecoder.MustEncode(ctx, &example{Name: t.Name()}))
		test.NotEq(t, "", actual)
	})

	// Asserted by round trip rather than by byte literal: the subtest's own
	// name is the payload, so a literal here would be a copy of t.Name().
	T.Run("cbor", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeCBOR)

		encoded := encoderDecoder.MustEncode(ctx, &example{Name: t.Name()})
		must.SliceNotEmpty(t, encoded)

		var decoded example
		must.NoError(t, encoderDecoder.DecodeBytes(ctx, encoded, &decoded))
		test.EqOp(t, t.Name(), decoded.Name)
	})

	T.Run("with broken struct", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		defer func() {
			test.NotNil(t, recover())
		}()

		encoderDecoder.MustEncode(ctx, &broken{Name: json.Number(t.Name())})
	})
}

func TestServerEncoderDecoder_EncodeResponseWithStatus(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		expectation := "name"
		ex := &example{Name: expectation}
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		ctx := t.Context()
		res := httptest.NewRecorder()

		expected := 666
		encoderDecoder.EncodeResponseWithStatus(ctx, res, ex, expected)

		test.EqOp(t, expected, res.Code, test.Sprintf("expected code to be %d, but got %d", expected, res.Code))
		test.EqOp(t, fmt.Sprintf("{%q:%q}", "name", ex.Name), res.Body.String())
	})
}

func TestServerEncoderDecoder_DecodeRequest(T *testing.T) {
	T.Parallel()

	testCases := map[string]struct {
		contentType ContentType
		marshaller  func(v any) ([]byte, error)
		expected    string
	}{
		"json": {
			contentType: ContentTypeJSON,
			expected:    `{"name":"name"}`,
			marshaller:  json.Marshal,
		},
		"xml": {
			contentType: ContentTypeXML,
			expected:    `<example><name>name</name></example>`,
			marshaller:  xml.Marshal,
		},
		"toml": {
			contentType: ContentTypeTOML,
			expected:    `<example><name>name</name></example>`,
			marshaller:  tomlMarshalFunc,
		},
		"yaml": {
			contentType: ContentTypeYAML,
			expected:    `<example><name>name</name></example>`,
			marshaller:  yaml.Marshal,
		},
		"cbor": {
			contentType: ContentTypeCBOR,
			expected:    `<example><name>name</name></example>`,
			marshaller:  cbormode.Marshal,
		},
		"emoji": {
			contentType: ContentTypeEmoji,
			expected:    `<example><name>name</name></example>`,
			marshaller:  marshalEmoji,
		},
	}

	e := &example{Name: "name"}

	for name, tc := range testCases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			encoderDecoder := NewServerEncoderDecoder(tc.contentType)

			bs, err := tc.marshaller(e)
			must.NoError(t, err)

			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				"https://whatever.whocares.gov",
				bytes.NewReader(bs),
			)
			must.NoError(t, err)
			req.Header.Set(ContentTypeHeaderKey, tc.contentType.String())

			var x example
			test.NoError(t, encoderDecoder.DecodeRequest(ctx, req, &x))
			test.EqOp(t, e.Name, x.Name)
		})
	}
}

func Test_serverEncoderDecoder_DecodeBytes(T *testing.T) {
	T.Parallel()

	goodDataTestCases := map[string]struct {
		contentType ContentType
		data        []byte
	}{
		"json": {
			data:        []byte(`{"name":"name"}`),
			contentType: ContentTypeJSON,
		},
		"xml": {
			data:        []byte(`<example><name>name</name></example>`),
			contentType: ContentTypeXML,
		},
		"toml": {
			data:        []byte(`name = "name"`),
			contentType: ContentTypeTOML,
		},
		"yaml": {
			data:        []byte(`name: "name"`),
			contentType: ContentTypeYAML,
		},
		// A one-pair CBOR map: 0xa1, then the text strings "name" and "name".
		"cbor": {
			data:        []byte("\xa1dnamedname"),
			contentType: ContentTypeCBOR,
		},
		"emoji": {
			data:        []byte("🍃🧁🌆🙍☔🌾🐯🦮💆🚂🚕🏏🧔✊🀄🏏☔🌊🥈🐾👥♓🙌🀄🀄🍧🦖📓♿😱🦨🐶🀄☕\n"),
			contentType: ContentTypeEmoji,
		},
	}
	goodDataExpectation := &example{Name: "name"}

	for name, tc := range goodDataTestCases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			encoderDecoder := NewServerEncoderDecoder(tc.contentType)

			var dest *example
			test.NoError(t, encoderDecoder.DecodeBytes(ctx, tc.data, &dest))

			test.Eq(t, goodDataExpectation, dest)
		})
	}
}

func TestServerEncoderDecoder_RespondWithData(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		res := httptest.NewRecorder()

		encoderDecoder.RespondWithData(ctx, res, &example{Name: t.Name()})

		test.EqOp(t, http.StatusOK, res.Code)
		test.NotEq(t, "", res.Body.String())
	})
}

func Test_tomlDecoder_Decode(T *testing.T) {
	T.Parallel()

	T.Run("with read error", func(t *testing.T) {
		t.Parallel()

		d := newTomlDecoder(&errReader{})

		var dest example
		test.Error(t, d.Decode(&dest))
	})
}

// The emoji path no longer has a streaming encoder of its own — every content
// type marshals to bytes and writes them — so the cases that adapter carried
// belong to Encoder.Encode now.
func Test_clientEncoder_Encode_errors(T *testing.T) {
	T.Parallel()

	T.Run("with marshal error", func(t *testing.T) {
		t.Parallel()

		enc := NewClientEncoder(ContentTypeEmoji)
		test.Error(t, enc.Encode(t.Context(), &bytes.Buffer{}, make(chan int)))
	})

	T.Run("with write error", func(t *testing.T) {
		t.Parallel()

		enc := NewClientEncoder(ContentTypeEmoji)
		test.Error(t, enc.Encode(t.Context(), &errWriter{}, &example{Name: "test"}))
	})
}

func Test_emojiDecoder_Decode(T *testing.T) {
	T.Parallel()

	T.Run("with read error", func(t *testing.T) {
		t.Parallel()

		d := newEmojiDecoder(&errReader{})

		var dest example
		test.Error(t, d.Decode(&dest))
	})
}

func TestServerEncoderDecoder_DecodeRequest_bodyCloseError(T *testing.T) {
	T.Parallel()

	T.Run("with body close error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		encoderDecoder := NewServerEncoderDecoder(ContentTypeJSON)

		data, err := json.Marshal(&example{Name: "test"})
		must.NoError(t, err)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://whatever.whocares.gov", &errorCloser{Reader: bytes.NewReader(data)})
		must.NoError(t, err)
		req.Header.Set(ContentTypeHeaderKey, contentTypeJSON)

		var dest example
		test.NoError(t, encoderDecoder.DecodeRequest(ctx, req, &dest))
		test.EqOp(t, "test", dest.Name)
	})
}
