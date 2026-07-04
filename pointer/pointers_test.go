package pointer

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestTo(T *testing.T) {
	T.Parallel()

	T.Run("with string", func(t *testing.T) {
		t.Parallel()

		expected := "things"
		actual := To(expected)

		must.NotNil(t, actual)
		test.EqOp(t, expected, *actual)
	})

	T.Run("with int", func(t *testing.T) {
		t.Parallel()

		expected := 42
		actual := To(expected)

		must.NotNil(t, actual)
		test.EqOp(t, expected, *actual)
	})

	T.Run("with zero value", func(t *testing.T) {
		t.Parallel()

		actual := To(0)

		must.NotNil(t, actual)
		test.EqOp(t, 0, *actual)
	})

	T.Run("with struct", func(t *testing.T) {
		t.Parallel()

		type example struct{ Name string }
		expected := example{Name: "test"}
		actual := To(expected)

		must.NotNil(t, actual)
		test.EqOp(t, expected, *actual)
	})
}

func TestToSlice(T *testing.T) {
	T.Parallel()

	T.Run("with string slice", func(t *testing.T) {
		t.Parallel()

		input := []string{"a", "b", "c"}
		actual := ToSlice(input)

		must.SliceLen(t, 3, actual)
		test.EqOp(t, "a", *actual[0])
		test.EqOp(t, "b", *actual[1])
		test.EqOp(t, "c", *actual[2])
	})

	T.Run("with int slice", func(t *testing.T) {
		t.Parallel()

		input := []int{1, 2, 3}
		actual := ToSlice(input)

		must.SliceLen(t, 3, actual)
		test.EqOp(t, 1, *actual[0])
		test.EqOp(t, 2, *actual[1])
		test.EqOp(t, 3, *actual[2])
	})

	T.Run("with nil slice", func(t *testing.T) {
		t.Parallel()

		actual := ToSlice[string](nil)

		test.NotNil(t, actual)
		test.SliceEmpty(t, actual)
	})

	T.Run("with empty slice", func(t *testing.T) {
		t.Parallel()

		actual := ToSlice([]string{})

		test.NotNil(t, actual)
		test.SliceEmpty(t, actual)
	})
}

func TestDereference(T *testing.T) {
	T.Parallel()

	T.Run("with string pointer", func(t *testing.T) {
		t.Parallel()

		rawExpected := "things"
		actual := Dereference(&rawExpected)

		test.EqOp(t, rawExpected, actual)
	})

	T.Run("with int pointer", func(t *testing.T) {
		t.Parallel()

		expected := 42
		actual := Dereference(&expected)

		test.EqOp(t, 42, actual)
	})

	T.Run("with nil string pointer", func(t *testing.T) {
		t.Parallel()

		actual := Dereference[string](nil)

		test.EqOp(t, "", actual)
	})

	T.Run("with nil int pointer", func(t *testing.T) {
		t.Parallel()

		actual := Dereference[int](nil)

		test.EqOp(t, 0, actual)
	})

	T.Run("with nil bool pointer", func(t *testing.T) {
		t.Parallel()

		actual := Dereference[bool](nil)

		test.False(t, actual)
	})
}

func TestDereferenceSlice(T *testing.T) {
	T.Parallel()

	T.Run("with string pointer slice", func(t *testing.T) {
		t.Parallel()

		a, b, c := "a", "b", "c"
		input := []*string{&a, &b, &c}
		actual := DereferenceSlice(input)

		must.SliceLen(t, 3, actual)
		test.EqOp(t, "a", actual[0])
		test.EqOp(t, "b", actual[1])
		test.EqOp(t, "c", actual[2])
	})

	T.Run("with int pointer slice", func(t *testing.T) {
		t.Parallel()

		a, b := 1, 2
		input := []*int{&a, &b}
		actual := DereferenceSlice(input)

		must.SliceLen(t, 2, actual)
		test.EqOp(t, 1, actual[0])
		test.EqOp(t, 2, actual[1])
	})

	T.Run("with a nil element zero-fills instead of panicking", func(t *testing.T) {
		t.Parallel()

		a, c := "a", "c"
		input := []*string{&a, nil, &c}
		actual := DereferenceSlice(input)

		must.SliceLen(t, 3, actual)
		test.EqOp(t, "a", actual[0])
		test.EqOp(t, "", actual[1])
		test.EqOp(t, "c", actual[2])
	})

	T.Run("with nil slice", func(t *testing.T) {
		t.Parallel()

		actual := DereferenceSlice[string](nil)

		test.NotNil(t, actual)
		test.SliceEmpty(t, actual)
	})

	T.Run("with empty slice", func(t *testing.T) {
		t.Parallel()

		actual := DereferenceSlice([]*string{})

		test.NotNil(t, actual)
		test.SliceEmpty(t, actual)
	})
}
