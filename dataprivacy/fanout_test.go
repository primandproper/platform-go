package dataprivacy

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubTx is a non-nil database.Tx that is never called. The fan-out helpers
// hand the transaction straight to the caller's closure and read nothing off
// it, so an embedded nil interface is exactly as much transaction as these
// tests need — and a method call on one would panic rather than pass quietly.
type stubTx struct{ database.Tx }

// fanSubject is the subject these tests fan out over.
var fanSubject = Subject{ID: "subject-1", Type: SubjectUser}

// scopesOf resolves to a fixed list, spelled here rather than through
// FixedScopes so that a test of FixedScopes cannot pass by tautology.
func scopesOf(scopes ...tenancy.Scope) func(context.Context, tenancy.Scope, Subject) ([]tenancy.Scope, error) {
	return func(context.Context, tenancy.Scope, Subject) ([]tenancy.Scope, error) {
		return scopes, nil
	}
}

func TestForEachOwner(T *testing.T) {
	T.Parallel()

	T.Run("runs once per owner, in the order resolved", func(t *testing.T) {
		t.Parallel()

		var seen []tenancy.Scope

		err := ForEachOwner(t.Context(), scopesOf(tenancy.Of("b"), tenancy.Of("a")),
			tenancy.Scope{}, fanSubject,
			func(_ context.Context, scope tenancy.Scope) error {
				seen = append(seen, scope)

				return nil
			})
		must.NoError(t, err)

		// Resolved order, not sorted order. Sorting here would be this package
		// deciding something the resolver already decided.
		test.Eq(t, []tenancy.Scope{tenancy.Of("b"), tenancy.Of("a")}, seen)
	})

	T.Run("a repeated owner is visited twice", func(t *testing.T) {
		t.Parallel()

		// Deliberate. A resolver answering with the same scope twice has a bug,
		// and an export that shows its rows twice is how somebody finds out;
		// collapsing it here would hide the bug in the one artifact it is most
		// expensive to be wrong in.
		visits := 0

		err := ForEachOwner(t.Context(), scopesOf(tenancy.Of("a"), tenancy.Of("a")),
			tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error {
				visits++

				return nil
			})
		must.NoError(t, err)
		test.EqOp(t, 2, visits)
	})

	T.Run("no owners is not an error", func(t *testing.T) {
		t.Parallel()

		// A subject who holds nothing here resolves to no scopes, and that is an
		// answer rather than a failure.
		visits := 0

		err := ForEachOwner(t.Context(), scopesOf(), tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error {
				visits++

				return nil
			})
		must.NoError(t, err)
		test.EqOp(t, 0, visits)
	})

	T.Run("a resolver failure names the subject", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("directory unreachable")

		err := ForEachOwner(t.Context(),
			func(context.Context, tenancy.Scope, Subject) ([]tenancy.Scope, error) {
				return nil, boom
			},
			tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error { return nil })
		must.Error(t, err)
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), fanSubject.ID)
	})

	T.Run("an owner's failure passes through unwrapped", func(t *testing.T) {
		t.Parallel()

		// The caller's closure is where that domain's own words live — which
		// table, which verb, and whether it calls one of these a scope or a
		// registry. A second wrap here would name the same failure twice.
		boom := platformerrors.New("the store refused")

		err := ForEachOwner(t.Context(), scopesOf(tenancy.Of("a"), tenancy.Of("b")),
			tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error { return boom })
		must.Error(t, err)
		test.EqOp(t, boom.Error(), err.Error())
	})

	T.Run("the first failure stops the fan-out", func(t *testing.T) {
		t.Parallel()

		visits := 0

		err := ForEachOwner(t.Context(), scopesOf(tenancy.Of("a"), tenancy.Of("b")),
			tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error {
				visits++

				return platformerrors.New("refused")
			})
		must.Error(t, err)
		test.EqOp(t, 1, visits)
	})

	T.Run("a cancelled context stops between owners", func(t *testing.T) {
		t.Parallel()

		// An erasure whose deadline expired stops here rather than on the next
		// scope's write, which is the same reading CollectAll takes between
		// pages.
		ctx, cancel := context.WithCancel(t.Context())
		visits := 0

		err := ForEachOwner(ctx, scopesOf(tenancy.Of("a"), tenancy.Of("b"), tenancy.Of("c")),
			tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error {
				visits++
				cancel()

				return nil
			})
		must.Error(t, err)
		test.ErrorIs(t, err, context.Canceled)
		test.EqOp(t, 1, visits)
	})

	T.Run("the owner is not always a scope", func(t *testing.T) {
		t.Parallel()

		// The reason the helper is generic: billing/privacy fans out over an
		// account, which carries a scope and an id and whose halves are not
		// inferable from one another. A helper fixed to tenancy.Scope would have
		// left that copy of the loop behind.
		type account struct {
			ID    string
			Scope tenancy.Scope
		}

		var seen []string

		err := ForEachOwner(t.Context(),
			func(context.Context, tenancy.Scope, Subject) ([]account, error) {
				return []account{{ID: "acct-1"}, {ID: "acct-2"}}, nil
			},
			tenancy.Scope{}, fanSubject,
			func(_ context.Context, a account) error {
				seen = append(seen, a.ID)

				return nil
			})
		must.NoError(t, err)
		test.Eq(t, []string{"acct-1", "acct-2"}, seen)
	})

	T.Run("nil arguments are refused", func(t *testing.T) {
		t.Parallel()

		// A missing resolver read as "this subject owns nothing" would produce
		// an export with the section missing and an erasure that destroyed
		// nothing, both reported as successes.
		err := ForEachOwner[tenancy.Scope](t.Context(), nil, tenancy.Scope{}, fanSubject,
			func(context.Context, tenancy.Scope) error { return nil })
		test.ErrorIs(t, err, ErrNilResolver)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)

		err = ForEachOwner(t.Context(), scopesOf(), tenancy.Scope{}, fanSubject, nil)
		test.ErrorIs(t, err, ErrNilFanOut)
	})
}

