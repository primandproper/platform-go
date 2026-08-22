package fake

import (
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/go-faker/faker/v4"
)

// BuildFakeID builds a fake identifier of the kind this platform issues.
//
// A random string is not one: identifiers are what rows join on, so a fake carrying
// twenty-five random letters where an identifier goes is a row that joins to nothing,
// and the test that fails is several layers away from the fake that caused it.
func BuildFakeID() string {
	return identifiers.New()
}

// BuildFakeNumber builds a whole number from the range generated numeric fields draw
// from.
//
// Whole for the reason BuildFakeRecord rounds the floats it generates: the columns
// these values round-trip through mostly keep two decimal places, and one that keeps
// fewer than the value does returns something the caller did not save.
func BuildFakeNumber() float64 {
	return float64(MustBuildFake[int]())
}

// BuildFakeString builds a fake string for a field that holds prose — a name, a
// description, a note.
//
// A sentence rather than a single word, because these values are read in failure
// messages, and six words drawn from a list are far likelier to be distinguishable
// than two fakes that both say "quo".
func BuildFakeString() string {
	return faker.Sentence()
}

// BuildFakePassword builds a fake password.
//
// Long, because the rule a password is usually checked against is a minimum length,
// and a fake that fails it turns every test that registers a user into a test of
// validation.
func BuildFakePassword() string {
	return faker.Password()
}
