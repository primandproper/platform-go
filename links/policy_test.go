package links

import (
	"testing"
	"time"

	"github.com/shoenig/test"
)

func TestActionPolicy_validate(T *testing.T) {
	T.Parallel()

	const validURL = "https://app.example.com/auth/magic/{token}"

	T.Run("accepts an https template", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ActionPolicy{URL: validURL, TTL: Duration(time.Minute)}.validate(testAction, false))
	})

	T.Run("accepts the token in a query value", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "https://app.example.com/unsubscribe?t={token}", TTL: Duration(time.Minute)}
		test.NoError(t, policy.validate(testAction, false))
	})

	T.Run("rejects a non-positive TTL", func(t *testing.T) {
		t.Parallel()

		for _, ttl := range []time.Duration{0, -time.Second} {
			test.ErrorIs(t, ActionPolicy{URL: validURL, TTL: Duration(ttl)}.validate(testAction, false), ErrInvalidTTL)
		}
	})

	T.Run("rejects a template with no placeholder", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "https://app.example.com/auth/magic", TTL: Duration(time.Minute)}
		test.ErrorIs(t, policy.validate(testAction, false), ErrInvalidActionURL)
	})

	T.Run("rejects a template with two placeholders", func(t *testing.T) {
		t.Parallel()

		// Only the first would be replaced, so the second would ship the literal
		// "{token}" to the user in a URL that looks nearly right.
		policy := ActionPolicy{URL: "https://app.example.com/{token}/x/{token}", TTL: Duration(time.Minute)}
		test.ErrorIs(t, policy.validate(testAction, false), ErrInvalidActionURL)
	})

	T.Run("rejects a relative template", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "/auth/magic/{token}", TTL: Duration(time.Minute)}
		test.ErrorIs(t, policy.validate(testAction, false), ErrInvalidActionURL)
	})

	T.Run("rejects an unparseable template", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "https://app.example.com/\x7f/{token}", TTL: Duration(time.Minute)}
		test.ErrorIs(t, policy.validate(testAction, false), ErrInvalidActionURL)
	})

	T.Run("rejects cleartext against a routable host", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "http://app.example.com/auth/magic/{token}", TTL: Duration(time.Minute)}
		err := policy.validate(testAction, false)

		test.ErrorIs(t, err, ErrInsecureActionURL)
		test.ErrorIs(t, err, ErrInvalidActionURL)
	})

	T.Run("allows cleartext against a loopback host", func(t *testing.T) {
		t.Parallel()

		for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "LOCALHOST"} {
			policy := ActionPolicy{URL: "http://" + host + "/auth/magic/{token}", TTL: Duration(time.Minute)}
			test.NoError(t, policy.validate(testAction, false), test.Sprintf("host %q", host))
		}
	})

	T.Run("allows cleartext anywhere when the escape hatch is set", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "http://app.example.com/auth/magic/{token}", TTL: Duration(time.Minute)}
		test.NoError(t, policy.validate(testAction, true))
	})

	T.Run("rejects a scheme that is neither, escape hatch or not", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "ftp://app.example.com/auth/magic/{token}", TTL: Duration(time.Minute)}
		test.ErrorIs(t, policy.validate(testAction, false), ErrInsecureActionURL)
	})
}

func TestActionPolicy_expand(T *testing.T) {
	T.Parallel()

	T.Run("substitutes the token", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "https://app.example.com/auth/magic/{token}"}
		test.EqOp(t, "https://app.example.com/auth/magic/abc", policy.expand("abc"))
	})
}

func TestIsLoopback(T *testing.T) {
	T.Parallel()

	T.Run("recognizes loopback hosts", func(t *testing.T) {
		t.Parallel()

		for _, host := range []string{"localhost", "LocalHost", "127.0.0.1", "127.9.9.9", "::1"} {
			test.True(t, isLoopback(host), test.Sprintf("host %q", host))
		}
	})

	T.Run("rejects everything else", func(t *testing.T) {
		t.Parallel()

		for _, host := range []string{"app.example.com", "10.0.0.1", "", "localhost.evil.com"} {
			test.False(t, isLoopback(host), test.Sprintf("host %q", host))
		}
	})
}
