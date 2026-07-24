package pointer_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v6/pointer"
)

func ExampleTo() {
	p := pointer.To("hello")
	fmt.Println(*p)
	// Output: hello
}

func ExampleDereference() {
	s := "world"
	val := pointer.Dereference(&s)
	fmt.Println(val)
	// Output: world
}

func ExampleDereference_nil() {
	val := pointer.Dereference[string](nil)
	fmt.Println(val)
	// Output:
}

func ExampleToSlice() {
	vals := []int{1, 2, 3}
	ptrs := pointer.ToSlice(vals)
	fmt.Println(*ptrs[0], *ptrs[1], *ptrs[2])
	// Output: 1 2 3
}

func ExampleDereferenceSlice() {
	a, b, c := 10, 20, 30
	vals := pointer.DereferenceSlice([]*int{&a, &b, &c})
	fmt.Println(vals)
	// Output: [10 20 30]
}
