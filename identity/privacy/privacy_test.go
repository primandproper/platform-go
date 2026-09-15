package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"
	"github.com/primandproper/platform-go/v14/identity/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two directories a subject's rows might be in. Two, because the whole of
// what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("dir_1")
	secondScope = tenancy.Of("dir_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands the collector and the eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// userIn is the subject's directory row in one scope, credentials and all — the
// export is asserted to have dropped them.
func userIn(scope tenancy.Scope) *identity.User {
	return &identity.User{
		ID:                            subject.ID,
		Scope:                         scope,
		Username:                      "ada",
		EmailAddress:                  "ada@example.com",
		HashedPassword:                "argon2$secret",
		TwoFactorSecret:               "otpauth://secret",
		EmailAddressVerificationToken: "verify-me",
		AccountStatus:                 identity.StatusGood,
	}
}

// invitationIn is one stored invitation, in a scope and a status.
func invitationIn(scope tenancy.Scope, id string, status identity.InvitationStatus) *identity.Invitation {
	return &identity.Invitation{
		ID:               id,
		Scope:            scope,
		BelongsToAccount: "acct_1",
		FromUser:         subject.ID,
		ToEmail:          "brian@example.com",
		Status:           status,
		ExpiresAt:        time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
	}
}

// pageOf returns one full page of invitations, as a store would.
func pageOf(found ...*identity.Invitation) *filtering.QueryFilteredResult[identity.Invitation] {
	return filtering.NewQueryFilteredResult(found, uint64(len(found)), uint64(len(found)),
		func(i *identity.Invitation) string { return i.ID }, filtering.DefaultQueryFilter())
}

// readableStore is the mock every collector case starts from: the subject
// present in every scope asked for, with nothing hanging off them.
func readableStore() *identitymock.StoreMock {
	return &identitymock.StoreMock{
		GetUserIncludingArchivedFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			_ string,
		) (*identity.User, error) {
			return userIn(scope), nil
		},
		ListMembershipsForUserFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
		) ([]*identity.Membership, error) {
			return nil, nil
		},
		ListInvitationsFromUserFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
			_ identity.InvitationStatus,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			return pageOf(), nil
		},
		ListInvitationsForEmailAddressFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
			_ identity.InvitationStatus,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			return pageOf(), nil
		},
	}
}

// collect runs one collection and decodes what it produced.
func collect(t *testing.T, collector *privacy.Collector) []privacy.Export {
	t.Helper()

	raw, err := collector.Collect(t.Context(), testScope, subject)
	must.NoError(t, err)

	var exports []privacy.Export
	must.NoError(t, json.Unmarshal(raw, &exports))

	return exports
}

// testReader is the executor a collector is built over.
//
// Nothing executes through it: the store beneath the collector is a mock, and
// what these tests assert is that the executor the collector was built with is
// the one it passes down. database.SQLQueryExecutor is an interface with no
// unexported methods, so unlike database.Tx a test can stand in for it.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the collector's store is a mock; nothing runs on this")
}

// withTx runs fn inside a real transaction.
//
// database.Tx carries an unexported method, so it is producible only by the
// database package — which is the point of the type, and which means a test that
// wants one opens a database rather than standing in for it. Nothing here
// executes a statement through it: the store beneath the eraser is a mock, and
// what is being asserted is that the executor the eraser was handed is the one
// it passes down.
func withTx(t *testing.T, fn func(q database.Tx)) {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "privacy.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		fn(q)

		return nil
	}))
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func TestRequestScope(T *testing.T) {
	T.Parallel()

	T.Run("resolves the scope the request names", func(t *testing.T) {
		t.Parallel()

		scopes, err := privacy.RequestScope(t.Context(), firstScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{firstScope}, scopes)
	})

	T.Run("refuses a request that names none", func(t *testing.T) {
		t.Parallel()

		// Not the global scope. An export that quietly covered only the global
		// directory would be well-formed, would have a section, and would be
		// missing the subject entirely.
		_, err := privacy.RequestScope(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})
}

func TestFixedScopes(T *testing.T) {
	T.Parallel()

	T.Run("resolves every subject to the same scopes", func(t *testing.T) {
		t.Parallel()

		scopes, err := privacy.FixedScopes(tenancy.Global())(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Global()}, scopes)
	})

	T.Run("keeps its own copy of what it was given", func(t *testing.T) {
		t.Parallel()

		// The caller's slice is the caller's. A resolver that aliased it would
		// have the directories it erases decided by whoever mutated that slice
		// next.
		given := []tenancy.Scope{firstScope, secondScope}
		resolve := privacy.FixedScopes(given...)
		given[0] = tenancy.Of("somebody_else")

		scopes, err := resolve(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{firstScope, secondScope}, scopes)
	})
}

func TestNewCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewCollector(nil, &testReader{}, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilStore)

		// The executor is a constructor argument because Collect has nowhere to
		// put one, so an absent one has to be refused here or every collection
		// fails at the store instead.
		_, err = privacy.NewCollector(&identitymock.StoreMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&identitymock.StoreMock{}, &testReader{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("exports the user redacted, with their memberships and invitations", func(t *testing.T) {
		t.Parallel()

		store := readableStore()
		store.ListMembershipsForUserFunc = func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			_ string,
		) ([]*identity.Membership, error) {
			return []*identity.Membership{
				{ID: "mem_1", Scope: scope, BelongsToUser: subject.ID, BelongsToAccount: "acct_1"},
				nil,
			}, nil
		}
		store.ListInvitationsFromUserFunc = func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			_ string,
			status identity.InvitationStatus,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			return pageOf(invitationIn(scope, "sent_"+status.String(), status)), nil
		}
		store.ListInvitationsForEmailAddressFunc = func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			address string,
			status identity.InvitationStatus,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			// The address is the subject's own, read off their row rather than
			// guessed at: a collector that asked by the subject's id would find
			// nothing, since this table addresses people by their mailbox.
			test.EqOp(t, "ada@example.com", address)

			return pageOf(invitationIn(scope, "received_"+status.String(), status)), nil
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		exports := collect(t, collector)
		must.SliceLen(t, 1, exports)

		// A subject access request is not a credential dump.
		must.NotNil(t, exports[0].User)
		test.EqOp(t, "", exports[0].User.HashedPassword)
		test.EqOp(t, "", exports[0].User.TwoFactorSecret)
		test.EqOp(t, "", exports[0].User.EmailAddressVerificationToken)
		test.EqOp(t, "ada@example.com", exports[0].User.EmailAddress)

		// The nil the store handed back is skipped rather than exported as a
		// null nobody checks for.
		must.SliceLen(t, 1, exports[0].Memberships)
		test.EqOp(t, "mem_1", exports[0].Memberships[0].ID)

		// Every status, in both directions: an invitation somebody accepted or
		// declined is as much a fact about them as one they have not answered.
		must.SliceLen(t, len(identity.InvitationStatuses), exports[0].InvitationsSent)
		must.SliceLen(t, len(identity.InvitationStatuses), exports[0].InvitationsReceived)

		for i, status := range identity.InvitationStatuses {
			test.EqOp(t, "sent_"+status.String(), exports[0].InvitationsSent[i].ID)
			test.EqOp(t, "received_"+status.String(), exports[0].InvitationsReceived[i].ID)
		}
	})

	T.Run("collects every scope the resolver names", func(t *testing.T) {
		t.Parallel()

		store := readableStore()

		collector, err := privacy.NewCollector(store, &testReader{},
			privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		exports := collect(t, collector)
		must.SliceLen(t, 2, exports)
		test.EqOp(t, firstScope, exports[0].Scope)
		test.EqOp(t, secondScope, exports[1].Scope)
	})

	T.Run("passes the executor it was built with", func(t *testing.T) {
		t.Parallel()

		// Collect is handed nothing, so the executor has to come from the
		// constructor — and a collector that quietly used something else would
		// read a database the caller did not name.
		var reader database.SQLQueryExecutor = &testReader{}

		store := readableStore()
		store.GetUserIncludingArchivedFunc = func(
			_ context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			_ string,
		) (*identity.User, error) {
			test.EqOp(t, reader, q)

			return userIn(scope), nil
		}

		collector, err := privacy.NewCollector(store, reader, privacy.RequestScope)
		must.NoError(t, err)

		test.SliceLen(t, 1, collect(t, collector))
	})

	T.Run("skips a directory the subject is not in", func(t *testing.T) {
		t.Parallel()

		// The ordinary answer for a resolver naming more directories than any
		// one person appears in. Failing here would make a broad resolver
		// unusable for the subjects it is right about.
		store := readableStore()
		store.GetUserIncludingArchivedFunc = func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			scope tenancy.Scope,
			_ string,
		) (*identity.User, error) {
			if scope == secondScope {
				return nil, platformerrors.Wrap(identity.ErrUserNotFound, "no such user")
			}

			return userIn(scope), nil
		}

		collector, err := privacy.NewCollector(store, &testReader{},
			privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		exports := collect(t, collector)
		must.SliceLen(t, 1, exports)
		test.EqOp(t, firstScope, exports[0].Scope)
	})

	T.Run("holds nothing for a subject in no directory at all", func(t *testing.T) {
		t.Parallel()

		store := readableStore()
		store.GetUserIncludingArchivedFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*identity.User, error) {
			return nil, identity.ErrUserNotFound
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		// A nil fragment is the domain holding nothing, which is not the same
		// document as an empty section.
		raw, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, raw)
	})

	T.Run("reports what the resolver and the store could not answer", func(t *testing.T) {
		t.Parallel()

		failing := func(context.Context, tenancy.Scope, dataprivacy.Subject) ([]tenancy.Scope, error) {
			return nil, errStoreUnavailable
		}

		collector, err := privacy.NewCollector(readableStore(), &testReader{}, failing)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)

		// A read that fails for a reason other than the subject's absence is a
		// failed export rather than an empty one.
		store := readableStore()
		store.GetUserIncludingArchivedFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*identity.User, error) {
			return nil, errStoreUnavailable
		}

		collector, err = privacy.NewCollector(store, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)
	})

	T.Run("reports a failed membership or invitation read", func(t *testing.T) {
		t.Parallel()

		store := readableStore()
		store.ListMembershipsForUserFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) ([]*identity.Membership, error) {
			return nil, errStoreUnavailable
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)

		sender := readableStore()
		sender.ListInvitationsFromUserFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
			identity.InvitationStatus,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			return nil, errStoreUnavailable
		}

		collector, err = privacy.NewCollector(sender, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)

		recipient := readableStore()
		recipient.ListInvitationsForEmailAddressFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
			identity.InvitationStatus,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Invitation], error) {
			return nil, errStoreUnavailable
		}

		collector, err = privacy.NewCollector(recipient, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)
	})
}

