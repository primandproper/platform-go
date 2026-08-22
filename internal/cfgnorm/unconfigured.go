package cfgnorm

import (
	"reflect"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/caarlos0/env/v11"
)

// UnconfiguredToNil releases the pointer sub-config fields of cfg that hold
// nothing the operator supplied.
//
// It is ZeroToNil scaled up one level, and it exists because ZeroToNil stops
// working once a sub-config stops being a leaf. Two things put a value into a
// freshly parsed sub-config without anyone asking for it:
//
//   - `env:",init"` recurses. Allocating *databasecfg.Config also allocates the
//     `,init` pointers nested inside it, so the outer struct is not zero even
//     though nothing was configured.
//   - `envDefault:` fills fields in. databasecfg.Config parses to
//     provider=postgres with four populated timeouts and pool sizes in an empty
//     environment, so it is never zero either.
//
// Under a plain ZeroToNil both of those read as "the operator configured this",
// which at a composition root means every subsystem is switched on by the act
// of parsing. So the comparison is against what the type itself parses to in an
// empty environment rather than against its zero value: whatever env parsing
// supplies on its own is not configuration. For a leaf config carrying no
// defaults the two are the same check.
//
// A sub-config the operator did fill in — in a file, in the environment, or by
// assignment afterwards — differs from that baseline and is left alone. The
// converse is the documented edge: a block that spells out nothing but the
// library's own defaults configures nothing, and is released.
//
// cfg must be a non-nil pointer to a struct. Only its own pointer-to-struct
// fields are considered; normalizing further down is each sub-config's own
// business, and it happens in their ValidateWithContext.
func UnconfiguredToNil(cfg any) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return errors.New("cfgnorm: UnconfiguredToNil needs a non-nil pointer to a struct")
	}

	v = v.Elem()
	structType := v.Type()

	for idx := range structType.NumField() {
		field := v.Field(idx)
		if !field.CanSet() || field.Kind() != reflect.Pointer || field.IsNil() || field.Type().Elem().Kind() != reflect.Struct {
			continue
		}

		baseline := reflect.New(field.Type().Elem())

		// An explicitly empty (non-nil) Environment overrides the default of
		// os.Environ, so the baseline sees only what the struct tags supply.
		if err := env.ParseWithOptions(baseline.Interface(), env.Options{Environment: map[string]string{}}); err != nil {
			return errors.Wrapf(err, "parsing the unconfigured baseline for %s", structType.Field(idx).Name)
		}

		if reflect.DeepEqual(field.Interface(), baseline.Interface()) {
			field.SetZero()
		}
	}

	return nil
}
