package mediaregistry

import (
	"reflect"
	"strings"
	"testing"

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

func TestObjectInput_ValidateWithContext(T *testing.T) {
	T.Parallel()

	// No Scope among these, and that is the type rather than the fixture: the
	// scope of a write is the argument the write is called with, and an input
	// has nowhere to carry a second copy of it.
	valid := func() *ObjectInput {
		return &ObjectInput{
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

// TestObjectInput_CarriesNothingTheWriteSettles is the guard on the distinction
// ObjectInput exists to make.
//
// While the argument and the row were one type, a caller that kept using its own
// argument after the write — handing it to a response, an audit entry, a cache —
// compiled cleanly and shipped a zero CreatedAt and a zero Size. Every field
// named here is one the write settles and the row reports, and a field of that
// name reappearing on the input would make that mistake representable again
// without anything else in the suite noticing.
func TestObjectInput_CarriesNothingTheWriteSettles(t *testing.T) {
	t.Parallel()

	input := reflect.TypeFor[ObjectInput]()

	for _, settled := range []string{"CreatedAt", "LastUpdatedAt", "ArchivedAt", "Scope"} {
		_, found := input.FieldByName(settled)
		test.False(t, found, test.Sprintf(
			"ObjectInput.%s is a field the write settles, and an input that carries one is an "+
				"argument a caller can mistake for the row", settled))

		// And each is genuinely on the row, so this test fails if the field was
		// renamed rather than if the distinction was lost.
		_, onRow := reflect.TypeFor[Object]().FieldByName(settled)
		test.True(t, onRow, test.Sprintf("Object.%s is where the write reports it", settled))
	}

	// The other half: what a caller does supply is here, so the input is not
	// merely smaller but is the whole of the write's question.
	for _, supplied := range []string{"ID", "Key", "ContentType", "OwnerID", "BelongsTo", "Size"} {
		_, found := input.FieldByName(supplied)
		test.True(t, found, test.Sprintf("ObjectInput.%s is the caller's to set", supplied))
	}
}
