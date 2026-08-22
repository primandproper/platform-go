package requestsigning

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/secrets"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fakeSecretSource answers from a map, and reports anything absent the way a
// real provider does.
type fakeSecretSource struct {
	values map[string]string
	err    error
	// failFor narrows err to one name, so a test can fail the previous-key read
	// while the current one succeeds. Empty means err applies to every read.
	failFor string
	reads   int
}

var _ secrets.SecretSource = (*fakeSecretSource)(nil)

func (f *fakeSecretSource) GetSecret(_ context.Context, name string) (string, error) {
	f.reads++

	if f.err != nil && (f.failFor == "" || f.failFor == name) {
		return "", f.err
	}

	value, ok := f.values[name]
	if !ok {
		return "", secrets.ErrSecretNotFound
	}

	return value, nil
}

func (*fakeSecretSource) Close() error { return nil }

func TestStaticKeyring(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		keyring, err := StaticKeyring(Keyring{Current: []byte("k")}).Keyring(t.Context())
		must.NoError(t, err)
		test.Eq(t, []byte("k"), keyring.Current)
	})
}

func TestNewSecretKeySource(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source := &fakeSecretSource{values: map[string]string{"CURRENT": "the key"}}

		keys, err := NewSecretKeySource(source, "CURRENT", "")
		must.NoError(t, err)

		keyring, err := keys.Keyring(t.Context())
		must.NoError(t, err)

		test.Eq(t, []byte("the key"), keyring.Current)
		test.SliceEmpty(t, keyring.Previous)
	})

	T.Run("reads both sides of a rotation", func(t *testing.T) {
		t.Parallel()

		source := &fakeSecretSource{values: map[string]string{"CURRENT": "new", "PREVIOUS": "old"}}

		keys, err := NewSecretKeySource(source, "CURRENT", "PREVIOUS")
		must.NoError(t, err)

		keyring, err := keys.Keyring(t.Context())
		must.NoError(t, err)

		test.Eq(t, []byte("new"), keyring.Current)
		test.Eq(t, []byte("old"), keyring.Previous)
	})

	// The steady state. No rotation is in progress, so the previous name is not
	// in the store, and that is not a failure to resolve the keyring.
	T.Run("an absent previous key closes the rotation window", func(t *testing.T) {
		t.Parallel()

		source := &fakeSecretSource{values: map[string]string{"CURRENT": "new"}}

		keys, err := NewSecretKeySource(source, "CURRENT", "PREVIOUS")
		must.NoError(t, err)

		keyring, err := keys.Keyring(t.Context())
		must.NoError(t, err)

		test.Eq(t, []byte("new"), keyring.Current)
		test.SliceEmpty(t, keyring.Previous)
	})

	// Re-read per call is the whole point: a rotation in the store reaches the
	// wire without a restart.
	T.Run("re-reads on every call", func(t *testing.T) {
		t.Parallel()

		source := &fakeSecretSource{values: map[string]string{"CURRENT": "first"}}

		keys, err := NewSecretKeySource(source, "CURRENT", "")
		must.NoError(t, err)

		_, err = keys.Keyring(t.Context())
		must.NoError(t, err)

		source.values["CURRENT"] = "second"

		keyring, err := keys.Keyring(t.Context())
		must.NoError(t, err)

		test.Eq(t, []byte("second"), keyring.Current)
		test.EqOp(t, 2, source.reads)
	})

	T.Run("a missing current key is an error", func(t *testing.T) {
		t.Parallel()

		keys, err := NewSecretKeySource(&fakeSecretSource{values: map[string]string{}}, "CURRENT", "")
		must.NoError(t, err)

		_, err = keys.Keyring(t.Context())
		test.ErrorIs(t, err, secrets.ErrSecretNotFound)
	})

	// An empty value is not a key. Accepting it would produce an HMAC under the
	// empty key, which verifies for anyone who guesses that is what happened.
	T.Run("an empty current key is an error", func(t *testing.T) {
		t.Parallel()

		keys, err := NewSecretKeySource(&fakeSecretSource{values: map[string]string{"CURRENT": ""}}, "CURRENT", "")
		must.NoError(t, err)

		_, err = keys.Keyring(t.Context())
		test.ErrorIs(t, err, ErrNoSigningKey)
	})

	// A store that cannot be reached is not a closed rotation window: answering
	// with a half-resolved keyring would silently narrow what verifies.
	T.Run("a failing store fails the resolution", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the store is down")

		keys, err := NewSecretKeySource(&fakeSecretSource{err: boom}, "CURRENT", "PREVIOUS")
		must.NoError(t, err)

		_, err = keys.Keyring(t.Context())
		test.ErrorIs(t, err, boom)
	})

	T.Run("a previous key the store could not answer for fails the resolution", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the store is down")

		keys, err := NewSecretKeySource(&fakeSecretSource{
			values:  map[string]string{"CURRENT": "new"},
			err:     boom,
			failFor: "PREVIOUS",
		}, "CURRENT", "PREVIOUS")
		must.NoError(t, err)

		_, err = keys.Keyring(t.Context())
		test.ErrorIs(t, err, boom)
	})

	T.Run("rejects its own bad inputs", func(t *testing.T) {
		t.Parallel()

		_, err := NewSecretKeySource(nil, "CURRENT", "")
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)

		_, err = NewSecretKeySource(&fakeSecretSource{}, "", "")
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}
