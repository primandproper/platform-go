package qrcodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/url"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
)

const (
	o11yName          = "qr_code_builder"
	base64ImagePrefix = "data:image/png;base64,"
)

type (
	// Builder generates QR codes for TOTP two-factor authentication.
	Builder interface {
		BuildQRCode(ctx context.Context, username, twoFactorSecret string) (string, error)
	}

	// Issuer identifies the service that issued the TOTP secret.
	Issuer string

	builder struct {
		o11y       observability.Observer
		qrEncode   func(content string, level qr.ErrorCorrectionLevel, mode qr.Encoding) (barcode.Barcode, error)
		scale      func(bc barcode.Barcode, width, height int) (barcode.Barcode, error)
		pngEncode  func(b *bytes.Buffer, img barcode.Barcode) error
		totpIssuer Issuer
	}
)

// NewBuilder returns a new QR code Builder.
func NewBuilder(issuer Issuer, tracerProvider tracing.TracerProvider, logger logging.Logger) Builder {
	return &builder{
		o11y:       observability.NewObserver(o11yName, logger, tracerProvider),
		totpIssuer: issuer,
		qrEncode:   qr.Encode,
		scale:      barcode.Scale,
		pngEncode: func(b *bytes.Buffer, img barcode.Barcode) error {
			return png.Encode(b, img)
		},
	}
}

// BuildQRCode builds a QR code for a given username and secret.
func (s *builder) BuildQRCode(ctx context.Context, username, twoFactorSecret string) (string, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	// otpauth://totp/{{ .Issuer }}:{{ .Username }}?secret={{ .Secret }}&issuer={{ .Issuer }}
	// The issuer, username, and secret are escaped so values containing spaces or reserved
	// characters (&, #, ?, ...) produce a valid, correctly-parsed URI.
	query := url.Values{}
	query.Set("secret", twoFactorSecret)
	query.Set("issuer", string(s.totpIssuer))

	otpString := fmt.Sprintf(
		"otpauth://totp/%s?%s",
		url.PathEscape(fmt.Sprintf("%s:%s", s.totpIssuer, username)),
		query.Encode(),
	)

	op.Set(keys.UsernameKey, username).Set(keys.LengthKey, len(otpString))

	// encode two factor secret as authenticator-friendly QR code
	qrCode, err := s.qrEncode(otpString, qr.L, qr.Auto)
	if err != nil {
		return "", observability.PrepareError(err, op.Span(), "encoding OTP string")
	}

	// scale the QR code so that it's not a PNG for ants.
	qrCode, err = s.scale(qrCode, 256, 256)
	if err != nil {
		return "", observability.PrepareError(err, op.Span(), "scaling QR code")
	}

	// encode the QR code to PNG.
	var b bytes.Buffer
	if err = s.pngEncode(&b, qrCode); err != nil {
		return "", observability.PrepareError(err, op.Span(), "encoding QR code to PNG")
	}

	// base64 encode the image for easy HTML use.
	return fmt.Sprintf("%s%s", base64ImagePrefix, base64.StdEncoding.EncodeToString(b.Bytes())), nil
}
