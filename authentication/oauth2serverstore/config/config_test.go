package oauth2serverstorecfg

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore"

	"github.com/primandproper/primitives-go/authentication/oauth2server"
	oauth2servercfg "github.com/primandproper/primitives-go/authentication/oauth2server/config"
	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testAuthenticator stands in for whatever a deployment uses to identify a
// human. What it does is irrelevant here; that it must be supplied is the point.
var testAuthenticator = oauth2server.SubjectAuthenticatorFunc(
	func(context.Context, *http.Request) (*oauth2server.Subject, error) {
		return &oauth2server.Subject{ID: "user_1"}, nil
	})

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in every zero field", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderDatabase, cfg.Provider)
		test.EqOp(t, oauth2server.DefaultAuthorizationCodeTTL, cfg.AuthorizationCodeTTL)
		test.EqOp(t, oauth2server.DefaultAccessTokenTTL, cfg.AccessTokenTTL)
		test.EqOp(t, oauth2server.DefaultRefreshTokenTTL, cfg.RefreshTokenTTL)
		test.EqOp(t, oauth2server.DefaultClientRegistrationTTL, pointer.Dereference(cfg.ClientRegistrationTTL))
		test.EqOp(t, oauth2server.DefaultSweepInterval, pointer.Dereference(cfg.SweepInterval))
	})

	T.Run("defaults to the durable provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		// The memory provider fails logins behind a load balancer, so it is not
		// something an unset environment should produce.
		test.EqOp(t, ProviderDatabase, cfg.Provider)
	})

	T.Run("leaves what was set alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, AccessTokenTTL: time.Minute}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderMemory, cfg.Provider)
		test.EqOp(t, time.Minute, cfg.AccessTokenTTL)
	})

	// Nil is the default and a spelled zero is the off-switch, which is only
	// true while this method can tell them apart. Defaulting the zero would put
	// the off-switch back out of reach.
	T.Run("leaves a spelled zero alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{SweepInterval: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Duration(0), pointer.Dereference(cfg.SweepInterval))
	})

	// The same defect two fields up. WithClientRegistrationTTL has always read
	// zero as "never lapse"; what could not reach it was a configured zero,
	// because this method mapped it to the ninety-day default first.
	T.Run("leaves a spelled zero registration lifetime alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ClientRegistrationTTL: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		must.NotNil(t, cfg.ClientRegistrationTTL)
		test.EqOp(t, time.Duration(0), *cfg.ClientRegistrationTTL)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Issuer: "https://auth.example"}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a provider nobody implements", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "redis"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("accepts the off-switch by rule rather than by omission", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Issuer: "https://auth.example", SweepInterval: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// Below zero there is no cadence to configure — every negative duration
	// reaches the store as "start nothing" — so a magnitude is somebody
	// describing a sweep they will not get.
	T.Run("refuses a negative interval", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Issuer: "https://auth.example", SweepInterval: pointer.To(-30 * time.Minute)}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("skips the database rules under the memory provider", func(t *testing.T) {
		t.Parallel()

		// `env:",init"` leaves the sub-config populated whichever provider is
		// selected, and ozzo validates any non-nil Validatable once a field's
		// rules run — so without the Skip a perfectly good memory configuration
		// would be unloadable because of a table prefix nothing will use.
		cfg := &Config{Provider: ProviderMemory, Database: databaseConfigWithBadPrefix()}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))

		cfg.Provider = ProviderDatabase
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a negative lifetime", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, AccessTokenTTL: -time.Minute}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// Nil is the unset field and zero is "never lapse"; both are answers the
	// rule has to permit, and it is applied without EnsureDefaults having run
	// so that the nil is really a nil when it reaches ozzo.
	T.Run("accepts an unset and a zero registration lifetime", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))

		cfg.ClientRegistrationTTL = pointer.To(time.Duration(0))
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// The option would fold a negative into zero, and a deployment that wrote
	// one was not asking for registrations that never lapse. The pointer does
	// not loosen this: Min reads through it.
	T.Run("refuses a negative registration lifetime", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, ClientRegistrationTTL: pointer.To(-time.Hour)}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds the memory provider", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{Provider: ProviderMemory}, nil)
		must.NoError(t, err)
		must.NotNil(t, store)
		test.NoError(t, store.Close())
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("an unknown provider is an error rather than a working-looking default", func(t *testing.T) {
		t.Parallel()

		// Falling back to memory would produce an authorization server that
		// signs users in, fails their next login on another replica, and looks
		// configured the whole time.
		store, err := NewStore(t.Context(), &Config{Provider: "redis"}, nil)
		test.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("the database provider needs a client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{Provider: ProviderDatabase}, nil)
		test.Error(t, err)

		// The nil check is the point. A concrete-typed constructor's result
		// returned straight through would arrive here as a non-nil interface
		// wrapping a nil pointer — a value that passes this check and panics on
		// first use.
		test.Nil(t, store)
	})
}

