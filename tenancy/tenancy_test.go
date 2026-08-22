package tenancy

import (
	"encoding/json"
	"errors"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOf(t *testing.T) {
	t.Parallel()

	t.Run("names the owner", func(t *testing.T) {
		t.Parallel()

		scope := Of("acct_1")

		must.NoError(t, scope.Validate())
		test.EqOp(t, "acct_1", scope.Owner())
		test.False(t, scope.IsGlobal())
	})

	t.Run("an empty identifier names nobody", func(t *testing.T) {
		t.Parallel()

		// The whole point of the type: a lookup that came back empty must not
		// become a read of the global scope.
		scope := Of("")

		must.ErrorIs(t, scope.Validate(), ErrNoScope)
		test.False(t, scope.IsGlobal())
	})
}

func TestGlobal(t *testing.T) {
	t.Parallel()

	t.Run("is valid, owned by nobody", func(t *testing.T) {
		t.Parallel()

		scope := Global()

		must.NoError(t, scope.Validate())
		test.True(t, scope.IsGlobal())
		test.EqOp(t, "", scope.Owner())
	})

	t.Run("is not the zero value", func(t *testing.T) {
		t.Parallel()

		// If these were one value every forgotten scope would read the global
		// one, which is the silent widening this type exists to prevent.
		test.NotEqOp(t, Scope{}, Global())
	})

	t.Run("matches only itself", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Global(), Global())
		test.NotEqOp(t, Global(), Of("acct_1"))
	})
}

func TestScope_Validate(t *testing.T) {
	t.Parallel()

	t.Run("the zero value is refused", func(t *testing.T) {
		t.Parallel()

		var scope Scope

		err := scope.Validate()
		must.ErrorIs(t, err, ErrNoScope)
		// Callers may check either the specific sentinel or the platform one.
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}

func TestScope_Equality(t *testing.T) {
	t.Parallel()

	t.Run("same owner is the same scope", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Of("acct_1"), Of("acct_1"))
		test.NotEqOp(t, Of("acct_1"), Of("acct_2"))
	})

	t.Run("is usable as a map key", func(t *testing.T) {
		t.Parallel()

		counts := map[Scope]int{}
		counts[Of("acct_1")]++
		counts[Of("acct_1")]++
		counts[Global()]++

		test.EqOp(t, 2, counts[Of("acct_1")])
		test.EqOp(t, 1, counts[Global()])
		test.MapLen(t, 2, counts)
	})
}

func TestFromOwner(t *testing.T) {
	t.Parallel()

	t.Run("round trips an owned scope", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Of("acct_1"), FromOwner(Of("acct_1").Owner()))
	})

	t.Run("the empty identifier is the global scope", func(t *testing.T) {
		t.Parallel()

		// Unlike Of: this arrived from a column that was written, not from a
		// caller whose lookup may have failed.
		test.EqOp(t, Global(), FromOwner(""))
	})
}

func TestScope_String(t *testing.T) {
	t.Parallel()

	t.Run("renders each state distinguishably", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "<unset>", Scope{}.String())
		test.EqOp(t, "<global>", Global().String())
		test.EqOp(t, "acct_1", Of("acct_1").String())
	})

	t.Run("an owner named global is still distinguishable", func(t *testing.T) {
		t.Parallel()

		test.NotEqOp(t, Global().String(), Of("global").String())
	})
}

func TestScope_Value(t *testing.T) {
	t.Parallel()

	t.Run("binds the owner identifier", func(t *testing.T) {
		t.Parallel()

		value, err := Of("acct_1").Value()
		must.NoError(t, err)
		test.EqOp(t, "acct_1", value)
	})

	t.Run("global binds the empty identifier", func(t *testing.T) {
		t.Parallel()

		// Which is what makes a scope column default to the global scope.
		value, err := Global().Value()
		must.NoError(t, err)
		test.EqOp(t, "", value)
	})

	t.Run("an unset scope refuses to bind", func(t *testing.T) {
		t.Parallel()

		// A predicate that lost its scope has to fail at the driver rather than
		// widen to the global scope's rows.
		value, err := Scope{}.Value()
		must.ErrorIs(t, err, ErrNoScope)
		test.Nil(t, value)
	})
}

func TestScope_Scan(t *testing.T) {
	t.Parallel()

	t.Run("reads a string column", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		must.NoError(t, scope.Scan("acct_1"))
		test.EqOp(t, Of("acct_1"), scope)
	})

	t.Run("reads a bytes column", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		must.NoError(t, scope.Scan([]byte("acct_1")))
		test.EqOp(t, Of("acct_1"), scope)
	})

	t.Run("the empty column value is the global scope", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		must.NoError(t, scope.Scan(""))
		test.EqOp(t, Global(), scope)

		must.NoError(t, scope.Scan([]byte{}))
		test.EqOp(t, Global(), scope)
	})

	t.Run("a NULL is refused", func(t *testing.T) {
		t.Parallel()

		// The documented column is NOT NULL: a NULL means the schema is not the
		// one the queries were written against.
		var scope Scope
		must.ErrorIs(t, scope.Scan(nil), ErrUnscannableScope)
		must.ErrorIs(t, scope.Validate(), ErrNoScope)
	})

	t.Run("an unexpected type is refused", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		must.ErrorIs(t, scope.Scan(42), ErrUnscannableScope)
	})
}

func TestScope_JSON(t *testing.T) {
	t.Parallel()

	t.Run("renders the owner identifier", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(Of("acct_1"))
		must.NoError(t, err)
		test.EqOp(t, `"acct_1"`, string(encoded))
	})

	t.Run("global renders as the empty identifier", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(Global())
		must.NoError(t, err)
		test.EqOp(t, `""`, string(encoded))
	})

	t.Run("an unset scope renders as null", func(t *testing.T) {
		t.Parallel()

		// Not "": that is Global's spelling, and conflating them makes a client
		// that omitted the field look like one that asked for the global scope.
		encoded, err := json.Marshal(Scope{})
		must.NoError(t, err)
		test.EqOp(t, "null", string(encoded))
	})

	t.Run("round trips every state", func(t *testing.T) {
		t.Parallel()

		for _, scope := range []Scope{Of("acct_1"), Global(), {}} {
			encoded, err := json.Marshal(scope)
			must.NoError(t, err)

			var decoded Scope
			must.NoError(t, json.Unmarshal(encoded, &decoded))
			test.EqOp(t, scope, decoded, test.Sprintf("round tripping %s", scope))
		}
	})

	t.Run("an absent field is the unset scope", func(t *testing.T) {
		t.Parallel()

		var payload struct {
			Scope Scope `json:"scope"`
		}

		must.NoError(t, json.Unmarshal([]byte(`{}`), &payload))
		must.ErrorIs(t, payload.Scope.Validate(), ErrNoScope)
	})

	t.Run("an explicit empty string is the global scope", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		must.NoError(t, json.Unmarshal([]byte(`""`), &scope))
		test.EqOp(t, Global(), scope)
	})

	t.Run("a non-string is refused", func(t *testing.T) {
		t.Parallel()

		var scope Scope
		err := json.Unmarshal([]byte(`42`), &scope)
		must.Error(t, err)
		test.False(t, errors.Is(err, ErrNoScope))
	})
}