func TestNewEraser(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewEraser(nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilStore)

		_, err = privacy.NewEraser(&identitymock.StoreMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("erases the invitations first, then the user, on the caller's transaction", func(t *testing.T) {
		t.Parallel()

		// The order is load-bearing: the invitation erasure reads the subject's
		// address off the row the user erasure destroys.
		var order []string

		withTx(t, func(tx database.Tx) {
			store := &identitymock.StoreMock{
				EraseInvitationsForSubjectFunc: func(
					_ context.Context,
					q database.Tx,
					scope tenancy.Scope,
					userID string,
				) (identity.InvitationErasure, error) {
					order = append(order, "invitations")
					test.EqOp(t, tx, q)
					test.EqOp(t, testScope, scope)
					test.EqOp(t, subject.ID, userID)

					return identity.InvitationErasure{Deleted: 2, Anonymized: 3}, nil
				},
				EraseUserFunc: func(
					_ context.Context,
					q database.Tx,
					_ tenancy.Scope,
					_ string,
				) (int64, error) {
					order = append(order, "user")
					test.EqOp(t, tx, q)

					return 1, nil
				},
			}

			eraser, err := privacy.NewEraser(store, privacy.RequestScope)
			must.NoError(t, err)

			outcome, err := eraser.Erase(t.Context(), tx, testScope, subject)
			must.NoError(t, err)

			// The user row and the invitations addressed to the subject are both
			// destroyed; the ones they sent survive without them.
			test.EqOp(t, int64(3), outcome.Deleted)
			test.EqOp(t, int64(3), outcome.Anonymized)

			// Nothing of the subject's is kept, so nothing is reported as kept:
			// what is left on an anonymized invitation is the recipient's.
			test.MapEmpty(t, outcome.Retained)
		})

		test.Eq(t, []string{"invitations", "user"}, order)
	})

	T.Run("erases every scope the resolver names", func(t *testing.T) {
		t.Parallel()

		withTx(t, func(tx database.Tx) {
			var erased []tenancy.Scope

			store := &identitymock.StoreMock{
				EraseInvitationsForSubjectFunc: func(
					_ context.Context,
					_ database.Tx,
					scope tenancy.Scope,
					_ string,
				) (identity.InvitationErasure, error) {
					erased = append(erased, scope)

					return identity.InvitationErasure{Deleted: 1}, nil
				},
				EraseUserFunc: func(context.Context, database.Tx, tenancy.Scope, string) (int64, error) {
					return 1, nil
				},
			}

			eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope, secondScope))
			must.NoError(t, err)

			outcome, err := eraser.Erase(t.Context(), tx, testScope, subject)
			must.NoError(t, err)
			test.EqOp(t, int64(4), outcome.Deleted)
			test.Eq(t, []tenancy.Scope{firstScope, secondScope}, erased)
		})
	})

	T.Run("skips a directory the subject is not in", func(t *testing.T) {
		t.Parallel()

		// This method controls the order, so the only way to reach
		// ErrUserNotFound here is a directory the subject is not in — which is
		// also what makes a replayed erasure a no-op rather than a failure.
		withTx(t, func(tx database.Tx) {
			var users int

			store := &identitymock.StoreMock{
				EraseInvitationsForSubjectFunc: func(
					_ context.Context,
					_ database.Tx,
					scope tenancy.Scope,
					_ string,
				) (identity.InvitationErasure, error) {
					if scope == secondScope {
						return identity.InvitationErasure{},
							platformerrors.Wrap(identity.ErrUserNotFound, "no such user")
					}

					return identity.InvitationErasure{Deleted: 1}, nil
				},
				EraseUserFunc: func(context.Context, database.Tx, tenancy.Scope, string) (int64, error) {
					users++

					return 1, nil
				},
			}

			eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope, secondScope))
			must.NoError(t, err)

			outcome, err := eraser.Erase(t.Context(), tx, testScope, subject)
			must.NoError(t, err)
			test.EqOp(t, int64(2), outcome.Deleted)

			// The skipped directory's user erasure never runs: there is nobody
			// there to erase, and running it would be a write keyed on an id
			// this directory does not hold.
			test.EqOp(t, 1, users)
		})
	})

	T.Run("refuses a nil transaction", func(t *testing.T) {
		t.Parallel()

		// An eraser that opened its own would decide when the subject's
		// footprint commits, which is the one thing the request's transaction
		// exists to keep.
		eraser, err := privacy.NewEraser(&identitymock.StoreMock{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
	})

	T.Run("reports what the resolver and the store could not answer", func(t *testing.T) {
		t.Parallel()

		withTx(t, func(tx database.Tx) {
			failing := func(context.Context, tenancy.Scope, dataprivacy.Subject) ([]tenancy.Scope, error) {
				return nil, errStoreUnavailable
			}

			eraser, err := privacy.NewEraser(&identitymock.StoreMock{}, failing)
			must.NoError(t, err)

			_, err = eraser.Erase(t.Context(), tx, testScope, subject)
			must.ErrorIs(t, err, errStoreUnavailable)

			invitations := &identitymock.StoreMock{
				EraseInvitationsForSubjectFunc: func(
					context.Context,
					database.Tx,
					tenancy.Scope,
					string,
				) (identity.InvitationErasure, error) {
					return identity.InvitationErasure{}, errStoreUnavailable
				},
			}

			eraser, err = privacy.NewEraser(invitations, privacy.RequestScope)
			must.NoError(t, err)

			_, err = eraser.Erase(t.Context(), tx, testScope, subject)
			must.ErrorIs(t, err, errStoreUnavailable)

			user := &identitymock.StoreMock{
				EraseInvitationsForSubjectFunc: func(
					context.Context,
					database.Tx,
					tenancy.Scope,
					string,
				) (identity.InvitationErasure, error) {
					return identity.InvitationErasure{}, nil
				},
				EraseUserFunc: func(context.Context, database.Tx, tenancy.Scope, string) (int64, error) {
					return 0, errStoreUnavailable
				},
			}

			eraser, err = privacy.NewEraser(user, privacy.RequestScope)
			must.NoError(t, err)

			_, err = eraser.Erase(t.Context(), tx, testScope, subject)
			must.ErrorIs(t, err, errStoreUnavailable)
		})
	})
}