func TestCollectByScope(T *testing.T) {
	T.Parallel()

	T.Run("pages every scope to its end and concatenates", func(t *testing.T) {
		t.Parallel()

		// Two scopes of three rows each, read two at a time: the point is that
		// each scope is walked to its end rather than read once.
		stores := map[tenancy.Scope]*pagedStore{
			tenancy.Of("a"): newPagedStore(collectRows(3), 2),
			tenancy.Of("b"): newPagedStore(collectRows(3), 2),
		}

		rows, err := CollectByScope(t.Context(), scopesOf(tenancy.Of("a"), tenancy.Of("b")),
			tenancy.Scope{}, fanSubject, "rows",
			func(
				ctx context.Context,
				scope tenancy.Scope,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[collectRow], error) {
				return stores[scope].fetch(ctx, filter)
			})
		must.NoError(t, err)
		test.SliceLen(t, 6, rows)
		test.Eq(t, []string{"row-00", "row-01", "row-02", "row-00", "row-01", "row-02"}, collectedIDs(rows))
	})

	T.Run("no scopes is an empty result rather than an error", func(t *testing.T) {
		t.Parallel()

		// nil rather than an empty slice, so it composes with Fragment's held
		// flag without a length check at the call site.
		rows, err := CollectByScope(t.Context(), scopesOf(), tenancy.Scope{}, fanSubject, "rows",
			func(
				context.Context,
				tenancy.Scope,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[collectRow], error) {
				t.Fatal("read must not run when nothing resolved")

				return nil, nil
			})
		must.NoError(t, err)
		test.Nil(t, rows)
	})

	T.Run("a failed read names what the domain holds and which scope", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the store refused")

		_, err := CollectByScope(t.Context(), scopesOf(tenancy.Of("tenant-7")),
			tenancy.Scope{}, fanSubject, "setting values",
			func(
				context.Context,
				tenancy.Scope,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[collectRow], error) {
				return nil, boom
			})
		must.Error(t, err)
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), "setting values")
		test.StrContains(t, err.Error(), "tenant-7")
	})

	T.Run("the filter reaches the read untouched", func(t *testing.T) {
		t.Parallel()

		// An adapter that wants archived rows copies the filter in its own
		// closure. The one collector in this module that must not do that is
		// notifications' device registry, whose table has no archived_at.
		var saw *bool

		_, err := CollectByScope(t.Context(), scopesOf(tenancy.Of("a")),
			tenancy.Scope{}, fanSubject, "rows",
			func(
				ctx context.Context,
				_ tenancy.Scope,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[collectRow], error) {
				saw = filter.IncludeArchived

				return newPagedStore(nil, 2).fetch(ctx, filter)
			})
		must.NoError(t, err)
		test.Nil(t, saw)
	})

	T.Run("a nil read is refused", func(t *testing.T) {
		t.Parallel()

		_, err := CollectByScope[collectRow](t.Context(), scopesOf(), tenancy.Scope{}, fanSubject, "rows", nil)
		test.ErrorIs(t, err, ErrNilFetch)
	})
}

