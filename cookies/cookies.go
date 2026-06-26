package cookies

import (
	"context"
	"encoding/base64"
	"fmt"

	perrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/gorilla/securecookie"
)

type Manager interface {
	Encode(ctx context.Context, name string, value any) (string, error)
	Decode(ctx context.Context, name, value string, dst any) error
}

type manager struct {
	tracer       tracing.Tracer
	secureCookie *securecookie.SecureCookie
}

// NewCookieManager returns a new Manager.
func NewCookieManager(cfg *Config, tracerProvider tracing.TracerProvider) (Manager, error) {
	if cfg == nil {
		return nil, perrors.ErrNilInputProvided
	}

	decodedHashkey, err := base64.StdEncoding.DecodeString(cfg.Base64EncodedHashKey)
	if err != nil {
		return nil, fmt.Errorf("decoding HashKey %q: %w", cfg.Base64EncodedHashKey, err)
	}

	decodedBlockKey, err := base64.StdEncoding.DecodeString(cfg.Base64EncodedBlockKey)
	if err != nil {
		return nil, fmt.Errorf("decoding BlockKey %q: %w", cfg.Base64EncodedBlockKey, err)
	}

	return &manager{
		secureCookie: securecookie.New(decodedHashkey, decodedBlockKey),
		tracer:       tracing.NewNamedTracer(tracerProvider, "cookie_manager"),
	}, nil
}

// Encode wraps securecookie's Encode method.
func (m *manager) Encode(ctx context.Context, name string, value any) (string, error) {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	encoded, err := m.secureCookie.Encode(name, value)
	if err != nil {
		return "", observability.PrepareError(err, span, "encoding cookie")
	}

	return encoded, nil
}

// Decode wraps securecookie's Decode method.
func (m *manager) Decode(ctx context.Context, name, value string, dst any) error {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	if err := m.secureCookie.Decode(name, value, dst); err != nil {
		return observability.PrepareError(err, span, "decoding cookie")
	}

	return nil
}
