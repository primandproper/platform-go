package routeplan

import (
	"fmt"
	"reflect"
	"strconv"
)

// SetScalar parses raw into fv. Types that implement encoding.TextUnmarshaler
// (uuid.UUID, time.Time, ...) parse themselves; otherwise the field's kind
// selects the strconv parser.
func SetScalar(fv reflect.Value, raw string) error {
	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}

		fv = fv.Elem()
	}

	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(raw))
		}
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Parsed at the field's own width, not at 64 and then narrowed by SetInt.
		// Parsing wide and setting narrow wraps silently: ?count=300 into an int8
		// bound to 44, and the handler received a plausible number rather than the
		// 400 the request had earned.
		n, err := strconv.ParseInt(raw, 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported parameter kind %s", fv.Kind())
	}

	return nil
}

// FormatScalar renders fv as the string a request carries it as — the inverse of
// SetScalar, and the same dispatch in reverse: a type that parses itself with
// UnmarshalText renders itself with MarshalText, and everything else goes through
// strconv at the field's own width.
//
// present reports whether the parameter should be sent at all. A nil pointer is
// the one case it is false: a pointer param field is how an input says "leave
// this one out", and the server reads an absent parameter as exactly that. A
// non-pointer field is always sent, even at its zero value, so that the In a
// caller passes and the In the handler receives are the same value.
func FormatScalar(fv reflect.Value) (text string, present bool, err error) {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return "", false, nil
		}

		fv = fv.Elem()
	}

	if m, ok := textMarshaler(fv); ok {
		b, marshalErr := m.MarshalText()
		if marshalErr != nil {
			return "", false, marshalErr
		}

		return string(b), true, nil
	}

	switch fv.Kind() {
	case reflect.String:
		return fv.String(), true, nil
	case reflect.Bool:
		return strconv.FormatBool(fv.Bool()), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(fv.Int(), 10), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(fv.Uint(), 10), true, nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(fv.Float(), 'g', -1, fv.Type().Bits()), true, nil
	default:
		return "", false, fmt.Errorf("unsupported parameter kind %s", fv.Kind())
	}
}

// textMarshaler returns fv as a TextMarshaler if its type implements one.
//
// The addressable copy is what makes this symmetric with SetScalar, which can
// always take the address of the field it is populating. A value read out of a
// non-addressable input has no address to take, and a MarshalText declared on
// the pointer receiver would go unnoticed — the parameter would then be rendered
// by kind, which for a struct is an error and for a named string is the wrong
// text.
func textMarshaler(fv reflect.Value) (TextMarshaler, bool) {
	if m, ok := fv.Interface().(TextMarshaler); ok {
		return m, true
	}

	if fv.CanAddr() {
		m, ok := fv.Addr().Interface().(TextMarshaler)

		return m, ok
	}

	if !reflect.PointerTo(fv.Type()).Implements(reflect.TypeFor[TextMarshaler]()) {
		return nil, false
	}

	addressable := reflect.New(fv.Type())
	addressable.Elem().Set(fv)

	m, ok := addressable.Interface().(TextMarshaler)

	return m, ok
}
