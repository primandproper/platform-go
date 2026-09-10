package audit

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type diffAddress struct {
	City   string `json:"city"`
	Postal string `json:"postal"`
}

type diffAudited struct {
	CreatedAt  time.Time   `json:"createdAt"`
	Address    diffAddress `json:"address"`
	Name       string      `json:"name"`
	Secret     string      `json:"-"`
	Internal   string      `audit:"-"        json:"internal"`
	Untagged   string
	unexported string // present precisely so the walk has one to skip
	Count      int    `json:"count"`
	Active     bool   `json:"active"`
}

type diffBase struct {
	Version int `json:"version"`
}

type diffEmbedded struct {
	Name string `json:"name"`

	diffBase
}

type diffPointerEmbedded struct {
	*diffBase

	Name string `json:"name"`
}

// DiffVersion is embedded by value and by pointer below, to cover the two
// shapes of embedded non-struct that must not be flattened. It is exported
// because an embedded field is named for its type, and an unexported one would
// be skipped before the flattening decision is ever reached — which is itself
// correct, and what encoding/json does.
type DiffVersion int

type diffEmbeddedScalar struct {
	DiffVersion
}

type diffEmbeddedScalarPtr struct {
	*DiffVersion
}

type diffDashNamed struct {
	// json:"-," names the field "-", where json:"-" would omit it. staticcheck
	// flags the spelling as a likely typo, which it usually is — here it is the
	// distinction under test, and Diff has to honor it or the audit log and the
	// encoded resource disagree about which fields exist.
	//
	//nolint:staticcheck // SA5008: naming a field "-" is deliberate here.
	Dash string `json:"-,"`
}

// diffOptionsOnly carries a json tag that renames nothing, which encoding/json
// reads as "keep the field name, apply the options".
type diffOptionsOnly struct {
	Name string `json:",omitempty"`
}

