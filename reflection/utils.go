package reflection

import (
	"reflect"
	"runtime"
	"strings"

	"github.com/primandproper/platform-go/v3/errors"
)

// GetTagNameByValue searches struct s (or *s) for a field whose value equals fieldValue
// and returns the tag for tagKey (e.g. "json") for the first match.
// It returns an error if s is not a struct or no matching field is found.
//
// Notes & limitations:
// - It compares field values using reflect.DeepEqual.
// - Unexported fields are skipped (fv.CanInterface() == false).
// - If multiple fields have identical values, the first match is returned (search order is struct field order).
// - This requires passing the originating struct instance; a bare field value alone is insufficient in Go.
func GetTagNameByValue(strukt, fieldValue any, tagKey string) (string, error) {
	v := reflect.ValueOf(strukt)
	if !v.IsValid() {
		return "", errors.New("nil value")
	}

	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", errors.New("nil pointer to struct")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", errors.Newf("GetTagNameByValue: expected struct or pointer to struct, got %s", v.Kind())
	}

	t := v.Type()
	var walk func(reflect.Value, reflect.Type) (string, bool)
	walk = func(rv reflect.Value, rt reflect.Type) (string, bool) {
		for i := 0; i < rt.NumField(); i++ {
			sf := rt.Field(i)
			fv := rv.Field(i)

			// if embedded struct, recurse
			if sf.Anonymous {
				// handle pointer-to-embedded
				fvKind := fv.Kind()
				if fvKind == reflect.Pointer && !fv.IsNil() {
					fv = fv.Elem()
					fvKind = fv.Kind()
				}
				if fvKind == reflect.Struct {
					if tag, ok := walk(fv, fv.Type()); ok {
						return tag, true
					}
				}
				continue
			}

			// skip unexported fields we can't interface with
			if !fv.IsValid() || !fv.CanInterface() {
				continue
			}

			if reflect.DeepEqual(fv.Interface(), fieldValue) {
				return sf.Tag.Get(tagKey), true
			}
		}
		return "", false
	}

	if tag, ok := walk(v, t); ok {
		return tag, nil
	}
	return "", errors.New("no matching field with that value found in struct")
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
func GetFieldTypes(strukt any) (map[string]any, error) {
	var t reflect.Type

	switch v := strukt.(type) {
	case reflect.Type:
		t = v
	default:
		rv := reflect.ValueOf(strukt)
		if !rv.IsValid() {
			return nil, errors.New("nil value")
		}

		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				// For nil pointer, try to get the type from the pointer type
				t = rv.Type().Elem()
			} else {
				t = rv.Elem().Type()
			}
		} else {
			t = rv.Type()
		}
	}

	return getFieldTypes(t, map[reflect.Type]bool{})
}

func getFieldTypes(t reflect.Type, visited map[reflect.Type]bool) (map[string]any, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil, errors.Newf("GetFieldTypes: expected struct or pointer to struct, got %v", t.Kind())
	}

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
