package auditerasure

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"
	identitymock "github.com/primandproper/platform-go/v15/identity/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errDirectoryUnavailable stands in for a directory that cannot answer.
var errDirectoryUnavailable = platformerrors.New("the identity directory is unavailable")

// directoryOf is an identity directory in which user-1 belongs to accounts.
// want, when set, is the executor every read must arrive on.
func directoryOf(t *testing.T, want func(database.SQLQueryExecutor), accounts ...*identity.Account) *identitymock.StoreMock {
	t.Helper()

	return &identitymock.StoreMock{
		ListAccountsForUserFunc: func(
			_ context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			userID string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Account], error) {
			if want != nil {
				want(q)
			}

			test.EqOp(t, tenancy.Global(), scope)
			test.EqOp(t, "user-1", userID)
			must.NotNil(t, filter.IncludeArchived)
			test.True(t, *filter.IncludeArchived)

			result := &filtering.QueryFilteredResult[identity.Account]{Data: accounts}
			if len(accounts) > 0 {
				result.Cursor = accounts[len(accounts)-1].ID
			}

			return result, nil
		},
	}
}

func TestOwnedScopeResolver(T *testing.T) {
	T.Parallel()

	T.Run("the subject's own chain and the accounts they own, not the ones they belong to", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = database.NewTxForTesting(nil)

		directory := directoryOf(t, func(got database.SQLQueryExecutor) { test.EqOp(t, q, got) },
			&identity.Account{ID: "account-1", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-2", OwnerUserID: "user-7"},
			&identity.Account{ID: "account-3", OwnerUserID: "user-1"},
		)

		scopes, err := OwnedScopeResolver(directory)(t.Context(), q, tenancy.Of("account-9"), dataprivacy.Subject{ID: "user-1"})
		must.NoError(t, err)

		test.Eq(t, []tenancy.Scope{tenancy.Of("user-1"), tenancy.Of("account-1"), tenancy.Of("account-3")}, scopes)
	})

	T.Run("refuses what it cannot answer for", func(t *testing.T) {
		t.Parallel()

		q := database.NewTxForTesting(nil)

		_, err := OwnedScopeResolver(nil)(t.Context(), q, tenancy.Scope{}, dataprivacy.Subject{ID: "user-1"})
		test.ErrorIs(t, err, ErrNilDirectory)

		_, err = OwnedScopeResolver(&identitymock.StoreMock{})(t.Context(), nil, tenancy.Scope{}, dataprivacy.Subject{ID: "user-1"})
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)

		_, err = OwnedScopeResolver(&identitymock.StoreMock{})(t.Context(), q, tenancy.Scope{}, dataprivacy.Subject{})
		test.ErrorIs(t, err, ErrAnonymousSubject)
	})

	T.Run("a failing directory is reported rather than resolved short", func(t *testing.T) {
		t.Parallel()

		directory := &identitymock.StoreMock{
			ListAccountsForUserFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter) (*filtering.QueryFilteredResult[identity.Account], error) {
				return nil, errDirectoryUnavailable
			},
		}

		_, err := OwnedScopeResolver(directory)(t.Context(), database.NewTxForTesting(nil), tenancy.Scope{}, dataprivacy.Subject{ID: "user-1"})
		test.ErrorIs(t, err, errDirectoryUnavailable)
	})
}

func TestSubjectScope(T *testing.T) {
	T.Parallel()

	T.Run("the subject's own ID and nothing else", func(t *testing.T) {
		t.Parallel()

		scopes, err := SubjectScope(t.Context(), nil, tenancy.Of("tenant-1"), dataprivacy.Subject{ID: "user-1"})
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of("user-1")}, scopes)
	})

	T.Run("refuses a subject with no ID rather than naming the global chain", func(t *testing.T) {
		t.Parallel()

		scopes, err := SubjectScope(t.Context(), nil, tenancy.Scope{}, dataprivacy.Subject{})
		test.ErrorIs(t, err, ErrAnonymousSubject)
		test.SliceEmpty(t, scopes)
	})
}

func TestEraser_OwnedScopeResolver(T *testing.T) {
	T.Parallel()

	T.Run("an owner's erasure deletes the account's chain and leaves the one they only belong to", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, tenancy.Of("user-1"), "user-1", "user-1")
		env.record(t, tenancy.Of("account-1"), "user-7", "account-1")
		env.record(t, tenancy.Of("account-1"), "user-1", "account-1")
		env.record(t, tenancy.Of("account-2"), "user-7", "account-2")
		env.record(t, tenancy.Of("account-2"), "user-1", "account-2")

		var erasureTx database.Tx

		directory := directoryOf(t,
			func(got database.SQLQueryExecutor) {
				// The resolver reads in the erasure's own transaction.
				test.EqOp(t, database.SQLQueryExecutor(erasureTx), got)
			},
			&identity.Account{ID: "account-1", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-2", OwnerUserID: "user-7"},
		)

		eraser, err := New(dialect.SQLite, OwnedScopeResolver(directory))
		must.NoError(t, err)

		var outcome dataprivacy.ErasureOutcome

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			erasureTx = tx

			var eraseErr error
			outcome, eraseErr = eraser.Erase(t.Context(), tx, tenancy.Scope{}, dataprivacy.Subject{ID: "user-1"})

			return eraseErr
		}))

		test.EqOp(t, int64(3), outcome.Deleted)
		test.EqOp(t, int64(0), env.countEntries(t, tenancy.Of("user-1")))
		test.EqOp(t, int64(0), env.countEntries(t, tenancy.Of("account-1")))
		test.EqOp(t, int64(2), env.countEntries(t, tenancy.Of("account-2")))

		// What the subject did in the account they only belonged to is kept,
		// and said to be.
		test.MapLen(t, 1, outcome.Retained)
		test.StrContains(t, outcome.Retained["entries"], "1 ")
	})

	T.Run("New refuses no resolver rather than assuming one", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, nil)
		test.ErrorIs(t, err, ErrNilScopeResolver)
		test.Nil(t, eraser)
	})
}
