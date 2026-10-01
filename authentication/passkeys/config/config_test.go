package passkeyscfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

// nowhere is a session store no ceremony here reaches.
type nowhere struct{}

func (nowhere) Save(context.Context, *webauthn.SessionData, time.Duration) error { return nil }

func (nowhere) Consume(context.Context, string) (*webauthn.SessionData, error) {
	return nil, webauthn.ErrSessionNotFound
}

func testRelyingParty(t *testing.T) *webauthn.RelyingParty {
	t.Helper()

	rp, err := webauthn.NewRelyingParty(t.Context(), &webauthn.Config{
		RPID:          "example.com",
		RPDisplayName: "Example",
		RPOrigins:     []string{"https://example.com"},
	}, nowhere{})
	must.NoError(t, err)

	return rp
}

func resolveNobody(context.Context, []byte) (passkeys.UserIdentity, error) {
	return passkeys.UserIdentity{}, platformerrors.New("nobody")
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("the zero value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("a prefix that renders no identifier", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "app"}, testDBClient(t))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, testDBClient(t))
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("a bad config is a nil store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"}, testDBClient(t))
		test.Error(t, err)
		test.Nil(t, store)
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		client := testDBClient(t)
		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store, testRelyingParty(t),
			resolveNobody, passkeys.AdmitEveryEnrollment)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("no enrollment gate", func(t *testing.T) {
		t.Parallel()

		client := testDBClient(t)
		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store, testRelyingParty(t), resolveNobody, nil)
		test.ErrorIs(t, err, passkeys.ErrNoEnrollmentGate)
		test.Nil(t, svc)
	})

	T.Run("no user resolver", func(t *testing.T) {
		t.Parallel()

		client := testDBClient(t)
		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store, testRelyingParty(t), nil, passkeys.AdmitEveryEnrollment)
		test.ErrorIs(t, err, passkeys.ErrNilResolver)
		test.Nil(t, svc)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), nil, testDBClient(t), nil, nil, nil, nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, svc)
	})
}
