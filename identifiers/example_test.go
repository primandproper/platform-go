package identifiers_test

import (
	"fmt"

	"github.com/primandproper/platform-go/identifiers"
)

func ExampleNew() {
	id := identifiers.New()

	// IDs are 20-character xid strings
	fmt.Println(len(id))
	// Output: 20
}

func ExampleValidate() {
	id := identifiers.New()
	err := identifiers.Validate(id)
	fmt.Println(err)
	// Output: <nil>
}
