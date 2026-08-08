package routeplan

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

		plain, params, err := ParsePath("/orgs/{orgID:uint64}/users/{slug:string}")

		must.NoError(t, err)
		test.EqOp(t, "/orgs/{orgID}/users/{slug}", plain)
		must.SliceLen(t, 2, params)
		test.EqOp(t, "orgID", params[0].Name)
		test.EqOp(t, "uint64", params[0].Token)
		test.EqOp(t, "slug", params[1].Name)
		test.EqOp(t, "string", params[1].Token)
	})

	T.Run("defaults an un-annotated param to string", func(t *testing.T) {
		t.Parallel()

		plain, params, err := ParsePath("/things/{id}")

		must.NoError(t, err)
		test.EqOp(t, "/things/{id}", plain)
		must.SliceLen(t, 1, params)
		test.EqOp(t, "string", params[0].Token)
	})

	T.Run("reports an unknown token", func(t *testing.T) {
		t.Parallel()

		plain, params, err := ParsePath("/x/{id:frobnicate}")

		test.Error(t, err)
		test.EqOp(t, "", plain)
		test.SliceEmpty(t, params)
	})
}

func TestTokenMatchesType_Rejections(T *testing.T) {
	T.Parallel()

	test.False(T, TokenMatchesType("bogus", reflect.TypeFor[int]()))
	test.False(T, TokenMatchesType("int", reflect.TypeFor[string]()))
	test.False(T, TokenMatchesType("float", reflect.TypeFor[string]()))
	test.False(T, TokenMatchesType("uint64", reflect.TypeFor[string]()))
	// double pointer exercises the deref loop more than once.
	test.True(T, TokenMatchesType("int", reflect.TypeFor[**int]()))
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

			test.EqOp(t, tc.want, TokenMatchesType(tc.token, tc.typ))
		})
	}
}