func TestEraseByScope(T *testing.T) {
	T.Parallel()

	T.Run("sums what every scope reported", func(t *testing.T) {
		t.Parallel()

		counts := map[tenancy.Scope]ErasureOutcome{
			tenancy.Of("a"): {Deleted: 2, Anonymized: 1},
			tenancy.Of("b"): {Deleted: 3, Anonymized: 4},
		}

		outcome, err := EraseByScope(t.Context(), stubTx{}, scopesOf(tenancy.Of("a"), tenancy.Of("b")),
			tenancy.Scope{}, fanSubject, "comments",
			func(_ context.Context, _ database.Tx, scope tenancy.Scope) (ErasureOutcome, error) {
				return counts[scope], nil
			})
		must.NoError(t, err)
		test.EqOp(t, int64(5), outcome.Deleted)
		test.EqOp(t, int64(5), outcome.Anonymized)
	})

	T.Run("Retained is not carried across the fan-out", func(t *testing.T) {
		t.Parallel()

		// An adapter that retains something composes one sentence for the whole
		// request out of the total — waitlists/privacy and mediaregistry/privacy
		// both do it after the fan-out — and N copies of the same paragraph, one
		// per tenant, is not a better answer in front of a regulator.
		outcome, err := EraseByScope(t.Context(), stubTx{}, scopesOf(tenancy.Of("a")),
			tenancy.Scope{}, fanSubject, "comments",
			func(context.Context, database.Tx, tenancy.Scope) (ErasureOutcome, error) {
				return ErasureOutcome{Retained: map[string]string{"rows": "kept"}}, nil
			})
		must.NoError(t, err)
		test.MapEmpty(t, outcome.Retained)
	})

	T.Run("the transaction reaches the write", func(t *testing.T) {
		t.Parallel()

		// Every eraser in one request shares one transaction, so the one handed
		// in is the one the write must run on.
		tx := stubTx{}

		var saw database.Tx

		_, err := EraseByScope(t.Context(), tx, scopesOf(tenancy.Of("a")),
			tenancy.Scope{}, fanSubject, "comments",
			func(_ context.Context, got database.Tx, _ tenancy.Scope) (ErasureOutcome, error) {
				saw = got

				return ErasureOutcome{}, nil
			})
		must.NoError(t, err)
		test.EqOp(t, database.Tx(tx), saw)
	})

	T.Run("a failed write discards the counts already summed", func(t *testing.T) {
		t.Parallel()

		// There is nothing to report counts about: every eraser in one request
		// shares one transaction, and the write that already happened is about
		// to roll back with the rest.
		boom := platformerrors.New("the store refused")

		outcome, err := EraseByScope(t.Context(), stubTx{}, scopesOf(tenancy.Of("a"), tenancy.Of("tenant-9")),
			tenancy.Scope{}, fanSubject, "comments",
			func(_ context.Context, _ database.Tx, scope tenancy.Scope) (ErasureOutcome, error) {
				if scope == tenancy.Of("a") {
					return ErasureOutcome{Deleted: 4}, nil
				}

				return ErasureOutcome{}, boom
			})
		must.Error(t, err)
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), "comments")
		test.StrContains(t, err.Error(), "tenant-9")
		test.EqOp(t, int64(0), outcome.Deleted)
		test.EqOp(t, int64(0), outcome.Anonymized)
		test.MapEmpty(t, outcome.Retained)
	})

	T.Run("nil arguments are refused", func(t *testing.T) {
		t.Parallel()

		_, err := EraseByScope(t.Context(), nil, scopesOf(), tenancy.Scope{}, fanSubject, "comments",
			func(context.Context, database.Tx, tenancy.Scope) (ErasureOutcome, error) {
				return ErasureOutcome{}, nil
			})
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = EraseByScope(t.Context(), stubTx{}, scopesOf(), tenancy.Scope{}, fanSubject, "comments", nil)
		test.ErrorIs(t, err, ErrNilErase)
	})
}

