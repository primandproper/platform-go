package countwidth_test

import (
	"reflect"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// results is every type in this module whose exported fields report how many of
// something a pass touched.
//
// Keyed by the name a failure has to hand a reader — package and type, since
// two packages may well both call one Result — and holding the type itself
// rather than a value, because what is being checked is the shape and not
// anything a run produced.
var results = map[string]reflect.Type{
	"metering.FlushResult":     reflect.TypeFor[metering.FlushResult](),
	"audit.VerificationResult": reflect.TypeFor[audit.VerificationResult](),
	"audit.Break":              reflect.TypeFor[audit.Break](),
}

// narrowIntegerKinds are the integer kinds an exported count may not have.
//
// It lists what is disallowed rather than asserting int64 outright, so that a
// field which is not an integer at all — a time, a scope, a reason, a break — is
// left alone. That is what lets a result type be rostered whole instead of field
// by field, and what makes a count added to one later covered by the row that is
// already there rather than by a row somebody has to remember to extend.
//
// uint64 is here with the rest. It is not narrow, but a count that answers in it
// is a count that cannot be compared against the int64 ones without a
// conversion, and the width was never the whole of the rule — the module answers
// counts in one type.
var narrowIntegerKinds = map[reflect.Kind]bool{
	reflect.Int:    true,
	reflect.Int8:   true,
	reflect.Int16:  true,
	reflect.Int32:  true,
	reflect.Uint:   true,
	reflect.Uint8:  true,
	reflect.Uint16: true,
	reflect.Uint32: true,
	reflect.Uint64: true,
}

// TestEveryRosteredCountIsWide is the rule this package exists for.
//
// The assertion is over every exported field rather than over a list of the
// ones that count, which is deliberate and is why the message says what it
// says: a field this test has never seen is exactly the field a widening
// somewhere else would have missed, and a roster of field names would have to
// be extended by the same person who added the narrow one.
func TestEveryRosteredCountIsWide(T *testing.T) {
	T.Parallel()

	for name, rt := range results {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			checked := 0

			for field := range rt.Fields() {
				if !field.IsExported() {
					continue
				}

				checked++

				test.False(t, narrowIntegerKinds[field.Type.Kind()], test.Sprintf(
					"%s.%s is an exported %s. A count answers in int64 here, and a narrower one is a "+
						"widening nobody can see coming and a major version when it arrives; a field that "+
						"is not a count does not belong on a result type",
					name, field.Name, field.Type.Kind()))
			}

			must.Positive(t, checked, must.Sprintf("%s has no exported fields, so this asserted nothing", name))
		})
	}
}
