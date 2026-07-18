package bitmask_test

import (
	"encoding/json"
	"fmt"

	"github.com/primandproper/platform-go/v5/bitmask"
)

type Permission uint8

const (
	Read   Permission = 1 << iota // 1
	Write                         // 2
	Delete                        // 4
	Admin                         // 8
)

func ExampleNew() {
	mask := bitmask.New(Read, Write)
	fmt.Println(mask.Value())
	// Output: 3
}

func ExampleNew_empty() {
	mask := bitmask.New[Permission]()
	fmt.Println(mask.IsEmpty())
	// Output: true
}

func ExampleFromValue() {
	mask := bitmask.FromValue(Permission(5))
	fmt.Println(mask.Has(Read), mask.Has(Delete))
	// Output: true true
}

func ExampleBitmask_Set() {
	base := bitmask.New(Read)
	mask := base.Set(Write, Delete)
	fmt.Println(mask.Has(Read), mask.Has(Write), mask.Has(Delete))
	// Output: true true true
}

func ExampleBitmask_Clear() {
	base := bitmask.New(Read, Write)
	mask := base.Clear(Write)
	fmt.Println(mask.Has(Read), mask.Has(Write))
	// Output: true false
}

func ExampleBitmask_Toggle() {
	base := bitmask.New(Read)
	mask := base.Toggle(Read, Write)
	fmt.Println(mask.Has(Read), mask.Has(Write))
	// Output: false true
}

func ExampleBitmask_Has() {
	mask := bitmask.New(Read, Write)
	fmt.Println(mask.Has(Read), mask.Has(Admin))
	// Output: true false
}

func ExampleBitmask_HasAll() {
	mask := bitmask.New(Read, Write, Delete)
	fmt.Println(mask.HasAll(Read, Write), mask.HasAll(Read, Admin))
	// Output: true false
}

func ExampleBitmask_HasAny() {
	mask := bitmask.New(Read)
	fmt.Println(mask.HasAny(Read, Write), mask.HasAny(Delete, Admin))
	// Output: true false
}

func ExampleBitmask_Count() {
	mask := bitmask.New(Read, Write, Delete)
	fmt.Println(mask.Count())
	// Output: 3
}

func ExampleBitmask_String() {
	mask := bitmask.New(Read, Delete)
	fmt.Println(mask.String())
	// Output: 00000101
}

func ExampleBitmask_Union() {
	a := bitmask.New(Read)
	b := bitmask.New(Write)
	result := a.Union(b)
	fmt.Println(result.Value())
	// Output: 3
}

func ExampleBitmask_Intersect() {
	a := bitmask.New(Read, Write)
	b := bitmask.New(Write, Delete)
	result := b.Intersect(a)
	fmt.Println(result.Value())
	// Output: 2
}

func ExampleBitmask_Difference() {
	a := bitmask.New(Read, Write, Delete)
	b := bitmask.New(Write)
	result := a.Difference(b)
	fmt.Println(result.Value())
	// Output: 5
}

func ExampleBitmask_MarshalJSON() {
	mask := bitmask.New(Read, Delete)

	data, err := json.Marshal(&mask)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output: 5
}

func ExampleBitmask_UnmarshalJSON() {
	var mask bitmask.Bitmask[Permission]
	_ = json.Unmarshal([]byte("5"), &mask)
	fmt.Println(mask.Has(Read), mask.Has(Delete))
	// Output: true true
}

func ExampleNew_permissions() {
	userPerms := bitmask.New(Read, Write)
	required := bitmask.New(Read, Admin)

	if userPerms.HasAll(Read, Admin) {
		fmt.Println("full access")
	} else if userPerms.HasAny(Read, Admin) {
		missing := required.Difference(userPerms)
		fmt.Println("partial access, missing", missing.Count(), "permissions")
	}
	// Output: partial access, missing 1 permissions
}