func TestDiff(T *testing.T) {
	T.Parallel()

	T.Run("reports only the fields that changed", func(t *testing.T) {
		t.Parallel()

		before := diffAudited{Name: "old", Count: 1, Active: true}
		after := diffAudited{Name: "new", Count: 1, Active: true}

		changes, err := Diff(before, after)
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.EqOp(t, "old", changes["name"].Old)
		test.EqOp(t, "new", changes["name"].New)
	})

	T.Run("names fields by their json tag, falling back to the field name", func(t *testing.T) {
		t.Parallel()

		changes, err := Diff(diffAudited{}, diffAudited{Count: 2, Untagged: "x"})
		must.NoError(t, err)
		must.MapLen(t, 2, changes)
		test.MapContainsKey(t, changes, "count")
		test.MapContainsKey(t, changes, "Untagged")
	})

	T.Run("skips fields excluded by tag", func(t *testing.T) {
		t.Parallel()

		before := diffAudited{Secret: "hunter2", Internal: "a"}
		after := diffAudited{Secret: "hunter3", Internal: "b"}

		changes, err := Diff(before, after)
		must.NoError(t, err)
		test.MapEmpty(t, changes)
	})

	T.Run("accepts pointers", func(t *testing.T) {
		t.Parallel()

		changes, err := Diff(&diffAudited{Name: "a"}, &diffAudited{Name: "b"})
		must.NoError(t, err)
		test.MapLen(t, 1, changes)
	})

	T.Run("records a creation as additions with no old value", func(t *testing.T) {
		t.Parallel()

		changes, err := Diff(nil, diffAudited{Name: "new", Count: 3})
		must.NoError(t, err)
		must.MapLen(t, 2, changes)
		test.Nil(t, changes["name"].Old)
		test.EqOp(t, "new", changes["name"].New)
	})

	T.Run("records a deletion as removals with no new value", func(t *testing.T) {
		t.Parallel()

		changes, err := Diff(diffAudited{Name: "gone"}, nil)
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.EqOp(t, "gone", changes["name"].Old)
		test.Nil(t, changes["name"].New)
	})

	T.Run("treats a nil pointer as an absent side", func(t *testing.T) {
		t.Parallel()

		var before *diffAudited

		changes, err := Diff(before, &diffAudited{Name: "new"})
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.Nil(t, changes["name"].Old)
	})

	T.Run("records a changed nested struct whole", func(t *testing.T) {
		t.Parallel()

		before := diffAudited{Address: diffAddress{City: "Springfield", Postal: "62701"}}
		after := diffAudited{Address: diffAddress{City: "Shelbyville", Postal: "62701"}}

		changes, err := Diff(before, after)
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.Eq(t, any(diffAddress{City: "Springfield", Postal: "62701"}), changes["address"].Old)
	})

	T.Run("flattens embedded structs", func(t *testing.T) {
		t.Parallel()

		before := diffEmbedded{Version: 1, Name: "a"}
		after := diffEmbedded{Version: 2, Name: "a"}

		changes, err := Diff(before, after)
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.EqOp(t, 1, changes["version"].Old)
		test.EqOp(t, 2, changes["version"].New)
	})

	T.Run("flattens an embedded pointer, nil included", func(t *testing.T) {
		t.Parallel()

		before := diffPointerEmbedded{Name: "a"}
		after := diffPointerEmbedded{diffBase: &diffBase{Version: 3}, Name: "a"}

		changes, err := Diff(before, after)
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.EqOp(t, 0, changes["version"].Old)
		test.EqOp(t, 3, changes["version"].New)
	})

	T.Run("records an embedded non-struct as an ordinary field", func(t *testing.T) {
		t.Parallel()

		// encoding/json promotes an embedded struct's fields and treats an
		// embedded non-struct as a field named for its type. Diff follows,
		// because the audit log and the encoded resource must not disagree
		// about which fields exist.
		changes, err := Diff(diffEmbeddedScalar{DiffVersion: 1}, diffEmbeddedScalar{DiffVersion: 2})
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.MapContainsKey(t, changes, "DiffVersion")
	})

	T.Run("records an embedded pointer to a non-struct as a field", func(t *testing.T) {
		t.Parallel()

		one, two := DiffVersion(1), DiffVersion(2)

		changes, err := Diff(diffEmbeddedScalarPtr{&one}, diffEmbeddedScalarPtr{&two})
		must.NoError(t, err)
		test.MapLen(t, 1, changes)
	})

	T.Run("honors a field literally named dash", func(t *testing.T) {
		t.Parallel()

		// json:"-," names the field "-"; json:"-" omits it. The distinction is
		// encoding/json's and it is preserved here.
		changes, err := Diff(diffDashNamed{Dash: "a"}, diffDashNamed{Dash: "b"})
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.MapContainsKey(t, changes, "-")
	})

	T.Run("treats a typed nil interface as an absent side", func(t *testing.T) {
		t.Parallel()

		var absent any

		changes, err := Diff(absent, diffAudited{Name: "new"})
		must.NoError(t, err)
		test.MapLen(t, 1, changes)
	})

	T.Run("returns nothing for identical values", func(t *testing.T) {
		t.Parallel()

		value := diffAudited{Name: "same", Count: 7}

		changes, err := Diff(value, value)
		must.NoError(t, err)

		// Empty rather than nil, so it can be assigned to an Entry without a
		// check at the call site.
		test.MapEmpty(t, changes)
		test.NotNil(t, changes)
	})

	T.Run("refuses two different types", func(t *testing.T) {
		t.Parallel()

		_, err := Diff(diffAudited{}, diffAddress{})
		test.ErrorIs(t, err, ErrDiffTypeMismatch)
	})

	T.Run("refuses a non-struct on either side", func(t *testing.T) {
		t.Parallel()

		_, err := Diff("a", "b")
		test.ErrorIs(t, err, ErrNotAStruct)

		_, err = Diff(diffAudited{}, "b")
		test.ErrorIs(t, err, ErrNotAStruct)
	})

	T.Run("names a field by its own name when the tag carries only options", func(t *testing.T) {
		t.Parallel()

		changes, err := Diff(diffOptionsOnly{Name: "a"}, diffOptionsOnly{Name: "b"})
		must.NoError(t, err)
		must.MapLen(t, 1, changes)
		test.MapContainsKey(t, changes, "Name")
	})

	T.Run("refuses two absent sides", func(t *testing.T) {
		t.Parallel()

		_, err := Diff(nil, nil)
		test.ErrorIs(t, err, ErrNothingToDiff)
	})
}

func TestDiff_RoundTripsThroughAnEntry(T *testing.T) {
	T.Parallel()

	client := newTestClient(T)
	recorder := newTestRecorder(T, newStubClock())
	reader := newTestReader(T, client)

	changes, err := Diff(
		diffAudited{Name: "old", Count: 1},
		diffAudited{Name: "new", Count: 2},
	)
	must.NoError(T, err)

	entry := &Entry{
		EventType:    EventUpdated,
		ResourceType: "audited",
		Scope:        tenancy.Global(),
		ResourceID:   "a1",
		Actor:        Actor{ID: "user_1", Type: ActorUser},
		Changes:      changes,
	}
	record(T, client, recorder, entry)

	read, err := reader.Get(T.Context(), entry.ID)
	must.NoError(T, err)
	must.MapLen(T, 2, read.Changes)

	// Numbers come back as JSON numbers rather than as the Go int they went in
	// as, which is why the chain hashes the stored bytes instead of re-encoding
	// what a read like this returns.
	test.EqOp(T, "old", read.Changes["name"].Old)
	test.Eq(T, any(float64(1)), read.Changes["count"].Old)
}
