package mediaregistry

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSubject(T *testing.T) {
	T.Parallel()

	T.Run("is attached only with both halves", func(t *testing.T) {
		t.Parallel()

		test.True(t, Subject{Type: "invoice", ID: "invoice_1"}.Attached())
		test.False(t, Subject{}.Attached())
		test.False(t, Subject{Type: "invoice"}.Attached())
		test.False(t, Subject{ID: "invoice_1"}.Attached())
	})

	T.Run("accepts the pair and the absence, and refuses a half", func(t *testing.T) {
		t.Parallel()

		// A type with no id names a table rather than a row, and an id with no
		// type names a row in no particular table. Either alone is a value
		// nothing can look up.
		must.NoError(t, Subject{}.Validate())
		must.NoError(t, Subject{Type: "invoice", ID: "invoice_1"}.Validate())
		must.ErrorIs(t, Subject{Type: "invoice"}.Validate(), ErrPartialSubject)
		must.ErrorIs(t, Subject{ID: "invoice_1"}.Validate(), ErrPartialSubject)
	})

	T.Run("renders for a span attribute", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "invoice:invoice_1", Subject{Type: "invoice", ID: "invoice_1"}.String())
		test.EqOp(t, "", Subject{}.String())
		test.EqOp(t, "", Subject{Type: "invoice"}.String())
	})
}

func TestObject_ValidateWithContext(T *testing.T) {
	T.Parallel()

	valid := func() *Object {
		return &Object{
			Scope:       tenancy.Of("tenant_1"),
			Key:         "avatars/grace/original.png",
			ContentType: "image/png",
			OwnerID:     "user_1",
			Size:        1024,
		}
	}

	T.Run("accepts a row that can answer who may read it", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, valid().ValidateWithContext(t.Context()))

		// The content type is optional: a provider that sniffed it and did not
		// report back leaves it empty, and a row that recorded nothing is
		// honest about that.
		noType := valid()
		noType.ContentType = ""
		must.NoError(t, noType.ValidateWithContext(t.Context()))

		// So is the size, for a zero-byte object.
		empty := valid()
		empty.Size = 0
		must.NoError(t, empty.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a row with no key", func(t *testing.T) {
		t.Parallel()

		object := valid()
		object.Key = ""
		must.Error(t, object.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a row with no owner", func(t *testing.T) {
		t.Parallel()

		// A row with no owner cannot decide who may read it, which is worse
		// than no row at all: the check that reads it finds an owner nobody
		// matches, or one everybody does.
		object := valid()
		object.OwnerID = ""
		must.Error(t, object.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a negative size", func(t *testing.T) {
		t.Parallel()

		object := valid()
		object.Size = -1
		must.Error(t, object.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a half-filled subject", func(t *testing.T) {
		t.Parallel()

		object := valid()
		object.BelongsTo = Subject{ID: "invoice_1"}
		must.ErrorIs(t, object.ValidateWithContext(t.Context()), ErrPartialSubject)
	})
}

// TestAttributeKeys pins that the exported attribute key is the one this
// package actually attaches, so a consumer labeling its own instruments with it
// charts against these spans rather than beside them.
func TestAttributeKeys(t *testing.T) {
	t.Parallel()

	test.EqOp(t, objectIDKey, ObjectAttributeKey)
	test.True(t, strings.HasPrefix(ObjectAttributeKey, serviceName+"."))
}
