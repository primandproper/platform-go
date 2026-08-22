package cookies

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	perrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"github.com/gorilla/securecookie"
)

type Manager interface {
	Encode(ctx context.Context, name string, value any) (string, error)
	Decode(ctx context.Context, name, value string, dst any) error
	BuildCookie(ctx context.Context, name string, value any) (*http.Cookie, error)
}

var _ Manager = (*SecureCookieManager)(nil)

// SecureCookieManager is the Manager backed by gorilla/securecookie. It is
// exported, and returned by NewCookieManager, so a caller can depend on the
// manager it built rather than on the Manager seam.
type SecureCookieManager struct {
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

// aesKeyLengths are the decoded block-key sizes AES accepts, selecting
// AES-128, AES-192, and AES-256 respectively.
var aesKeyLengths = []int{16, 24, 32}

// NewCookieManager returns a new Manager.
//
// The decoded block key is checked against the AES key sizes here rather than
// left to the codec. securecookie.New runs BlockFunc(aes.NewCipher) internally
// and stores the resulting error on the codec instead of returning it, so a
// wrong-sized key built a manager with a nil error and failed on the first
// Encode or Decode — at whichever request first needed a cookie, rather than at
// startup where the key was configured.
func NewCookieManager(cfg *Config, opts ...Option) (*SecureCookieManager, error) {
	if cfg == nil {
		return nil, perrors.ErrNilInputParameter
	}

	o := newOptions(opts)

	if err := cfg.ValidateWithContext(context.Background()); err != nil {
		return nil, fmt.Errorf("validating cookie config: %w", err)
	}

	decodedHashkey, err := base64.StdEncoding.DecodeString(cfg.Base64EncodedHashKey)
	if err != nil {
		return nil, fmt.Errorf("decoding HashKey: %w", err)
	}

	decodedBlockKey, err := base64.StdEncoding.DecodeString(cfg.Base64EncodedBlockKey)
	if err != nil {
		return nil, fmt.Errorf("decoding BlockKey: %w", err)
	}

	// The length, never the key: this error reaches logs.
	if !slices.Contains(aesKeyLengths, len(decodedBlockKey)) {
		return nil, perrors.Wrapf(perrors.ErrUnrecognizedInputValue,
			"cookie block key decodes to %d bytes, must be 16, 24, or 32", len(decodedBlockKey))
	}

	sc := securecookie.New(decodedHashkey, decodedBlockKey)
	if cfg.Lifetime > 0 {
		// Bound the MAC-protected timestamp to the configured lifetime so a
		// captured cookie cannot be replayed past it; otherwise securecookie
		// defaults to 30 days regardless of config.
		sc = sc.MaxAge(int(cfg.Lifetime.Seconds()))
	}

	return &SecureCookieManager{
		secureCookie: sc,
		domain:       cfg.Domain,
		lifetime:     cfg.Lifetime,
		sameSite:     sameSiteMode(cfg.SameSite),
		secureOnly:   cfg.SecureOnly,
		o11y:         observability.NewObserver("cookie_manager", o.logger, o.tracerProvider),
	}, nil
}

// Encode wraps securecookie's Encode method.
func (m *SecureCookieManager) Encode(ctx context.Context, name string, value any) (string, error) {
	_, op := m.o11y.Begin(ctx, observability.WithValue(keys.NameKey, name))
	defer op.End()

	encoded, err := m.secureCookie.Encode(name, value)
	if err != nil {
		return "", op.Error(err, "encoding cookie")
	}

	return encoded, nil
}

// BuildCookie encodes value and returns a ready-to-set *http.Cookie carrying
// the manager's configured security attributes: Secure from SecureOnly, Domain,
// SameSite, and MaxAge/Expires from Lifetime, plus a non-negotiable HttpOnly
// default. Callers hand the result to http.SetCookie.
func (m *SecureCookieManager) BuildCookie(ctx context.Context, name string, value any) (*http.Cookie, error) {
	_, op := m.o11y.Begin(ctx, observability.WithValue(keys.NameKey, name))
	defer op.End()

	encoded, err := m.secureCookie.Encode(name, value)
	if err != nil {
		return nil, op.Error(err, "encoding cookie")
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
func (m *SecureCookieManager) Decode(ctx context.Context, name, value string, dst any) error {
	_, op := m.o11y.Begin(ctx, observability.WithValue(keys.NameKey, name))
	defer op.End()

	if err := m.secureCookie.Decode(name, value, dst); err != nil {
		return op.Error(err, "decoding cookie")
	}

	return nil
}
