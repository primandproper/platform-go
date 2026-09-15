package links

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/encoding"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestDuration_String(T *testing.T) {
	T.Parallel()

	T.Run("renders the spelling a file uses", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "15m0s", Duration(15*time.Minute).String())
	})
}

func TestDuration_JSON(T *testing.T) {
	T.Parallel()

	T.Run("reads a written duration", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			text     string
			expected time.Duration
		}{
			{text: "15m", expected: 15 * time.Minute},
			{text: "1h30m", expected: 90 * time.Minute},
			{text: "8760h", expected: 365 * 24 * time.Hour},
			{text: "30s", expected: 30 * time.Second},
		} {
			var d Duration
			must.NoError(t, json.Unmarshal([]byte(`"`+tc.text+`"`), &d))
			test.EqOp(t, Duration(tc.expected), d)
		}
	})

	T.Run("writes the spelling it reads", func(t *testing.T) {
		t.Parallel()

		// The round trip is the property: a file this package wrote is a file
		// it documents, rather than one whose ttl is a nanosecond count only
		// this package can read back.
		encoded, err := json.Marshal(Duration(15 * time.Minute))
		must.NoError(t, err)
		test.EqOp(t, `"15m0s"`, string(encoded))

		var d Duration
		must.NoError(t, json.Unmarshal(encoded, &d))
		test.EqOp(t, Duration(15*time.Minute), d)
	})

	T.Run("refuses a bare number rather than reading nanoseconds", func(t *testing.T) {
		t.Parallel()

		// "15" from somebody who meant fifteen minutes would otherwise mint
		// links that are dead before they are delivered, and every check after
		// this one would pass.
		var d Duration
		test.ErrorIs(t, json.Unmarshal([]byte("15"), &d), ErrInvalidTTL)
		test.EqOp(t, Duration(0), d)
	})

	T.Run("refuses a string that is not a duration", func(t *testing.T) {
		t.Parallel()

		for _, text := range []string{`""`, `"fifteen minutes"`, `"15"`, `"15 m"`} {
			var d Duration
			test.ErrorIs(t, json.Unmarshal([]byte(text), &d), ErrInvalidTTL)
		}
	})

	T.Run("reads a policy out of a document", func(t *testing.T) {
		t.Parallel()

		var policy ActionPolicy
		must.NoError(t, json.Unmarshal(
			[]byte(`{"url":"https://app.example.com/auth/magic/{token}","ttl":"15m"}`), &policy))

		test.EqOp(t, Duration(15*time.Minute), policy.TTL)
		test.NoError(t, policy.validate("magic_login", false))
	})
}

func TestDuration_YAML(T *testing.T) {
	T.Parallel()

	// Decoded through the encoder a consumer reaching for YAML would use,
	// rather than against a library imported only here: the method this type
	// declares is honored by the decoder or it is honored by nothing.
	yamlEncoder := encoding.NewClientEncoder(encoding.ContentTypeYAML)

	T.Run("reads a written duration", func(t *testing.T) {
		t.Parallel()

		var policy ActionPolicy
		must.NoError(t, yamlEncoder.Unmarshal(t.Context(),
			[]byte("url: https://app.example.com/auth/magic/{token}\nttl: 15m\n"), &policy))

		test.EqOp(t, Duration(15*time.Minute), policy.TTL)
		test.EqOp(t, "https://app.example.com/auth/magic/{token}", policy.URL)
	})

	T.Run("writes the spelling it reads", func(t *testing.T) {
		t.Parallel()

		encoded, err := yamlEncoder.Marshal(t.Context(), ActionPolicy{
			URL: "https://app.example.com/auth/magic/{token}",
			TTL: Duration(15 * time.Minute),
		})
		must.NoError(t, err)

		var policy ActionPolicy
		must.NoError(t, yamlEncoder.Unmarshal(t.Context(), encoded, &policy))
		test.EqOp(t, Duration(15*time.Minute), policy.TTL)
	})

	T.Run("refuses a value that is not a duration", func(t *testing.T) {
		t.Parallel()

		for _, document := range []string{"ttl: 900000000000\n", "ttl: fifteen minutes\n", "ttl: [15m]\n"} {
			var policy ActionPolicy
			test.ErrorIs(t, yamlEncoder.Unmarshal(t.Context(), []byte(document), &policy), ErrInvalidTTL)
		}
	})
}
