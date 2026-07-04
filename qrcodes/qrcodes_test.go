package qrcodes

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v3/observability"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingBuilder builds a *builder with a RecordingObserver swapped in, so a
// test can both drive BuildQRCode and assert that it observed an operation.
func newRecordingBuilder(t *testing.T) (*builder, *observability.RecordingObserver) {
	t.Helper()

	b := NewBuilder("test-issuer", nil, nil).(*builder)
	obs := observability.NewRecordingObserver()
	b.o11y = obs

	return b, obs
}

func TestNewBuilder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		b := NewBuilder("test-issuer", nil, nil)
		test.NotNil(t, b)
	})
}

func Test_builder_BuildQRCode(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		b, obs := newRecordingBuilder(t)

		actual, err := b.BuildQRCode(ctx, "username", "two-factor-secret")
		must.NoError(t, err)
		test.NotEq(t, "", actual)
		// The image is PNG-encoded, so the data URI must advertise image/png.
		test.StrHasPrefix(t, "data:image/png;base64,", actual)

		// BuildQRCode attaches no values, but it must still open and end an operation.
		obs.ObservedOperationWithData(t, map[string]any{})
	})

	T.Run("with content exceeding QR capacity", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		b := NewBuilder("test-issuer", nil, nil)

		// A username longer than the maximum QR code capacity forces qr.Encode to fail.
		actual, err := b.BuildQRCode(ctx, strings.Repeat("a", 4000), "two-factor-secret")
		test.EqOp(t, "", actual)
		test.Error(t, err)
	})

	T.Run("with scale error", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		b := NewBuilder("test-issuer", nil, nil).(*builder)
		b.scale = func(barcode.Barcode, int, int) (barcode.Barcode, error) {
			return nil, fmt.Errorf("scale error")
		}

		actual, err := b.BuildQRCode(ctx, "username", "two-factor-secret")
		test.EqOp(t, "", actual)
		test.Error(t, err)
	})

	T.Run("with png encode error", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		b := NewBuilder("test-issuer", nil, nil).(*builder)
		b.pngEncode = func(*bytes.Buffer, barcode.Barcode) error {
			return fmt.Errorf("png encode error")
		}

		actual, err := b.BuildQRCode(ctx, "username", "two-factor-secret")
		test.EqOp(t, "", actual)
		test.Error(t, err)
	})

	T.Run("escapes issuer, username, and secret containing reserved characters", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		b := NewBuilder("My & App", nil, nil).(*builder)

		var captured string
		b.qrEncode = func(content string, _ qr.ErrorCorrectionLevel, _ qr.Encoding) (barcode.Barcode, error) {
			captured = content
			return nil, fmt.Errorf("stop after capturing the otpauth URI")
		}

		_, err := b.BuildQRCode(ctx, "user name", "SECRET&123")
		test.Error(t, err)

		parsed, err := url.Parse(captured)
		must.NoError(t, err)
		test.EqOp(t, "otpauth", parsed.Scheme)
		test.EqOp(t, "totp", parsed.Host)
		test.EqOp(t, "My & App", parsed.Query().Get("issuer"))
		test.EqOp(t, "SECRET&123", parsed.Query().Get("secret"))
		// The label decodes back to the literal "issuer:username".
		test.StrContains(t, parsed.Path, "My & App:user name")
	})
}
