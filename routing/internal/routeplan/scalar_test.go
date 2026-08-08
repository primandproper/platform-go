package routeplan

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSetScalar(T *testing.T) {
	T.Parallel()

	T.Run("string", func(t *testing.T) {
		t.Parallel()
		var s string
		must.NoError(t, SetScalar(reflect.ValueOf(&s).Elem(), "hello"))
		test.EqOp(t, "hello", s)
	})

	T.Run("int", func(t *testing.T) {
		t.Parallel()
		var n int
		must.NoError(t, SetScalar(reflect.ValueOf(&n).Elem(), "-5"))
		test.EqOp(t, -5, n)
	})

	T.Run("uint64", func(t *testing.T) {
		t.Parallel()
		var n uint64
		must.NoError(t, SetScalar(reflect.ValueOf(&n).Elem(), "42"))
		test.EqOp(t, uint64(42), n)
	})

	T.Run("bool", func(t *testing.T) {
		t.Parallel()
		var b bool
		must.NoError(t, SetScalar(reflect.ValueOf(&b).Elem(), "true"))
		test.True(t, b)
	})

	T.Run("float64", func(t *testing.T) {
		t.Parallel()
		var f float64
		must.NoError(t, SetScalar(reflect.ValueOf(&f).Elem(), "3.5"))
		test.EqOp(t, 3.5, f)
	})

	T.Run("uuid via TextUnmarshaler", func(t *testing.T) {
		t.Parallel()
		var id uuid.UUID
		expected := uuid.New()
		must.NoError(t, SetScalar(reflect.ValueOf(&id).Elem(), expected.String()))
		test.EqOp(t, expected, id)
	})

	T.Run("pointer allocates", func(t *testing.T) {
		t.Parallel()
		var p *int
		must.NoError(t, SetScalar(reflect.ValueOf(&p).Elem(), "9"))
		must.NotNil(t, p)
		test.EqOp(t, 9, *p)
	})

	T.Run("invalid uint returns error", func(t *testing.T) {
		t.Parallel()
		var n uint64
		test.Error(t, SetScalar(reflect.ValueOf(&n).Elem(), "not-a-number"))
	})

	T.Run("unsupported kind errors", func(t *testing.T) {
		t.Parallel()
		var c complex128
		test.Error(t, SetScalar(reflect.ValueOf(&c).Elem(), "1"))
	})

	T.Run("bad bool errors", func(t *testing.T) {
		t.Parallel()
		var b bool
		test.Error(t, SetScalar(reflect.ValueOf(&b).Elem(), "notbool"))
	})

	T.Run("bad float errors", func(t *testing.T) {
		t.Parallel()
		var f float64
		test.Error(t, SetScalar(reflect.ValueOf(&f).Elem(), "notfloat"))
	})

	T.Run("bad int errors", func(t *testing.T) {
		t.Parallel()
		var n int
		test.Error(t, SetScalar(reflect.ValueOf(&n).Elem(), "notint"))
	})
}

func Test_SetScalar_rejectsOverflow(T *testing.T) {
	T.Parallel()

	// Parsing wide and narrowing via SetInt wraps silently: ?count=300 into an
	// int8 bound to 44, and the handler saw a plausible number instead of the
	// 400 the request had earned.
	T.Run("int8", func(t *testing.T) {
		t.Parallel()

		var v int8
		test.Error(t, SetScalar(reflect.ValueOf(&v).Elem(), "300"))
		test.EqOp(t, int8(0), v)
	})

	T.Run("uint8", func(t *testing.T) {
		t.Parallel()

		var v uint8
		test.Error(t, SetScalar(reflect.ValueOf(&v).Elem(), "300"))
	})

	T.Run("int16", func(t *testing.T) {
		t.Parallel()

		var v int16
		test.Error(t, SetScalar(reflect.ValueOf(&v).Elem(), "40000"))
	})

	T.Run("still accepts an in-range value", func(t *testing.T) {
		t.Parallel()

		var v int8
		test.NoError(t, SetScalar(reflect.ValueOf(&v).Elem(), "100"))
		test.EqOp(t, int8(100), v)
	})
}

// TestFormatScalar_RoundTrip is the property the client's correctness rests on:
// whatever SetScalar can read out of a request, FormatScalar can put back into
// one, and the value survives the trip unchanged.
func TestFormatScalar_RoundTrip(T *testing.T) {
	T.Parallel()

	id := uuid.New()
	stamp := time.Date(2026, time.August, 8, 12, 30, 0, 0, time.UTC)

	cases := []struct {
		value any
		name  string
		text  string
	}{
		{name: "string", value: "hello", text: "hello"},
		{name: "bool", value: true, text: "true"},
		{name: "int", value: -5, text: "-5"},
		{name: "int8", value: int8(100), text: "100"},
		{name: "uint64", value: uint64(42), text: "42"},
		{name: "float64", value: 3.5, text: "3.5"},
		{name: "float32", value: float32(1.5), text: "1.5"},
		{name: "uuid via TextMarshaler", value: id, text: id.String()},
		{name: "time via TextMarshaler", value: stamp, text: "2026-08-08T12:30:00Z"},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text, present, err := FormatScalar(reflect.ValueOf(tc.value))
			must.NoError(t, err)
			test.True(t, present)
			test.EqOp(t, tc.text, text)

			back := reflect.New(reflect.TypeOf(tc.value))
			must.NoError(t, SetScalar(back.Elem(), text))
			test.Eq(t, tc.value, back.Elem().Interface())
		})
	}
}

func TestFormatScalar_Pointers(T *testing.T) {
	T.Parallel()

	T.Run("a nil pointer is absent, not empty", func(t *testing.T) {
		t.Parallel()

		var p *int

		text, present, err := FormatScalar(reflect.ValueOf(p))
		must.NoError(t, err)
		test.False(t, present)
		test.EqOp(t, "", text)
	})

	T.Run("a set pointer formats its pointee", func(t *testing.T) {
		t.Parallel()

		n := 7

		text, present, err := FormatScalar(reflect.ValueOf(&n))
		must.NoError(t, err)
		test.True(t, present)
		test.EqOp(t, "7", text)
	})

	T.Run("a zero value is still sent", func(t *testing.T) {
		t.Parallel()

		text, present, err := FormatScalar(reflect.ValueOf(0))
		must.NoError(t, err)
		test.True(t, present)
		test.EqOp(t, "0", text)
	})
}

// pointerMarshaler declares MarshalText on the pointer receiver, so formatting a
// non-addressable value of it only works through the addressable copy.
type pointerMarshaler struct {
	Word string
}

func (p *pointerMarshaler) MarshalText() ([]byte, error) { return []byte(p.Word), nil }

func TestFormatScalar_PointerReceiverMarshaler(T *testing.T) {
	T.Parallel()

	text, present, err := FormatScalar(reflect.ValueOf(pointerMarshaler{Word: "ok"}))
	must.NoError(T, err)
	test.True(T, present)
	test.EqOp(T, "ok", text)
}

func TestFormatScalar_UnsupportedKind(T *testing.T) {
	T.Parallel()

	_, _, err := FormatScalar(reflect.ValueOf(complex128(1)))
	test.Error(T, err)
}
