package auditerasure

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"
	identitymigrations "github.com/primandproper/platform-go/v15/identity/migrations"
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

// directoryOf is an identity directory in which user-1 belongs to accounts,
// and each account's live members are the users rosters names for it — none,
// for an account rosters does not name. want, when set, is the executor every
// read must arrive on.
func directoryOf(
	t *testing.T,
	want func(database.SQLQueryExecutor),
	rosters map[string][]string,
	accounts ...*identity.Account,
) *identitymock.StoreMock {
	t.Helper()

	return &identitymock.StoreMock{
		ListAccountMembersFunc: func(
			_ context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			accountID string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
			if want != nil {
				want(q)
			}

			test.EqOp(t, tenancy.Global(), scope)
			must.NotNil(t, filter)
			must.NotNil(t, filter.MaxResponseSize)

			result := &filtering.QueryFilteredResult[identity.MembershipWithUser]{}
			for _, userID := range rosters[accountID] {
				if len(result.Data) == int(*filter.MaxResponseSize) {
					break
				}

				result.Data = append(result.Data, &identity.MembershipWithUser{
					User:             &identity.User{ID: userID},
					BelongsToUser:    userID,
					BelongsToAccount: accountID,
				})
			}

			return result, nil
		},
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

	T.Run("the subject's own chain and the accounts only they belong to", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = database.NewTxForTesting(nil)

		directory := directoryOf(t, func(got database.SQLQueryExecutor) { test.EqOp(t, q, got) },
			map[string][]string{
				"account-1": {"user-1"},
				"account-2": {"user-1", "user-7"},
				"account-3": {"user-1"},
				// Owned and shared: it stays standing, so its history does.
				"account-4": {"user-1", "user-7"},
				// Shared with somebody whose ID sorts first, so the subject
				// is not the first row of the page.
				"account-5": {"user-0", "user-1"},
				// account-6's roster is empty, as every roster reads once the
				// subject's own user row is archived earlier in the erasure:
				// a roster names live users only.
			},
			&identity.Account{ID: "account-1", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-2", OwnerUserID: "user-7"},
			&identity.Account{ID: "account-3", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-4", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-5", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-6", OwnerUserID: "user-1"},
		)

		scopes, err := OwnedScopeResolver(directory)(t.Context(), q, tenancy.Of("account-9"), dataprivacy.Subject{ID: "user-1"})
		must.NoError(t, err)

		test.Eq(t, []tenancy.Scope{
			tenancy.Of("user-1"),
			tenancy.Of("account-1"),
			tenancy.Of("account-3"),
			tenancy.Of("account-6"),
		}, scopes)

		// The rosters read are the owned accounts', and only theirs.
		read := make([]string, 0, len(directory.ListAccountMembersCalls()))
		for _, call := range directory.ListAccountMembersCalls() {
			read = append(read, call.AccountID)
		}
		test.Eq(t, []string{"account-1", "account-3", "account-4", "account-5", "account-6"}, read)
	})

	T.Run("a failing roster read is reported rather than resolved short", func(t *testing.T) {
		t.Parallel()

		directory := directoryOf(t, nil, nil, &identity.Account{ID: "account-1", OwnerUserID: "user-1"})
		directory.ListAccountMembersFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
			return nil, errDirectoryUnavailable
		}

		scopes, err := OwnedScopeResolver(directory)(t.Context(), database.NewTxForTesting(nil), tenancy.Scope{}, dataprivacy.Subject{ID: "user-1"})
		test.ErrorIs(t, err, errDirectoryUnavailable)
		test.SliceEmpty(t, scopes)
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

// TestOwnedScopeResolver_LiveDirectory runs the resolver against identity's own
// store on SQLite, because what counts as a member is the store's roster read
// and not a mock's reading of it.
func TestOwnedScopeResolver_LiveDirectory(T *testing.T) {
	T.Parallel()

	T.Run("resolves the accounts the subject owns and nobody else lives in", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		env := newAuditEnv(t)

		stmts, err := identitymigrations.Statements(dialect.SQLite, identity.DefaultTablePrefix)
		must.NoError(t, err)

		for _, stmt := range stmts {
			_, execErr := env.client.Writer().ExecContext(ctx, stmt)
			must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
		}

		directory, err := identity.NewSQLStore(env.client)
		must.NoError(t, err)

		scope := tenancy.Global()
		ids := map[string]string{}

		must.NoError(t, env.client.WithTransaction(ctx, func(tx database.Tx) error {
			for _, name := range []string{"subject", "colleague", "departed"} {
				user, createErr := directory.CreateUser(ctx, tx, scope, &identity.User{
					Username:       name,
					EmailAddress:   name + "@example.com",
					HashedPassword: "argon2id$v=19$m=65536,t=3,p=2$...",
				})
				if createErr != nil {
					return createErr
				}

				ids[name] = user.ID
			}

			for _, account := range []struct {
				name, owner string
				members     []string
			}{
				{name: "solo", owner: "subject", members: []string{"subject"}},
				{name: "shared", owner: "subject", members: []string{"subject", "colleague"}},
				{name: "joined", owner: "colleague", members: []string{"colleague", "subject"}},
				{name: "vacated", owner: "subject", members: []string{"subject", "departed"}},
			} {
				created, createErr := directory.CreateAccount(ctx, tx, scope, &identity.Account{
					Name:        account.name,
					OwnerUserID: ids[account.owner],
				})
				if createErr != nil {
					return createErr
				}

				ids[account.name] = created.ID

				for _, member := range account.members {
					if _, createErr = directory.CreateMembership(ctx, tx, scope, &identity.Membership{
						BelongsToUser:    ids[member],
						BelongsToAccount: created.ID,
						Roles:            []string{"member"},
					}); createErr != nil {
						return createErr
					}
				}
			}

			// A member whose user is archived has left, and an account whose
			// only other member has left is the subject's alone.
			_, archiveErr := directory.ArchiveUser(ctx, tx, scope, ids["departed"])

			return archiveErr
		}))

		scopes, err := OwnedScopeResolver(directory)(ctx, env.client.Reader(), tenancy.Scope{}, dataprivacy.Subject{ID: ids["subject"]})
		must.NoError(t, err)

		test.SliceContainsAllOp(t, scopes, []tenancy.Scope{
			tenancy.Of(ids["subject"]),
			tenancy.Of(ids["solo"]),
			tenancy.Of(ids["vacated"]),
		})
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

	T.Run("a sole owner's erasure deletes the account's chain and leaves the shared ones", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, tenancy.Of("user-1"), "user-1", "user-1")
		env.record(t, tenancy.Of("account-1"), "user-7", "account-1")
		env.record(t, tenancy.Of("account-1"), "user-1", "account-1")
		env.record(t, tenancy.Of("account-2"), "user-7", "account-2")
		env.record(t, tenancy.Of("account-2"), "user-1", "account-2")
		env.record(t, tenancy.Of("account-3"), "user-7", "account-3")
		env.record(t, tenancy.Of("account-3"), "user-1", "account-3")

		var erasureTx database.Tx

		directory := directoryOf(t,
			func(got database.SQLQueryExecutor) {
				// The resolver reads in the erasure's own transaction.
				test.EqOp(t, database.SQLQueryExecutor(erasureTx), got)
			},
			map[string][]string{
				// user-7 acted in account-1 once and has since left it.
				"account-1": {"user-1"},
				"account-2": {"user-1", "user-7"},
				"account-3": {"user-1", "user-7"},
			},
			&identity.Account{ID: "account-1", OwnerUserID: "user-1"},
			&identity.Account{ID: "account-2", OwnerUserID: "user-7"},
			// Owned by the subject, and shared: it outlives them, and so does
			// its history.
			&identity.Account{ID: "account-3", OwnerUserID: "user-1"},
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
		test.EqOp(t, int64(2), env.countEntries(t, tenancy.Of("account-3")))

		// What the subject did in the accounts that outlive them is kept, and
		// said to be.
		test.MapLen(t, 1, outcome.Retained)
		test.StrContains(t, outcome.Retained["entries"], "2 ")
	})

	T.Run("New refuses no resolver rather than assuming one", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, nil)
		test.ErrorIs(t, err, ErrNilScopeResolver)
		test.Nil(t, eraser)
	})
}
