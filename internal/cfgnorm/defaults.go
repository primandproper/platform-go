package cfgnorm

import (
	"reflect"

	"github.com/primandproper/platform-go/v13/errors"
)

// defaulter is the half of the config convention that is not expressible in a
// struct tag. A package whose defaults fit `envDefault:` uses those; a package
// whose defaults are computed, conditional, or derived from another field
// writes EnsureDefaults instead.
type defaulter interface {
	EnsureDefaults()
}

// EnsureSubDefaults calls EnsureDefaults on every non-nil pointer sub-config of
// cfg that has one.
//
// It exists because a composition root validates sub-configs it did not
// construct. Every constructor in this module applies defaults before
// validating — an unset field with a documented default is not a validation
// failure — but a parent that hands a sub-config straight to ozzo skips that
// step, and the sub-configs whose defaults live in EnsureDefaults rather than in
// `envDefault:` tags are then rejected for every field the operator left to the
// library. An outbox configured entirely from the environment fails on seven
// blank fields it was never meant to have to name.
//
// Call it after UnconfiguredToNil and never before: defaulting first fills in
// every sub-config `env:",init"` allocated, and nothing would ever look
// unconfigured again.
//
// cfg must be a non-nil pointer to a struct. Only its own pointer-to-struct
// fields are considered — defaulting further down is each sub-config's own
// business, and its EnsureDefaults is where that happens.
func EnsureSubDefaults(cfg any) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return errors.New("cfgnorm: EnsureSubDefaults needs a non-nil pointer to a struct")
	}

	v = v.Elem()

	for _, field := range v.Fields() {
		if !field.CanInterface() || field.Kind() != reflect.Pointer || field.IsNil() || field.Type().Elem().Kind() != reflect.Struct {
			continue
		}

		if d, ok := field.Interface().(defaulter); ok {
			d.EnsureDefaults()
		}
	}

	return nil
}
