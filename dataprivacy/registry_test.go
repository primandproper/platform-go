package dataprivacy

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegistry(T *testing.T) {
	T.Parallel()

	T.Run("keys come back sorted", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		must.NoError(t, registry.RegisterCollector("webhooks", staticCollector(`{}`)))
		must.NoError(t, registry.RegisterCollector("identity", staticCollector(`{}`)))
		must.NoError(t, registry.RegisterCollector("billing", staticCollector(`{}`)))

		// Sorted rather than in registration order, so two exports of the same
		// subject differ only when the data does.
		test.Eq(t, []string{"billing", "identity", "webhooks"}, registry.CollectorKeys())
	})

	T.Run("re-registering a key is an error", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		must.NoError(t, registry.RegisterCollector("identity", staticCollector(`{}`)))

		// A silent overwrite would drop a domain from every export from then
		// on, and the only symptom would be a missing section.
		err := registry.RegisterCollector("identity", staticCollector(`{}`))
		test.ErrorIs(t, err, ErrDuplicateKey)
	})

	T.Run("collectors and erasers are independent namespaces", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		must.NoError(t, registry.RegisterCollector("identity", staticCollector(`{}`)))
		must.NoError(t, registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))

		test.Eq(t, []string{"identity"}, registry.CollectorKeys())
		test.Eq(t, []string{"identity"}, registry.EraserKeys())
	})

	T.Run("a collector-only domain is legitimate", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		// A domain holding data it must export but may never delete — an
		// immutable ledger — is the normal case, not a misconfiguration.
		must.NoError(t, registry.RegisterCollector("ledger", staticCollector(`{}`)))

		test.SliceLen(t, 1, registry.CollectorKeys())
		test.SliceEmpty(t, registry.EraserKeys())
	})

	T.Run("rejects keys that are not plain identifiers", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		for _, key := range []string{"Identity", "meal planning", "billing-v2", "a..b", ".leading"} {
			err := registry.RegisterCollector(key, staticCollector(`{}`))
			test.ErrorIs(t, err, ErrInvalidKey, test.Sprintf("key %q", key))
		}
	})

	T.Run("accepts dotted and underscored keys", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		for _, key := range []string{"identity", "meal_planning", "billing.invoices"} {
			test.NoError(t, registry.RegisterCollector(key, staticCollector(`{}`)), test.Sprintf("key %q", key))
		}
	})

	T.Run("rejects nil registrations", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		test.ErrorIs(t, registry.RegisterCollector("identity", nil), platformerrors.ErrNilInputParameter)
		test.ErrorIs(t, registry.RegisterEraser("identity", nil), platformerrors.ErrNilInputParameter)
	})

	T.Run("lookups report absence", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		_, ok := registry.Collector("identity")
		test.False(t, ok)

		_, ok = registry.Eraser("identity")
		test.False(t, ok)
	})
}
