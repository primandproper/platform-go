package reflection

import (
	"reflect"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNotAStruct indicates a value that is neither a struct nor a pointer to
	// one, and so has no fields to inspect.
	ErrNotAStruct = errors.New("reflection: value is not a struct or a pointer to one")

	// ErrNoValue indicates an argument that carried no value at all: an untyped
	// nil, an invalid reflect.Value, or a nil pointer. It is distinct from
	// ErrNotAStruct because a nil *T still names a type, and callers that only
	// need the type — GetFieldTypes, for one — can proceed where callers that
	// need the fields' values cannot.
	ErrNoValue = errors.New("reflection: no value to inspect")

	// ErrNoMatchingField indicates a search that completed without finding the
	// field it was looking for.
	ErrNoMatchingField = errors.New("reflection: no matching field found in struct")
)

// StructValue resolves an argument to the struct value underneath it, reporting
// whether there was one at all.
//
// The three ways a Go argument can be absent — an untyped nil interface, an
// invalid reflect.Value, and a typed nil pointer — all collapse to
// present=false with a nil error. That is the distinction most callers of
// reflect get wrong: a nil *T is not an error, it is a value that exists in the
// type system and not at runtime, and whether that is fatal depends on the
// caller. Callers that need a value return ErrNoValue on !present; callers that
// only need a type should use StructType, which accepts a nil pointer happily.
//
// A non-nil argument that is not a struct is ErrNotAStruct, wrapped with the
// kind that was passed instead.
func StructValue(v any) (value reflect.Value, present bool, err error) {
	if v == nil {
		return reflect.Value{}, false, nil
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return reflect.Value{}, false, nil
	}

	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, false, nil
		}

		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false, errors.Wrapf(ErrNotAStruct, "got %s", rv.Kind())
	}

	return rv, true, nil
}

// StructType resolves an argument to the struct type underneath it. The
// argument may be a struct, a pointer to one, a nil pointer to one, or a
// reflect.Type naming one.
//
// This is the type-only counterpart to StructValue, and it exists because the
// two answer different questions: a nil *T has no fields to read but does have
// fields to describe.
func StructType(v any) (reflect.Type, error) {
	var t reflect.Type

	switch typed := v.(type) {
	case nil:
		return nil, ErrNoValue
	case reflect.Type:
		t = typed
	default:
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return nil, ErrNoValue
		}

		t = rv.Type()
	}

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil, errors.Wrapf(ErrNotAStruct, "got %s", t.Kind())
	}

	return t, nil
}

// FieldName resolves the name a struct field is encoded under for tagKey,
// reporting whether the name was given explicitly by the tag and whether the
// field is skipped entirely.
//
// The convention is encoding/json's, which most tag-driven encoders share: the
// value up to the first comma is the name, anything after it is options, an
// absent or empty tag means the field's own name, and "-" means omit. The one
// piece of trivia worth having written down once is that json:"-" omits a field
// while json:"-," names it "-"; both are honored here so that a caller
// describing a struct and an encoder writing it never disagree about which
// fields exist.
//
// The explicit return distinguishes a name the tag gave from one defaulted to
// the field's own name. Callers flattening embedded fields need it: an
// anonymous field with no name of its own is promoted, while one carrying an
// explicit name is a named object in the encoded form.
//
// field is taken by pointer only because reflect.StructField is large enough
// that passing it by value trips the hugeParam check.
func FieldName(field *reflect.StructField, tagKey string) (name string, explicit, skip bool) {
	tag, ok := field.Tag.Lookup(tagKey)
	if !ok {
		return field.Name, false, false
	}

	tagged, _, _ := strings.Cut(tag, ",")

	switch tagged {
	case "-":
		if strings.HasPrefix(tag, "-,") {
			return "-", true, false
		}

		return "", false, true
	case "":
		return field.Name, false, false
	default:
		return tagged, true, false
	}
}

// DerefOrZero dereferences a pointer, yielding the zero value of its element
// type when it is nil. A non-pointer is returned unchanged.
//
// It is for walks that must keep descending through an absent pointer rather
// than stopping at it — comparing two structs field by field when one side's
// embedded pointer is nil, say. Note that this is the right behavior only when
// the zero value is a meaningful stand-in for absence. A search matching on
// field values should skip a nil pointer instead, because every zero field of
// the substitute would match a zero needle.
func DerefOrZero(v reflect.Value) reflect.Value {
	if v.Kind() != reflect.Pointer {
		return v
	}

	if v.IsNil() {
		return reflect.Zero(v.Type().Elem())
	}

	return v.Elem()
}
