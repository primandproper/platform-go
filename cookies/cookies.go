package cookies

import (
	"context"
	"encoding/base64"
	"fmt"

	perrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/gorilla/securecookie"
)

type Manager interface {
	Encode(ctx context.Context, name string, value any) (string, error)
	Decode(ctx context.Context, name, value string, dst any) error
}

type manager struct {
	o11y         observability.Observer
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
		o11y:         observability.NewObserver("cookie_manager", nil, tracerProvider),
	}, nil
}

// Encode wraps securecookie's Encode method.
func (m *manager) Encode(ctx context.Context, name string, value any) (string, error) {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.NameKey, name)

	encoded, err := m.secureCookie.Encode(name, value)
	if err != nil {
		return "", observability.PrepareError(err, op.Span(), "encoding cookie")
	}

	return encoded, nil
}

// Decode wraps securecookie's Decode method.
func (m *manager) Decode(ctx context.Context, name, value string, dst any) error {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.NameKey, name)

	if err := m.secureCookie.Decode(name, value, dst); err != nil {
		return observability.PrepareError(err, op.Span(), "decoding cookie")
	}

	return nil
}