// TestConfig_SweepInterval exercises the field's documented contract where it
// is actually decided. The stores' own WithSweeper has always honored a
// non-positive interval; what could not reach it was a configured one, because
// EnsureDefaults mapped every zero to the default before the option was built.
//
// The observable is Store.Sweep's own count. A record the background sweeper
// already removed leaves the scheduled sweep nothing to do, which is exactly
// the duplication a deployment turns the sweeper off to avoid.
func TestConfig_SweepInterval(T *testing.T) {
	T.Parallel()

	// The wall clock is deliberate: inside a synctest bubble clock.NewClock
	// reads the bubble's time, so the store's ticker advances with time.Sleep
	// and needs no test double.
	T.Run("a zero interval leaves dead records for somebody else to remove", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			store, err := NewStore(t.Context(),
				&Config{Provider: ProviderMemory, SweepInterval: pointer.To(time.Duration(0))}, nil)
			must.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			must.NoError(t, store.CreateAuthorizationCode(t.Context(), expiringCode()))

			// Past the code's deadline and past every tick a defaulted interval
			// would have taken.
			time.Sleep(time.Hour)
			synctest.Wait()

			// The scheduled sweep this deployment runs instead still has work,
			// which is only true because nothing swept behind its back.
			swept, err := store.Sweep(t.Context(), time.Now())
			must.NoError(t, err)
			test.EqOp(t, int64(1), swept)
		})
	})

	T.Run("an unset interval sweeps, because a table nobody sweeps only grows", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			store, err := NewStore(t.Context(), &Config{Provider: ProviderMemory}, nil)
			must.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			must.NoError(t, store.CreateAuthorizationCode(t.Context(), expiringCode()))

			time.Sleep(time.Hour)
			synctest.Wait()

			swept, err := store.Sweep(t.Context(), time.Now())
			must.NoError(t, err)
			test.EqOp(t, int64(0), swept)
		})
	})
}

// expiringCode is one authorization code, redeemable for a minute — long
// enough to be alive when it is written and dead long before either test looks
// again.
func expiringCode() *oauth2server.AuthorizationCode {
	now := time.Now().UTC()

	return &oauth2server.AuthorizationCode{
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Minute),
		Hash:      oauth2server.Hash("swept"),
		ClientID:  "client",
		Subject:   oauth2server.Subject{ID: "user"},
	}
}

// TestConfig_ClientRegistrationTTL exercises the field's documented contract
// where it is actually decided. WithClientRegistrationTTL has always read a
// zero as registrations that never lapse; what could not reach it was a
// configured zero, because EnsureDefaults mapped every zero to the ninety-day
// default before the option was built.
//
// The observable is the registration response. RFC 7591's
// client_secret_expires_at is rendered from the stored client's ExpiresAt —
// zero when it is unset, the epoch seconds of the deadline otherwise — and
// client_id_issued_at from its CreatedAt, so the two together are the client
// record as the protocol shows it. The store a config-built server writes to
// is its own, which is why the test reads the response rather than the row.
func TestConfig_ClientRegistrationTTL(T *testing.T) {
	T.Parallel()

	T.Run("a zero lifetime registers a client that never lapses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example", ClientRegistrationTTL: pointer.To(time.Duration(0))}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)

		reg := registerClient(t, srv)

		// The client holds a secret, so a zero here is the registration's own
		// expiry being unset and not a public client with nothing to expire.
		must.NotEq(t, "", reg.ClientSecret)
		test.EqOp(t, int64(0), reg.ClientSecretExpiresAt)
	})

	T.Run("an unset lifetime registers a client that lapses after the default", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example"}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)

		reg := registerClient(t, srv)

		// Both stamps come from the same clock read, so the deadline is the
		// issue time plus the default to the second.
		must.NotEq(t, "", reg.ClientSecret)
		test.EqOp(t, reg.ClientIDIssuedAt+int64(oauth2server.DefaultClientRegistrationTTL/time.Second),
			reg.ClientSecretExpiresAt)
	})
}

