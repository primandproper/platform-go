package charset_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/charset"
)

func ExampleNew() {
	// A name that may hold letters, digits and underscores, and may not start
	// with a digit.
	name := charset.New(
		charset.ASCIIAlphanumeric.Union(charset.Bytes('_')),
		charset.WithFirst(charset.ASCIILetters.Union(charset.Bytes('_'))),
	)

	fmt.Println(name.Valid("outbox_messages"), name.Valid("1table"), name.Valid(""))
	// Output: true false false
}

func ExampleAllowEmpty() {
	// A table prefix, where empty is a value rather than a missing one: it
	// renders the component's own names.
	prefix := charset.New(
		charset.ASCIIAlphanumeric.Union(charset.Bytes('_')),
		charset.WithFirst(charset.ASCIILetters.Union(charset.Bytes('_'))),
		charset.AllowEmpty(),
	)

	fmt.Println(prefix.Valid(""), prefix.Valid("ddb"), prefix.Valid("ddb-1"))
	// Output: true true false
}

func ExampleWithSeparator() {
	// A table name, optionally qualified by exactly one schema.
	identifier := charset.New(
		charset.ASCIIAlphanumeric.Union(charset.Bytes('_')),
		charset.WithFirst(charset.ASCIILetters.Union(charset.Bytes('_'))),
		charset.WithSeparator('.', 2),
	)

	fmt.Println(identifier.Valid("outbox_messages"))
	fmt.Println(identifier.Valid("app.outbox_messages"))
	fmt.Println(identifier.Valid("db.app.outbox_messages"))
	fmt.Println(identifier.Valid("app."))
	// Output:
	// true
	// true
	// false
	// false
}

func ExampleWithExactLength() {
	deviceToken := charset.New(charset.HexDigits, charset.WithExactLength(64))

	fmt.Println(deviceToken.Valid("deadbeef00112233445566778899aabbccddeeff00112233445566778899aabb"))
	fmt.Println(deviceToken.Valid("deadbeef"))
	// Output:
	// true
	// false
}

func ExampleSet_ContainsAll() {
	// A rule that is nothing but an alphabet needs no Checker. This one is a
	// denylist, so it starts from every byte and subtracts.
	keyBytes := charset.AllBytes.Without(charset.Bytes(0, '\n', '\r'))

	fmt.Println(keyBytes.ContainsAll("tenant:acme:order:01HQ8Z"))
	fmt.Println(keyBytes.ContainsAll("tenant\nacme"))
	// Output:
	// true
	// false
}

func ExampleSet_String() {
	// Runs come out in byte order, so '_' lands between 'Z' and 'a'.
	fmt.Println(charset.ASCIIAlphanumeric.Union(charset.Bytes('_')))
	fmt.Println(charset.VisibleASCII)
	// Output:
	// [0-9A-Z_a-z]
	// [!-~]
}

// Nothing is decoded, so a byte outside the alphabet is refused on its own
// terms whether or not it spells a character.
func ExampleChecker_Valid_nonASCII() {
	name := charset.New(charset.ASCIILetters)

	fmt.Println(name.Valid("naive"), name.Valid("naïve"), name.Valid("a\xffb"))
	// Output: true false false
}
