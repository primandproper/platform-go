package recording

import (
	"reflect"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recorderOwned names the audit.Entry fields a hook may not supply, each with
// the reason. Everything else on audit.Entry is the caller's, and Entry has to
// carry it under the same name and type.
//
// An entry is a decision, not an exemption: a field added to audit.Entry that
// is neither here nor on Entry fails TestEntryMirrorsAuditEntry until somebody
// says which it is.
var recorderOwned = map[string]string{
	"ID":         "minted by the audit recorder when empty, and a hook has no reason to pre-mint one",
	"RecordedAt": "stamped by the audit recorder's clock",
	"Seq":        "the chain's",
	"PrevHash":   "the chain's",
	"Hash":       "the chain's",
	"Actor":      "read off the context by this Recorder; a hook that could set it could attribute a write to anyone",
	"Scope":      "decided by this Recorder's ScopeResolver; a hook that could set it could file under another tenant's chain",
}

// TestEntryMirrorsAuditEntry is the claim Entry's doc makes, checked: it is the
// caller-supplied half of an audit.Entry and nothing else. Five fields exist
// twice because of it, and this is what keeps the two from drifting.
func TestEntryMirrorsAuditEntry(T *testing.T) {
	T.Parallel()

	auditEntry := reflect.TypeFor[audit.Entry]()
	entry := reflect.TypeFor[Entry]()

	T.Run("every caller-supplied audit field is on Entry, by name and type", func(t *testing.T) {
		t.Parallel()

		for field := range auditEntry.Fields() {
			if why, owned := recorderOwned[field.Name]; owned {
				test.NotEqOp(t, "", why)

				_, mirrored := entry.FieldByName(field.Name)
				test.False(t, mirrored, test.Sprintf("audit.Entry.%s is the recorder's and must not be on Entry", field.Name))

				continue
			}

			mirror, ok := entry.FieldByName(field.Name)
			must.True(t, ok, must.Sprintf("audit.Entry.%s is caller-supplied and Entry has no field for it; add it, or roster it as the recorder's", field.Name))
			test.EqOp(t, field.Type, mirror.Type, test.Sprintf("Entry.%s has a different type than audit.Entry.%s", field.Name, field.Name))
		}
	})

	T.Run("every Entry field but SubjectID is an audit field", func(t *testing.T) {
		t.Parallel()

		for field := range entry.Fields() {
			if field.Name == "SubjectID" {
				continue
			}

			_, ok := auditEntry.FieldByName(field.Name)
			test.True(t, ok, test.Sprintf("Entry.%s is not a field audit.Entry can carry", field.Name))
		}
	})

	T.Run("every recorder-owned field is a real audit field", func(t *testing.T) {
		t.Parallel()

		for name := range recorderOwned {
			_, ok := auditEntry.FieldByName(name)
			test.True(t, ok, test.Sprintf("recorderOwned names %s, which audit.Entry does not have", name))
		}
	})

	T.Run("file copies every mirrored field through", func(t *testing.T) {
		t.Parallel()

		// Every mirrored field set to something non-zero, by reflection, so a
		// field added to both types and forgotten in file fails here.
		supplied := &Entry{}
		value := reflect.ValueOf(supplied).Elem()

		for i := range entry.NumField() {
			field := entry.Field(i)
			if field.Name == "SubjectID" {
				continue
			}

			value.Field(i).Set(nonZero(t, field.Type))
		}

		r := &Recorder{scopeFor: writeScope}
		batches, order, err := r.file(t.Context(), testScope, audit.Actor{ID: "user-1"}, []*Entry{supplied})
		must.NoError(t, err)
		must.SliceLen(t, 1, order)
		must.SliceLen(t, 1, batches[testScope])

		filed := reflect.ValueOf(batches[testScope][0]).Elem()
		for i := range entry.NumField() {
			field := entry.Field(i)
			if field.Name == "SubjectID" {
				continue
			}

			test.True(t, reflect.DeepEqual(value.Field(i).Interface(), filed.FieldByName(field.Name).Interface()),
				test.Sprintf("file dropped Entry.%s on the way to audit.Entry", field.Name))
		}

		test.EqOp(t, "user-1", batches[testScope][0].Actor.ID)
		test.EqOp(t, testScope, batches[testScope][0].Scope)
	})
}

// nonZero builds a distinguishable value of t, for the kinds Entry's fields
// have. A kind this does not know is a failed test rather than a zero value
// that would pass the copy check vacuously.
func nonZero(t *testing.T, typ reflect.Type) reflect.Value {
	t.Helper()

	switch typ.Kind() {
	case reflect.String:
		return reflect.ValueOf("x").Convert(typ)
	case reflect.Map:
		m := reflect.MakeMap(typ)
		m.SetMapIndex(reflect.ValueOf("k").Convert(typ.Key()), reflect.Zero(typ.Elem()))

		return m
	case reflect.Int64:
		return reflect.ValueOf(int64(1)).Convert(typ)
	case reflect.Struct:
		if typ == reflect.TypeFor[tenancy.Scope]() {
			return reflect.ValueOf(tenancy.Of("x"))
		}
	default:
	}

	t.Fatalf("nonZero does not know how to build a %s; teach it", typ)

	return reflect.Value{}
}