// registerClient registers one confidential client through a server's own
// handler and hands back what the server said about it.
func registerClient(t *testing.T, srv *oauth2server.Server) *oauth2server.RegistrationResponse {
	t.Helper()

	body, err := json.Marshal(map[string]any{"redirect_uris": []string{"https://client.example/callback"}})
	must.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, oauth2server.PathRegister, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	must.EqOp(t, http.StatusCreated, res.Code)

	out := &oauth2server.RegistrationResponse{}
	must.NoError(t, json.Unmarshal(res.Body.Bytes(), out))

	return out
}

func TestNewServer_StoreFailure(T *testing.T) {
	T.Parallel()

	T.Run("a store that cannot be built is reported rather than skipped", func(t *testing.T) {
		t.Parallel()

		// NewServer builds the store first, and a server over a store that
		// failed to build would be an authorization server with nowhere to keep
		// an authorization code.
		srv, err := NewServer(t.Context(),
			&Config{Provider: ProviderDatabase, Issuer: "https://auth.example"},
			nil, testAuthenticator)

		test.Error(t, err)
		test.Nil(t, srv)
	})
}

func TestNewServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example", Scopes: []string{"read"}}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)
		must.NotNil(t, srv)

		test.EqOp(t, "https://auth.example", srv.Issuer())
		test.Eq(t, []string{"read"}, srv.Metadata().ScopesSupported)
	})

	T.Run("carries the configured lifetimes through", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example", AccessTokenTTL: 42 * time.Second}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		srv, err := NewServer(t.Context(), nil, nil, testAuthenticator)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, srv)
	})

	T.Run("an issuer that is not one fails at construction", func(t *testing.T) {
		t.Parallel()

		// Not re-validated by this Config, deliberately: the rule lives in
		// oauth2server.NewServer, and a second copy would be a second place for
		// it to be wrong.
		srv, err := NewServer(t.Context(), &Config{Provider: ProviderMemory}, nil, testAuthenticator)
		test.ErrorIs(t, err, oauth2server.ErrEmptyIssuer)
		test.Nil(t, srv)
	})

	T.Run("carries the registration switch through", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example", DisableDynamicRegistration: true}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)

		// The discovery document is where the switch is visible, and the
		// endpoint being absent from it is the point: a deployment whose
		// clients are administered elsewhere publishes no /register.
		test.EqOp(t, "", srv.Metadata().RegistrationEndpoint)
	})

	T.Run("serves registration when nothing turned it off", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example"}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator)
		must.NoError(t, err)

		// Spelled as a disable so that an unset environment is the protocol's
		// own behavior rather than a server a client cannot register with.
		test.EqOp(t, "https://auth.example"+oauth2server.PathRegister, srv.Metadata().RegistrationEndpoint)
	})

	T.Run("options passed through win over the config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, Issuer: "https://auth.example", Scopes: []string{"read"}}

		srv, err := NewServer(t.Context(), cfg, nil, testAuthenticator,
			WithServerOptions(oauth2servercfg.WithServerOptions(oauth2server.WithServiceDocumentation("https://docs.example"))))
		must.NoError(t, err)

		test.EqOp(t, "https://docs.example", srv.Metadata().ServiceDocumentation)
	})
}

// databaseConfigWithBadPrefix is a database sub-config that would fail its own
// validation, for the case that proves the memory provider does not run it.
func databaseConfigWithBadPrefix() oauth2serverstore.Config {
	return oauth2serverstore.Config{TablePrefix: "trailing_"}
}