func TestFixedScopes(T *testing.T) {
	T.Parallel()

	T.Run("answers the same scopes for every subject", func(t *testing.T) {
		t.Parallel()

		resolve := FixedScopes(tenancy.Global(), tenancy.Of("a"))

		scopes, err := resolve(t.Context(), tenancy.Scope{}, fanSubject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Global(), tenancy.Of("a")}, scopes)

		scopes, err = resolve(t.Context(), tenancy.Of("ignored"), Subject{ID: "somebody-else"})
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Global(), tenancy.Of("a")}, scopes)
	})

	T.Run("the caller's slice cannot be edited afterwards", func(t *testing.T) {
		t.Parallel()

		given := []tenancy.Scope{tenancy.Of("a")}
		resolve := FixedScopes(given...)
		given[0] = tenancy.Of("b")

		scopes, err := resolve(t.Context(), tenancy.Scope{}, fanSubject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of("a")}, scopes)
	})
}

func TestRequestScopeOr(T *testing.T) {
	T.Parallel()

	T.Run("answers the scope the request named", func(t *testing.T) {
		t.Parallel()

		scopes, err := RequestScopeOr(nil)(t.Context(), tenancy.Of("tenant-3"), fanSubject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of("tenant-3")}, scopes)
	})

	T.Run("a request naming no scope is refused with the caller's sentinel", func(t *testing.T) {
		t.Parallel()

		// The wording belongs to the adapter, because a consumer matching that
		// package's ErrUnscopedRequest is matching something it can read in a
		// log. Answering tenancy.Global() instead would produce an export that
		// is well-formed, has a section, and is missing every row.
		mine := platformerrors.New("comments request names no scope")

		_, err := RequestScopeOr(mine)(t.Context(), tenancy.Scope{}, fanSubject)
		must.Error(t, err)
		test.ErrorIs(t, err, mine)
		test.StrContains(t, err.Error(), fanSubject.ID)
	})

	T.Run("no sentinel falls back to this package's own", func(t *testing.T) {
		t.Parallel()

		_, err := RequestScopeOr(nil)(t.Context(), tenancy.Scope{}, fanSubject)
		test.ErrorIs(t, err, ErrUnscopedRequest)
	})

	T.Run("the global scope is a scope a request may name", func(t *testing.T) {
		t.Parallel()

		// tenancy.Global() validates, so a resolver built here answers with it.
		// What a request may not be *confined* to is dataprivacy's own ruling,
		// enforced at submission by ErrGlobalRequestScope rather than here.
		scopes, err := RequestScopeOr(nil)(t.Context(), tenancy.Global(), fanSubject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Global()}, scopes)
	})
}
