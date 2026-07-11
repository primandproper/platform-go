package encoding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v4/observability"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/keith-turner/ecoji/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingClientEncoder builds a clientEncoder with a RecordingObserver
// swapped in, so a test can both drive the encoder and assert which fields it
// observed.
func newRecordingClientEncoder(t *testing.T, ct ContentType) (*clientEncoder, *observability.RecordingObserver) {
	t.Helper()

	e, ok := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct).(*clientEncoder)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	e.o11y = obs

	return e, obs
}

func TestNewClientEncoder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON))
	})
}

func Test_clientEncoder_Unmarshal(T *testing.T) {
	T.Parallel()

	testCases := map[string]struct {
		contentType ContentType
		expected    string
	}{
		"json": {
			contentType: ContentTypeJSON,
			expected:    `{"name": "name"}`,
		},
		"xml": {
			contentType: ContentTypeXML,
			expected:    `<example><name>name</name></example>`,
		},
		"toml": {
			contentType: ContentTypeTOML,
			expected:    `name = "name"`,
		},
		"yaml": {
			contentType: ContentTypeYAML,
			expected:    `name: "name"`,
		},
		"emoji": {
			contentType: ContentTypeEmoji,
			expected:    "🍃🧁🌆🙍☔🌾🐯🦮💆🚂🚕🏏🧔✊🀄🏏☔🌊🥈🐾👥♓🙌🀄🀄🍧🦖📓♿😱🦨🐶🀄☕\n",
		},
	}

	for name, tc := range testCases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), tc.contentType)

			expected := &example{Name: "name"}
			actual := &example{}

			test.NoError(t, e.Unmarshal(ctx, []byte(tc.expected), &actual))
			test.Eq(t, expected, actual)
		})
	}

	T.Run("observes data length", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e, obs := newRecordingClientEncoder(t, ContentTypeJSON)

		data := []byte(`{"name": "name"}`)
		actual := &example{}

		test.NoError(t, e.Unmarshal(ctx, data, &actual))

		obs.ObservedOperationWithData(t, map[string]any{
			"data_length": len(data),
		})
	})

	T.Run("with invalid data", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON)

		actual := &example{}

		test.Error(t, e.Unmarshal(ctx, []byte(`{"name"   `), &actual))
		test.EqOp(t, "", actual.Name)
	})

	T.Run("observes data length even on failure", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e, obs := newRecordingClientEncoder(t, ContentTypeJSON)

		data := []byte(`{"name"   `)
		actual := &example{}

		test.Error(t, e.Unmarshal(ctx, data, &actual))

		obs.ObservedOperationWithData(t, map[string]any{
			"data_length": len(data),
		})
	})
}

func Test_clientEncoder_Encode(T *testing.T) {
	T.Parallel()

	for _, ct := range ContentTypes {
		T.Run(ContentTypeToString(ct), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct)

			res := httptest.NewRecorder()

			test.NoError(t, e.Encode(ctx, res, &example{Name: t.Name()}))
		})
	}

	for _, ct := range ContentTypes {
		T.Run(fmt.Sprintf("%s handles io.Writer errors", ContentTypeToString(ct)), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct)

			mw := &ioWriterMock{
				WriteFunc: func(_ []byte) (int, error) {
					return 0, errors.New("blah")
				},
			}

			test.Error(t, e.Encode(ctx, mw, &example{Name: t.Name()}))
			test.SliceLen(t, 1, mw.WriteCalls())
		})
	}

	T.Run("with invalid data", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON)

		test.Error(t, e.Encode(ctx, nil, &broken{Name: json.Number(t.Name())}))
	})

	T.Run("with emoji encode error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeEmoji)

		var b bytes.Buffer
		test.Error(t, e.Encode(ctx, &b, make(chan int)))
	})
}

func Test_clientEncoder_EncodeReader(T *testing.T) {
	T.Parallel()

	for _, ct := range ContentTypes {
		T.Run(ContentTypeToString(ct), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ct)

			actual, err := e.EncodeReader(ctx, &example{Name: t.Name()})
			test.NoError(t, err)
			test.NotNil(t, actual)
		})
	}

	T.Run("with invalid data", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		e := NewClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON)

		actual, err := e.EncodeReader(ctx, &broken{Name: json.Number(t.Name())})
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

func Test_marshalEmoji(T *testing.T) {
	T.Parallel()

	T.Run("with un-encodable data", func(t *testing.T) {
		t.Parallel()

		_, err := marshalEmoji(make(chan int))
		test.Error(t, err)
	})
}

func Test_unmarshalEmoji(T *testing.T) {
	T.Parallel()

	T.Run("with invalid ecoji data", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.Error(t, unmarshalEmoji([]byte("not valid ecoji data"), &dest))
	})

	T.Run("with valid ecoji but invalid gob data", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		must.NoError(t, ecoji.EncodeV2(bytes.NewReader([]byte("not valid gob data")), &buf, 76))

		var dest example
		test.Error(t, unmarshalEmoji(buf.Bytes(), &dest))
	})
}

func Test_tomlMarshalFunc(T *testing.T) {
	T.Parallel()

	T.Run("with un-encodable data", func(t *testing.T) {
		t.Parallel()

		_, err := tomlMarshalFunc(make(chan int))
		test.Error(t, err)
	})
}
