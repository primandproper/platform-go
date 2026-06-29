package cookies

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	perrors "github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/gorilla/securecookie"
)

type Manager interface {
	Encode(ctx context.Context, name string, value any) (string, error)
	Decode(ctx context.Context, name, value string, dst any) error
	BuildCookie(ctx context.Context, name string, value any) (*http.Cookie, error)
}

type manager struct {
	o11y         observability.Observer
	secureCookie *securecookie.SecureCookie
	domain       string
	lifetime     time.Duration
	sameSite     http.SameSite
	secureOnly   bool
}

// sameSiteMode maps a Config.SameSite string to its http.SameSite mode,
// defaulting to Lax for the empty (or any unexpected) value. Validation in
// Config rejects unsupported values before they reach here.
func sameSiteMode(s string) http.SameSite {
	switch strings.ToLower(s) {
	case SameSiteStrict:
		return http.SameSiteStrictMode
	case SameSiteNone:
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
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

	sc := securecookie.New(decodedHashkey, decodedBlockKey)
	if cfg.Lifetime > 0 {
		// Bound the MAC-protected timestamp to the configured lifetime so a
		// captured cookie cannot be replayed past it; otherwise securecookie
		// defaults to 30 days regardless of config.
		sc = sc.MaxAge(int(cfg.Lifetime.Seconds()))
	}

	return &manager{
		secureCookie: sc,
		domain:       cfg.Domain,
		lifetime:     cfg.Lifetime,
		sameSite:     sameSiteMode(cfg.SameSite),
		secureOnly:   cfg.SecureOnly,
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

// BuildCookie encodes value and returns a ready-to-set *http.Cookie carrying
// the manager's configured security attributes: Secure from SecureOnly, Domain,
// SameSite, and MaxAge/Expires from Lifetime, plus a non-negotiable HttpOnly
// default. Callers hand the result to http.SetCookie.
func (m *manager) BuildCookie(ctx context.Context, name string, value any) (*http.Cookie, error) {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.NameKey, name)

	encoded, err := m.secureCookie.Encode(name, value)
	if err != nil {
		return nil, observability.PrepareError(err, op.Span(), "encoding cookie")
	}

	cookie := &http.Cookie{
		Name:     name,
		Value:    encoded,
		Path:     "/",
		Domain:   m.domain,
		HttpOnly: true,
		Secure:   m.secureOnly,
		SameSite: m.sameSite,
	}

	if m.lifetime > 0 {
		cookie.MaxAge = int(m.lifetime.Seconds())
		cookie.Expires = time.Now().Add(m.lifetime)
	}

	return cookie, nil
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
