package cookies

import (
	"encoding/base64"
	"testing"

	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

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

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		must.NotNil(t, m)

		actual, err := m.Encode(ctx, "test", &example{Name: t.Name()})
		must.NoError(t, err)
		test.NotEq(t, "", actual)
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

		m, err := NewCookieManager(buildConfigForTest(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		must.NotNil(t, m)

		encoded, err := m.Encode(ctx, "test", &example{Name: t.Name()})
		must.NoError(t, err)
		test.NotEq(t, "", encoded)

		var actual example
		must.NoError(t, m.Decode(ctx, "test", encoded, &actual))
		test.EqOp(t, actual.Name, t.Name())
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
