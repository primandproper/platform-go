package reflection

import (
	"reflect"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type exampleStruct struct {
	Field1 string `json:"field1"`
	Field2 string `json:"field2"`
}

type embeddedParent struct {
	exampleStruct
	Field3 string `json:"field3"`
}

type pointerEmbeddedParent struct {
	*exampleStruct
	Field3 string `json:"field3"`
}

type nestedStruct struct {
	Inner    exampleStruct  `json:"inner"`
	InnerPtr *exampleStruct `json:"inner_ptr"`
	Name     string         `json:"name"`
}

type unexportedFieldStruct struct {
	unexported string
	Exported   string `json:"exported"`
}

type selfReferentialNode struct {
	Next *selfReferentialNode `json:"next"`
	Name string               `json:"name"`
}

type mutuallyRecursiveA struct {
	B    *mutuallyRecursiveB `json:"b"`
	Name string              `json:"name"`
}

type mutuallyRecursiveB struct {
	A    *mutuallyRecursiveA `json:"a"`
	Name string              `json:"name"`
}

func TestGetTagNameByValue(T *testing.T) {
	T.Parallel()

	T.Run("with pointer", func(t *testing.T) {
		t.Parallel()

		x := &exampleStruct{}
		expected := "field1"

		actual, err := GetTagNameByValue(x, x.Field1, "json")
		test.NoError(t, err)
		test.EqOp(t, expected, actual)
	})

	T.Run("unpointered", func(t *testing.T) {
		t.Parallel()

		x := exampleStruct{}
		expected := "field1"

		actual, err := GetTagNameByValue(x, x.Field1, "json")
		test.NoError(t, err)
		test.EqOp(t, expected, actual)
	})

	T.Run("with nil value", func(t *testing.T) {
		t.Parallel()

		actual, err := GetTagNameByValue(nil, "blah", "json")
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})

	T.Run("with nil pointer", func(t *testing.T) {
		t.Parallel()

		var x *exampleStruct

		actual, err := GetTagNameByValue(x, "blah", "json")
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})

	T.Run("with non-struct value", func(t *testing.T) {
		t.Parallel()

		actual, err := GetTagNameByValue("not a struct", "blah", "json")
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})

	T.Run("with no matching field", func(t *testing.T) {
		t.Parallel()

		x := exampleStruct{
			Field1: "value1",
			Field2: "value2",
		}

		actual, err := GetTagNameByValue(x, "nonexistent", "json")
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})

	T.Run("with embedded struct", func(t *testing.T) {
		t.Parallel()

		x := embeddedParent{
			exampleStruct: exampleStruct{
				Field1: "unique_value",
			},
			Field3: "field3_value",
		}

		actual, err := GetTagNameByValue(x, "unique_value", "json")
		test.NoError(t, err)
		test.EqOp(t, "field1", actual)
	})

	T.Run("with pointer-embedded struct", func(t *testing.T) {
		t.Parallel()

		x := pointerEmbeddedParent{
			exampleStruct: &exampleStruct{
				Field1: "unique_ptr_value",
			},
			Field3: "field3_value",
		}

		actual, err := GetTagNameByValue(x, "unique_ptr_value", "json")
		test.NoError(t, err)
		test.EqOp(t, "field1", actual)
	})

	T.Run("with nil pointer-embedded struct", func(t *testing.T) {
		t.Parallel()

		x := pointerEmbeddedParent{
			exampleStruct: nil,
			Field3:        "unique_nil_embed",
		}

		actual, err := GetTagNameByValue(x, "unique_nil_embed", "json")
		test.NoError(t, err)
		test.EqOp(t, "field3", actual)
	})

	T.Run("with second field match", func(t *testing.T) {
		t.Parallel()

		x := exampleStruct{
			Field1: "aaa",
			Field2: "bbb",
		}

		actual, err := GetTagNameByValue(x, "bbb", "json")
		test.NoError(t, err)
		test.EqOp(t, "field2", actual)
	})

	T.Run("with unexported fields", func(t *testing.T) {
		t.Parallel()

		x := unexportedFieldStruct{
			Exported: "unique_exported_val",
		}

		actual, err := GetTagNameByValue(x, "unique_exported_val", "json")
		test.NoError(t, err)
		test.EqOp(t, "exported", actual)
	})

	T.Run("with pointer to non-struct", func(t *testing.T) {
		t.Parallel()

		s := "hello"
		actual, err := GetTagNameByValue(&s, "hello", "json")
		test.Error(t, err)
		test.EqOp(t, "", actual)
	})
}

func TestGetMethodName(T *testing.T) {
	T.Parallel()

	T.Run("with a function", func(t *testing.T) {
		t.Parallel()

		actual := GetMethodName(TestGetMethodName)
		test.EqOp(t, "TestGetMethodName", actual)
	})

	T.Run("with a method", func(t *testing.T) {
		t.Parallel()

		// exampleStruct has no exported methods, so use a known interface method
		r := reflect.TypeFor[*exampleStruct]()
		actual := GetMethodName(r.Kind)
		test.EqOp(t, "Kind", actual)
	})

	T.Run("with non-function value", func(t *testing.T) {
		t.Parallel()

		actual := GetMethodName("not a function")
		test.EqOp(t, "", actual)
	})

	T.Run("with anonymous function", func(t *testing.T) {
		t.Parallel()

		fn := func() {}
		actual := GetMethodName(fn)
		test.NotEq(t, "", actual)
	})
}

func TestGetFieldTypes(T *testing.T) {
	T.Parallel()

	T.Run("with struct value", func(t *testing.T) {
		t.Parallel()

		x := exampleStruct{}
		result, err := GetFieldTypes(x)
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
		test.Eq(t, "string", result["Field2"])
	})

	T.Run("with pointer to struct", func(t *testing.T) {
		t.Parallel()

		x := &exampleStruct{}
		result, err := GetFieldTypes(x)
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
		test.Eq(t, "string", result["Field2"])
	})

	T.Run("with nil pointer to struct", func(t *testing.T) {
		t.Parallel()

		var x *exampleStruct
		result, err := GetFieldTypes(x)
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
		test.Eq(t, "string", result["Field2"])
	})

	T.Run("with nil value", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(nil)
		test.Error(t, err)
		test.Nil(t, result)
	})

	T.Run("with non-struct value", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes("not a struct")
		test.Error(t, err)
		test.Nil(t, result)
	})

	T.Run("with reflect.Type directly", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(reflect.TypeFor[exampleStruct]())
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
		test.Eq(t, "string", result["Field2"])
	})

	T.Run("with reflect.Type of pointer", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(reflect.TypeFor[exampleStruct]())
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
		test.Eq(t, "string", result["Field2"])
	})

	T.Run("with nested struct", func(t *testing.T) {
		t.Parallel()

		x := nestedStruct{}
		result, err := GetFieldTypes(x)
		must.NoError(t, err)

		innerMap, ok := result["Inner"].(map[string]any)
		must.True(t, ok)
		test.Eq(t, "string", innerMap["Field1"])
		test.Eq(t, "string", innerMap["Field2"])

		innerPtrMap, ok := result["InnerPtr"].(map[string]any)
		must.True(t, ok)
		test.Eq(t, "string", innerPtrMap["Field1"])

		test.Eq(t, "string", result["Name"])
	})

	T.Run("with unexported fields skipped", func(t *testing.T) {
		t.Parallel()

		x := unexportedFieldStruct{Exported: "val"}
		result, err := GetFieldTypes(x)
		must.NoError(t, err)

		test.Eq(t, "string", result["Exported"])
		_, hasUnexported := result["unexported"]
		test.False(t, hasUnexported)
	})

	T.Run("with non-struct reflect.Type", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(reflect.TypeFor[string]())
		test.Error(t, err)
		test.Nil(t, result)
	})

	T.Run("with pointer reflect.Type", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(reflect.TypeFor[*exampleStruct]())
		must.NoError(t, err)

		test.Eq(t, "string", result["Field1"])
	})

	T.Run("with self-referential struct", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(selfReferentialNode{})
		must.NoError(t, err)

		test.Eq(t, "string", result["Name"])
		// The recursive field is recorded as its type name rather than recursing forever.
		test.Eq(t, "reflection.selfReferentialNode", result["Next"])
	})

	T.Run("with mutually recursive structs", func(t *testing.T) {
		t.Parallel()

		result, err := GetFieldTypes(mutuallyRecursiveA{})
		must.NoError(t, err)

		test.Eq(t, "string", result["Name"])

		bMap, ok := result["B"].(map[string]any)
		must.True(t, ok)
		test.Eq(t, "string", bMap["Name"])
		test.Eq(t, "reflection.mutuallyRecursiveA", bMap["A"])
	})
}
