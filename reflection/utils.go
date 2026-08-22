package reflection

import (
	"reflect"
	"runtime"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
)

// GetTagNameByValue searches struct strukt (or *strukt) for a field whose value
// equals fieldValue and returns the name that field is encoded under for
// tagKey (e.g. "json").
//
// The name is resolved by FieldName, so it is the tag's name with any options
// stripped — a field tagged `json:"name,omitempty"` reports "name" — and a
// field with no tag for tagKey reports its own name rather than the empty
// string. A field the tag omits entirely is never matched.
//
// Notes and limitations:
//   - Values are compared with reflect.DeepEqual.
//   - Unexported fields are skipped, since their values cannot be read.
//   - The first match in struct field order wins, so a struct with two fields
//     holding equal values reports the earlier one. Passing a value that
//     several fields could hold — a zero string, say — is therefore ambiguous
//     by construction.
//   - Embedded fields are flattened exactly as encoding/json promotes them: an
//     anonymous field with no explicit tag name is searched through, while one
//     carrying a name is a named object and is compared whole. An embedded
//     field of an unexported type that carries a name is therefore invisible:
//     an embedded field's name is its type's name, so it cannot be read to be
//     compared, and the tag has opted it out of being searched through.
//   - A nil embedded pointer is skipped rather than stood in for by a zero
//     struct. Substituting one would make every zero field of the substitute
//     match a zero fieldValue, reporting a field that holds nothing as the
//     field that holds what was asked for.
//   - This requires the originating struct instance; a bare field value alone
//     is insufficient in Go.
func GetTagNameByValue(strukt, fieldValue any, tagKey string) (string, error) {
	value, present, err := StructValue(strukt)
	if err != nil {
		return "", err
	}

	if !present {
		return "", ErrNoValue
	}

	if name, ok := findFieldNameByValue(value, fieldValue, tagKey); ok {
		return name, nil
	}

	return "", ErrNoMatchingField
}

// findFieldNameByValue walks one struct level, descending through embedded
// structs, and reports the name of the first field equal to fieldValue.
func findFieldNameByValue(structValue reflect.Value, fieldValue any, tagKey string) (string, bool) {
	t := structValue.Type()

	for i := range t.NumField() {
		field := t.Field(i)

		name, explicit, skip := FieldName(&field, tagKey)
		if skip {
			continue
		}

		value := structValue.Field(i)

		// This runs before the readability check below, and has to: an embedded
		// field's name is its type's name, so `struct { base }` is an unexported
		// field whose exported members are nonetheless promoted — by
		// encoding/json, and so by this. reflect agrees, allowing those members
		// to be read even though the embedded field itself cannot be.
		if field.Anonymous && !explicit {
			if embedded, ok := embeddedStruct(value); ok {
				if found, matched := findFieldNameByValue(embedded, fieldValue, tagKey); matched {
					return found, true
				}

				continue
			}
		}

		if !value.IsValid() || !value.CanInterface() {
			continue
		}

		if reflect.DeepEqual(value.Interface(), fieldValue) {
			return name, true
		}
	}

	return "", false
}

// embeddedStruct resolves an embedded field to the struct value to descend
// into, reporting false for anything that is not one — including a nil
// embedded pointer, which is left to be compared as the pointer it is rather
// than searched through as the struct it is not.
func embeddedStruct(value reflect.Value) (reflect.Value, bool) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() || value.Type().Elem().Kind() != reflect.Struct {
			return reflect.Value{}, false
		}

		return value.Elem(), true
	}

	if value.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}

	return value, true
}

// GetMethodName is meant to fetch the name of a given method passed in as an argument.
func GetMethodName(method any) string {
	v := reflect.ValueOf(method)
	if v.Kind() != reflect.Func {
		return ""
	}

	if pc := v.Pointer(); pc != 0 {
		if f := runtime.FuncForPC(pc); f != nil {
			fullName := f.Name()
			parts := strings.Split(fullName, ".")
			if len(parts) > 0 {
				return strings.TrimSuffix(parts[len(parts)-1], "-fm")
			}
		}
	}

	return ""
}

// GetFieldTypes returns a map of field names to their types. For nested structs,
// the value is a map[string]any containing the nested struct's fields.
//
// The argument may be a struct, a pointer to one, a nil pointer to one, or a
// reflect.Type naming one: this describes a shape and so needs only the type.
//
// Fields are keyed by their Go names, not by any tag — this reports what the
// type declares, not what an encoder would write. Embedded structs are
// therefore recorded as a nested entry under the embedded type's name rather
// than flattened; use GetTagNameByValue or FieldName for the encoded view.
func GetFieldTypes(strukt any) (map[string]any, error) {
	t, err := StructType(strukt)
	if err != nil {
		return nil, err
	}

	return getFieldTypes(t, map[reflect.Type]bool{})
}

// getFieldTypes describes a struct type's fields. t must already be a struct
// type; both call sites resolve that before calling.
func getFieldTypes(t reflect.Type, visited map[reflect.Type]bool) (map[string]any, error) {
	// Track types on the current recursion path so self-referential types (e.g. Next *Node)
	// terminate instead of recursing until the stack overflows.
	visited[t] = true
	defer delete(visited, t)

	result := make(map[string]any)

	for sf := range t.Fields() {
		x := sf
		fieldType := x.Type

		// Skip unexported fields
		if !x.IsExported() {
			continue
		}

		// Check if it's a pointer type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		// If it's a struct, recursively get its fields
		if fieldType.Kind() == reflect.Struct {
			// A field whose type is already on the path is a cycle; record its type name
			// rather than recursing.
			if visited[fieldType] {
				result[x.Name] = fieldType.String()
				continue
			}

			nestedMap, err := getFieldTypes(fieldType, visited)
			if err != nil {
				return nil, errors.Wrapf(err, "error processing nested struct field %s", x.Name)
			}

			result[x.Name] = nestedMap
		} else {
			// For non-struct fields, store the type string
			result[x.Name] = fieldType.String()
		}
	}

	return result, nil
}
