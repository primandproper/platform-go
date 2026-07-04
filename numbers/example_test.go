package numbers_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v3/numbers"
)

func ExampleRoundToDecimalPlaces() {
	result := numbers.RoundToDecimalPlaces(3.14159, 2)
	fmt.Println(result)
	// Output: 3.14
}

func ExampleScale() {
	// Double a quantity of 2.5
	result := numbers.Scale(2.5, 2.0)
	fmt.Println(result)
	// Output: 5
}

func ExampleScaleToYield() {
	// Scale from 4 servings to 6 servings
	result := numbers.ScaleToYield(2.0, 4, 6)
	fmt.Println(result)
	// Output: 3
}
