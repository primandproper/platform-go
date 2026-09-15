package links

import (
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		opts := []Option{nil, WithAction(testAction, testPolicy()), nil}

		m, err := NewMinter(newMemoryStore(), opts...)
		must.NoError(t, err)
		test.NotNil(t, m)
	})

	T.Run("WithAction ignores an empty action", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(newMemoryStore(), WithAction("", testPolicy()))
		test.ErrorIs(t, err, ErrNoActions)
	})

	T.Run("WithAction replaces an earlier policy for the same action", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t, WithAction(testAction, ActionPolicy{
			URL: "https://other.example.com/{token}",
			TTL: Duration(time.Hour),
		}))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)
		test.StrHasPrefix(t, "https://other.example.com/", link.URL)
	})

	T.Run("WithActions registers a whole map", func(t *testing.T) {
		t.Parallel()

		m, err := NewMinter(newMemoryStore(), WithActions(map[Action]ActionPolicy{
			testAction:     testPolicy(),
			"verify_email": {URL: "https://app.example.com/verify/{token}", TTL: Duration(time.Hour)},
			"":             testPolicy(),
		}))
		must.NoError(t, err)

		test.SliceLen(t, 2, m.Actions())
	})

	T.Run("WithActions leaves a nil map alone", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(newMemoryStore(), WithActions(nil))
		test.ErrorIs(t, err, ErrNoActions)
	})

	T.Run("the registry is copied, so a caller's map cannot mutate it later", func(t *testing.T) {
		t.Parallel()

		actions := map[Action]ActionPolicy{testAction: testPolicy()}

		m, err := NewMinter(newMemoryStore(), WithActions(actions))
		must.NoError(t, err)

		actions["injected"] = ActionPolicy{URL: "http://evil.example.com/{token}", TTL: Duration(time.Hour)}

		_, err = m.Mint(t.Context(), "injected", testSubject)
		test.ErrorIs(t, err, ErrUnknownAction)
	})

	T.Run("WithTokenBytes changes the token length", func(t *testing.T) {
		t.Parallel()

		short := newTestMinter(t, WithTokenBytes(16))
		long := newTestMinter(t, WithTokenBytes(64))

		shortLink, err := short.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		longLink, err := long.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		test.Less(t, len(longLink.Token), len(shortLink.Token))
	})

	T.Run("non-positive values leave the defaults in place", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t,
			WithTokenBytes(0),
			WithMaxTokenLength(-1),
			WithRetention(0),
			WithClock(nil),
			WithHasher(nil),
			WithGenerator(nil),
		)

		test.EqOp(t, DefaultTokenBytes, m.tokenBytes)
		test.EqOp(t, DefaultMaxTokenLength, m.maxTokenLength)
		test.EqOp(t, DefaultRetention, m.retention)
		test.NotNil(t, m.clock)
		test.NotNil(t, m.hasher)
		test.NotNil(t, m.generator)
	})

	T.Run("WithInsecureURLs admits a cleartext action URL", func(t *testing.T) {
		t.Parallel()

		policy := ActionPolicy{URL: "http://staging.example.com/auth/{token}", TTL: Duration(time.Hour)}

		_, err := NewMinter(newMemoryStore(), WithAction(testAction, policy))
		test.ErrorIs(t, err, ErrInsecureActionURL)

		_, err = NewMinter(newMemoryStore(), WithAction(testAction, policy), WithInsecureURLs())
		test.NoError(t, err)
	})
}

func TestMintOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithTTL ignores a non-positive value", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject, WithTTL(0))
		must.NoError(t, err)

		test.EqOp(t, c.Clock().Now().UTC().Add(testActionTTL), link.ExpiresAt)
	})

	T.Run("WithMetadata copies the caller's map", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		metadata := map[string]string{"next": "/dashboard"}

		link, err := m.Mint(t.Context(), testAction, testSubject, WithMetadata(metadata))
		must.NoError(t, err)

		metadata["next"] = "/evil"

		claims, err := m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)
		test.EqOp(t, "/dashboard", claims.Metadata["next"])
	})

	T.Run("WithMetadata accumulates across calls", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject,
			WithMetadata(map[string]string{"next": "/dashboard"}),
			WithMetadata(map[string]string{"campaign": "june"}),
		)
		must.NoError(t, err)

		claims, err := m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		test.EqOp(t, "/dashboard", claims.Metadata["next"])
		test.EqOp(t, "june", claims.Metadata["campaign"])
	})

	T.Run("a link with no metadata redeems with none", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		claims, err := m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)
		test.MapLen(t, 0, claims.Metadata)
	})
}
