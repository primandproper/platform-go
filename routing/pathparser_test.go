package routing

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestParsePath(T *testing.T) {
	T.Parallel()

	T.Run("strips type annotations and collects params", func(t *testing.T) {
		t.Parallel()

		plain, params := parsePath("/orgs/{orgID:uint64}/users/{slug:string}")

		test.EqOp(t, "/orgs/{orgID}/users/{slug}", plain)
		must.SliceLen(t, 2, params)
		test.EqOp(t, "orgID", params[0].Name)
		test.EqOp(t, "uint64", params[0].Token)
		test.EqOp(t, "slug", params[1].Name)
		test.EqOp(t, "string", params[1].Token)
	})

	T.Run("defaults an un-annotated param to string", func(t *testing.T) {
		t.Parallel()

		plain, params := parsePath("/things/{id}")

		test.EqOp(t, "/things/{id}", plain)
		must.SliceLen(t, 1, params)
		test.EqOp(t, "string", params[0].Token)
	})

	T.Run("panics on an unknown token", func(t *testing.T) {
		t.Parallel()

		defer func() {
			test.NotNil(t, recover())
		}()

		parsePath("/x/{id:frobnicate}")
	})
}

func TestTokenMatchesType(T *testing.T) {
	T.Parallel()

	cases := []struct {
		typ   reflect.Type
		name  string
		token string
		want  bool
	}{
		{name: "uint64 to uint64", token: "uint64", typ: reflect.TypeFor[uint64](), want: true},
		{name: "uint64 to string mismatch", token: "uint64", typ: reflect.TypeFor[string](), want: false},
		{name: "string to string", token: "string", typ: reflect.TypeFor[string](), want: true},
		{name: "bool to bool", token: "bool", typ: reflect.TypeFor[bool](), want: true},
		{name: "int to int", token: "int", typ: reflect.TypeFor[int](), want: true},
		{name: "float to float64", token: "float", typ: reflect.TypeFor[float64](), want: true},
		{name: "uuid to uuid.UUID via TextUnmarshaler", token: "uuid", typ: reflect.TypeFor[uuid.UUID](), want: true},
		{name: "string to time.Time via TextUnmarshaler", token: "string", typ: reflect.TypeFor[time.Time](), want: true},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.want, tokenMatchesType(tc.token, tc.typ))
		})
	}
}

func TestSetScalar(T *testing.T) {
	T.Parallel()

	T.Run("string", func(t *testing.T) {
		t.Parallel()
		var s string
		must.NoError(t, setScalar(reflect.ValueOf(&s).Elem(), "hello"))
		test.EqOp(t, "hello", s)
	})

	T.Run("uint64", func(t *testing.T) {
		t.Parallel()
		var n uint64
		must.NoError(t, setScalar(reflect.ValueOf(&n).Elem(), "42"))
		test.EqOp(t, uint64(42), n)
	})

	T.Run("bool", func(t *testing.T) {
		t.Parallel()
		var b bool
		must.NoError(t, setScalar(reflect.ValueOf(&b).Elem(), "true"))
		test.True(t, b)
	})

	T.Run("float64", func(t *testing.T) {
		t.Parallel()
		var f float64
		must.NoError(t, setScalar(reflect.ValueOf(&f).Elem(), "3.5"))
		test.EqOp(t, 3.5, f)
	})

	T.Run("uuid via TextUnmarshaler", func(t *testing.T) {
		t.Parallel()
		var id uuid.UUID
		expected := uuid.New()
		must.NoError(t, setScalar(reflect.ValueOf(&id).Elem(), expected.String()))
		test.EqOp(t, expected, id)
	})

	T.Run("invalid uint returns error", func(t *testing.T) {
		t.Parallel()
		var n uint64
		test.Error(t, setScalar(reflect.ValueOf(&n).Elem(), "not-a-number"))
	})
}
