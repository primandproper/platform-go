package cookies

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	testKey = "HEREISA32CHARSECRETWHICHISMADEUP"
)

func buildConfigForTest() *Config {
	return &Config{
		Base64EncodedHashKey:  base64.StdEncoding.EncodeToString([]byte(testKey)),
		Base64EncodedBlockKey: base64.StdEncoding.EncodeToString([]byte(testKey)),
	}
}

// newRecordingManager builds a manager with a RecordingObserver swapped in, so a
// test can both drive a method and assert the operation it observed.
func newRecordingManager(t *testing.T) (*manager, *observability.RecordingObserver) {
	t.Helper()

	m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
	must.NoError(t, err)
	must.NotNil(t, m)

	impl, ok := m.(*manager)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	impl.o11y = obs

	return impl, obs
}

func TestNewCookieManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		test.NoError(t, err)
		test.NotNil(t, m)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		m, err := NewCookieManager(nil, tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, m)
	})

	T.Run("with invalid hash key", func(t *testing.T) {
		t.Parallel()

		cfg := buildConfigForTest()
		cfg.Base64EncodedHashKey = "not-valid-base64!!!"

		m, err := NewCookieManager(cfg, tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, m)
	})

	T.Run("with invalid block key", func(t *testing.T) {
		t.Parallel()

		cfg := buildConfigForTest()
		cfg.Base64EncodedBlockKey = "not-valid-base64!!!"

		m, err := NewCookieManager(cfg, tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, m)
	})
}

type example struct {
	Name string
}

func Test_manager_Encode(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)

		actual, err := m.Encode(ctx, "test", &example{Name: t.Name()})
		must.NoError(t, err)
		test.NotEq(t, "", actual)

		// Encode opens (and ends) exactly one observed operation.
		must.SliceLen(t, 1, obs.Operations)
		test.True(t, obs.Operations[0].Ended)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.NameKey: "test",
		})
	})

	T.Run("with unencodable value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		must.NotNil(t, m)

		// Functions cannot be gob-encoded; securecookie.Encode will return an error.
		actual, err := m.Encode(ctx, "test", func() {})
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})
}

func Test_manager_Decode(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)

		encoded, err := m.Encode(ctx, "test", &example{Name: t.Name()})
		must.NoError(t, err)
		test.NotEq(t, "", encoded)

		var actual example
		must.NoError(t, m.Decode(ctx, "test", encoded, &actual))
		test.EqOp(t, actual.Name, t.Name())

		// Both Encode and Decode opened (and ended) an observed operation, each
		// attaching the cookie name.
		must.SliceLen(t, 2, obs.Operations)
		test.True(t, obs.Operations[0].Ended)
		test.True(t, obs.Operations[1].Ended)
		obs.Operations[0].Observed(t, observability.ObservedKeyValue(keys.NameKey, "test"))
		obs.Operations[1].Observed(t, observability.ObservedKeyValue(keys.NameKey, "test"))
	})

	T.Run("with invalid encoded value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		must.NotNil(t, m)

		var actual example
		test.Error(t, m.Decode(ctx, "test", "this-is-not-a-valid-cookie", &actual))
	})
}

func Test_manager_BuildCookie(T *testing.T) {
	T.Parallel()

	T.Run("applies configured security attributes", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildConfigForTest()
		cfg.Domain = "example.com"
		cfg.Lifetime = time.Hour
		cfg.SecureOnly = true

		m, err := NewCookieManager(cfg, tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		cookie, err := m.BuildCookie(ctx, "session", &example{Name: t.Name()})
		must.NoError(t, err)
		must.NotNil(t, cookie)

		test.EqOp(t, "session", cookie.Name)
		test.NotEq(t, "", cookie.Value)
		test.EqOp(t, "/", cookie.Path)
		test.EqOp(t, "example.com", cookie.Domain)
		test.True(t, cookie.HttpOnly)
		test.True(t, cookie.Secure)
		test.EqOp(t, http.SameSiteLaxMode, cookie.SameSite)
		test.EqOp(t, int(time.Hour.Seconds()), cookie.MaxAge)

		// The embedded value must still round-trip back through Decode.
		var got example
		must.NoError(t, m.Decode(ctx, "session", cookie.Value, &got))
		test.EqOp(t, t.Name(), got.Name)
	})

	T.Run("honors a configured SameSite policy", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildConfigForTest()
		cfg.SameSite = SameSiteStrict

		m, err := NewCookieManager(cfg, tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		cookie, err := m.BuildCookie(ctx, "session", &example{Name: t.Name()})
		must.NoError(t, err)
		must.NotNil(t, cookie)

		test.EqOp(t, http.SameSiteStrictMode, cookie.SameSite)
	})

	T.Run("without lifetime omits max age and stays non-secure by default", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		cookie, err := m.BuildCookie(ctx, "session", &example{Name: t.Name()})
		must.NoError(t, err)
		must.NotNil(t, cookie)

		test.EqOp(t, 0, cookie.MaxAge)
		test.True(t, cookie.Expires.IsZero())
		test.False(t, cookie.Secure)
		// HttpOnly and SameSite are non-negotiable defaults regardless of config.
		test.True(t, cookie.HttpOnly)
		test.EqOp(t, http.SameSiteLaxMode, cookie.SameSite)
	})

	T.Run("with unencodable value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		cookie, err := m.BuildCookie(ctx, "session", func() {})
		test.Error(t, err)
		test.Nil(t, cookie)
	})
}
