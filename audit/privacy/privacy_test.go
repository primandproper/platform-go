package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/audit/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/dataprivacy/auditerasure"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")
)

// subject is the person these tests are about.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// errReaderUnavailable stands in for a reader that cannot answer.
var errReaderUnavailable = platformerrors.New("the audit reader is unavailable")

// testReader is the executor a collector is built over. Nothing executes through
// it: the reader beneath the collector is a mock, and what these tests assert is
// that the executor the collector was built with is the one it passes down.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the collector's reader is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the collector's reader is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the collector's reader is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the collector's reader is a mock; nothing runs on this")
}

// page answers a List with every row in one page, as the real reader does for a
// result shorter than the page size.
func page(entries ...*audit.Entry) *filtering.QueryFilteredResult[audit.Entry] {
	result := &filtering.QueryFilteredResult[audit.Entry]{Data: entries}
	if len(entries) > 0 {
		result.Cursor = entries[len(entries)-1].ID
	}

	return result
}

// entry is one recorded event in a scope.
func entry(scope tenancy.Scope, id string, seq int64, actorID, resourceID string) *audit.Entry {
	return &audit.Entry{
		ID:           id,
		Scope:        scope,
		Seq:          seq,
		EventType:    audit.EventUpdated,
		ResourceType: "recipe",
		ResourceID:   resourceID,
		Actor:        audit.Actor{ID: actorID, Type: audit.ActorUser, IP: "203.0.113.7"},
	}
}

func TestDefaultKey(T *testing.T) {
	T.Parallel()

	// The collector and dataprivacy/auditerasure are one domain's two halves, so
	// an export's "audit" section and an erasure outcome's "audit" line name the
	// same log.
	test.EqOp(T, auditerasure.DefaultKey, privacy.DefaultKey)
}

func TestRequestScope(T *testing.T) {
	T.Parallel()

	T.Run("resolves the scope the request named", func(t *testing.T) {
		t.Parallel()

		scopes, err := privacy.RequestScope(t.Context(), firstScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{firstScope}, scopes)
	})

	T.Run("refuses a request that named none", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.RequestScope(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})
}

func TestNewCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewCollector(nil, &testReader{}, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilReader)

		_, err = privacy.NewCollector(&auditmock.ReaderMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&auditmock.ReaderMock{}, &testReader{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("reads each scope by actor and by resource, confined to that scope", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		log := &auditmock.ReaderMock{
			ListFunc: func(
				_ context.Context,
				q database.SQLQueryExecutor,
				query *audit.Query,
				_ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				test.EqOp(t, reader, q)

				// Every read names its scope. An unconfined List is the operator
				// console's read, and a subject's export is not that.
				must.NotNil(t, query.Scope)

				// Exactly one of the two narrowings, and it is the subject.
				switch {
				case query.ActorID != "":
					test.EqOp(t, subject.ID, query.ActorID)
					test.EqOp(t, "", query.ResourceID)

					return page(entry(*query.Scope, "acted_in_"+query.Scope.String(), 1, subject.ID, "recipe_1")), nil
				default:
					test.EqOp(t, subject.ID, query.ResourceID)

					return page(entry(*query.Scope, "acted_on_in_"+query.Scope.String(), 0, "admin_1", subject.ID)), nil
				}
			},
		}

		collector, err := privacy.NewCollector(log, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))

		// Scopes in resolver order, and within each scope chain order: the entry
		// the subject was acted on in sits at Seq 0, ahead of the one they acted in.
		ids := make([]string, 0, len(collected))
		for i := range collected {
			ids = append(ids, collected[i].ID)
		}

		test.Eq(t, []string{
			"acted_on_in_acct_1", "acted_in_acct_1",
			"acted_on_in_acct_2", "acted_in_acct_2",
		}, ids)
		test.SliceLen(t, 4, log.ListCalls())
	})

	T.Run("an entry the subject acted on themselves is exported once", func(t *testing.T) {
		t.Parallel()

		both := entry(firstScope, "self", 0, subject.ID, subject.ID)

		log := &auditmock.ReaderMock{
			ListFunc: func(
				context.Context, database.SQLQueryExecutor, *audit.Query, *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				return page(both), nil
			},
		}

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)
		test.EqOp(t, "self", collected[0].ID)
	})

	T.Run("keeps the subject's own address and drops somebody else's", func(t *testing.T) {
		t.Parallel()

		log := &auditmock.ReaderMock{
			ListFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, query *audit.Query, _ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				if query.ActorID != "" {
					return page(entry(firstScope, "mine", 0, subject.ID, "recipe_1")), nil
				}

				return page(entry(firstScope, "theirs", 1, "admin_1", subject.ID)), nil
			},
		}

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)

		test.EqOp(t, "mine", collected[0].ID)
		test.EqOp(t, "203.0.113.7", collected[0].Actor.IP)

		// Who acted on the subject is theirs to know; where that person was
		// connecting from is not.
		test.EqOp(t, "theirs", collected[1].ID)
		test.EqOp(t, "admin_1", collected[1].Actor.ID)
		test.EqOp(t, "", collected[1].Actor.IP)
	})

	T.Run("pages each read to its end", func(t *testing.T) {
		t.Parallel()

		log := &auditmock.ReaderMock{
			ListFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, query *audit.Query, filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				if query.ActorID == "" {
					return page(), nil
				}

				// A full first page, then a short second one.
				if filter.Cursor == nil {
					entries := make([]*audit.Entry, filtering.MaxQueryFilterLimit)
					for i := range entries {
						entries[i] = entry(firstScope, fmt.Sprintf("first_%d", i), int64(i), subject.ID, "r")
					}

					return page(entries...), nil
				}

				return page(entry(firstScope, "last", int64(filtering.MaxQueryFilterLimit), subject.ID, "r")), nil
			},
		}

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, int(filtering.MaxQueryFilterLimit)+1, collected)
		test.EqOp(t, "last", collected[len(collected)-1].ID)
	})

	T.Run("a subject in no entry holds nothing", func(t *testing.T) {
		t.Parallel()

		log := &auditmock.ReaderMock{
			ListFunc: func(
				context.Context, database.SQLQueryExecutor, *audit.Query, *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				return page(), nil
			},
		}

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("a resolver that names no scope reads nothing", func(t *testing.T) {
		t.Parallel()

		log := &auditmock.ReaderMock{}

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, log.ListCalls())
	})

	T.Run("a resolver that refuses", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&auditmock.ReaderMock{}, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})

	T.Run("a read that fails, by actor or by resource", func(t *testing.T) {
		t.Parallel()

		for _, failing := range []string{"actor", "resource"} {
			log := &auditmock.ReaderMock{
				ListFunc: func(
					_ context.Context, _ database.SQLQueryExecutor, query *audit.Query, _ *filtering.QueryFilter,
				) (*filtering.QueryFilteredResult[audit.Entry], error) {
					if (failing == "actor") == (query.ActorID != "") {
						return nil, errReaderUnavailable
					}

					return page(), nil
				},
			}

			collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
			must.NoError(t, err)

			fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
			must.ErrorIs(t, err, errReaderUnavailable, must.Sprintf("failing the %s read", failing))
			test.StrContains(t, err.Error(), firstScope.String())
			test.Nil(t, fragment)
		}
	})
}
