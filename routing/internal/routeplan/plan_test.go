package routeplan

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type pagingParams struct {
	Page int `query:"page"`
}

type nestedInput struct {
	*pagingParams

	Name string `json:"name"`
	ID   uint64 `path:"id"`
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("collects params through an embedded struct", func(t *testing.T) {
		t.Parallel()

		plan, err := New(reflect.TypeFor[nestedInput](), []ParamSpec{{Name: "id", Token: "uint64"}}, http.MethodPost)
		must.NoError(t, err)

		test.True(t, plan.HasBody)
		test.True(t, plan.SendsBody())

		_, ok := plan.Find(InQuery, "page")
		test.True(t, ok)
		_, ok = plan.Find(InPath, "id")
		test.True(t, ok)
	})

	T.Run("a GET carries no body even when the input has body fields", func(t *testing.T) {
		t.Parallel()

		plan, err := New(reflect.TypeFor[nestedInput](), []ParamSpec{{Name: "id", Token: "uint64"}}, http.MethodGet)
		must.NoError(t, err)

		test.True(t, plan.HasBody)
		test.False(t, plan.SendsBody())
	})

	T.Run("reports a path param with no matching field", func(t *testing.T) {
		t.Parallel()

		_, err := New(reflect.TypeFor[nestedInput](), []ParamSpec{{Name: "missing", Token: "string"}}, http.MethodGet)
		test.Error(t, err)
	})

	T.Run("reports a path param whose token cannot hold the field", func(t *testing.T) {
		t.Parallel()

		_, err := New(reflect.TypeFor[nestedInput](), []ParamSpec{{Name: "id", Token: "string"}}, http.MethodGet)
		test.Error(t, err)
	})
}

func TestParamField_Value(T *testing.T) {
	T.Parallel()

	plan, err := New(reflect.TypeFor[nestedInput](), nil, http.MethodPost)
	must.NoError(T, err)

	page, ok := plan.Find(InQuery, "page")
	must.True(T, ok)

	T.Run("reads a field promoted through an embedded pointer", func(t *testing.T) {
		t.Parallel()

		in := nestedInput{pagingParams: &pagingParams{Page: 3}}

		v, reachable := page.Value(reflect.ValueOf(in))
		must.True(t, reachable)
		test.EqOp(t, 3, int(v.Int()))
	})

	// FieldByIndex would panic here. A destination being populated wants that
	// panic; a source being read wants to be told the field is not there.
	T.Run("reports a field behind a nil embedded pointer as unreachable", func(t *testing.T) {
		t.Parallel()

		_, reachable := page.Value(reflect.ValueOf(nestedInput{}))
		test.False(t, reachable)
	})

	T.Run("walks through a pointer to the input itself", func(t *testing.T) {
		t.Parallel()

		in := &nestedInput{pagingParams: &pagingParams{Page: 9}}

		v, reachable := page.Value(reflect.ValueOf(in))
		must.True(t, reachable)
		test.EqOp(t, 9, int(v.Int()))
	})
}

// PagingParams is embedded by pointer under an exported name, so the pointer can
// be replaced and the sharing severed.
type PagingParams struct {
	Page int `query:"page"`
}

type detachableInput struct {
	*PagingParams

	Name string `json:"name"`
}

func TestParamField_Detach(T *testing.T) {
	T.Parallel()

	T.Run("writing through it leaves the original alone", func(t *testing.T) {
		t.Parallel()

		plan, err := New(reflect.TypeFor[detachableInput](), nil, http.MethodPost)
		must.NoError(t, err)

		page, ok := plan.Find(InQuery, "page")
		must.True(t, ok)

		shared := &PagingParams{Page: 4}
		original := detachableInput{PagingParams: shared, Name: "x"}

		clone := reflect.New(reflect.TypeFor[detachableInput]())
		clone.Elem().Set(reflect.ValueOf(original))

		field, detached := page.Detach(clone.Elem())
		must.True(t, detached)
		must.True(t, field.CanSet())
		field.SetZero()

		test.EqOp(t, 0, clone.Elem().Interface().(detachableInput).Page)
		test.EqOp(t, 4, original.Page)
		test.EqOp(t, 4, shared.Page)
	})

	// nestedInput embeds *pagingParams under an unexported name, so reflect will
	// not let the pointer be reassigned and the sharing cannot be severed.
	T.Run("refuses a pointer it cannot replace", func(t *testing.T) {
		t.Parallel()

		plan, err := New(reflect.TypeFor[nestedInput](), nil, http.MethodPost)
		must.NoError(t, err)

		page, ok := plan.Find(InQuery, "page")
		must.True(t, ok)

		clone := reflect.New(reflect.TypeFor[nestedInput]())
		clone.Elem().Set(reflect.ValueOf(nestedInput{pagingParams: &pagingParams{Page: 4}}))

		_, detached := page.Detach(clone.Elem())
		test.False(t, detached)
	})

	T.Run("reports a nil pointer as nothing to detach", func(t *testing.T) {
		t.Parallel()

		plan, err := New(reflect.TypeFor[detachableInput](), nil, http.MethodPost)
		must.NoError(t, err)

		page, ok := plan.Find(InQuery, "page")
		must.True(t, ok)

		clone := reflect.New(reflect.TypeFor[detachableInput]())

		_, detached := page.Detach(clone.Elem())
		test.False(t, detached)
	})
}
