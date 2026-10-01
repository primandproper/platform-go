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
		ResourceType: "article",
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

// logOf is a reader double answering List and ListAcrossScopes with one
// function, where a nil scope is the read across every tenant — so each case
// below says what it answers per scope, and the across-scopes read is still
// only reachable through its own method.
func logOf(
	list func(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope *tenancy.Scope,
		query *audit.Query,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[audit.Entry], error),
) *auditmock.ReaderMock {
	return &auditmock.ReaderMock{
		ListFunc: func(
			ctx context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			query *audit.Query,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return list(ctx, q, &scope, query, filter)
		},
		ListAcrossScopesFunc: func(
			ctx context.Context,
			q database.SQLQueryExecutor,
			query *audit.Query,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return list(ctx, q, nil, query, filter)
		},
	}
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

	T.Run("reads each scope by actor and by resource confined to it, and by impersonator across every scope", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		log := logOf(func(
			_ context.Context,
			q database.SQLQueryExecutor,
			scope *tenancy.Scope, query *audit.Query,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			test.EqOp(t, reader, q)

			// Exactly one of the three narrowings, and it is the subject.
			switch {
			case query.ImpersonatorID != "":
				// The one read that spans every scope, confined instead by
				// the subject's own ID.
				test.Nil(t, scope)
				test.EqOp(t, subject.ID, query.ImpersonatorID)
				test.EqOp(t, "", query.ActorID)
				test.EqOp(t, "", query.ResourceID)

				return page(), nil
			case query.ActorID != "":
				// Every other read names its scope. An unconfined List
				// by actor or resource is the operator console's read,
				// and a subject's export is not that.
				must.NotNil(t, scope)
				test.EqOp(t, subject.ID, query.ActorID)
				test.EqOp(t, "", query.ResourceID)

				return page(entry(*scope, "acted_in_"+scope.String(), 1, subject.ID, "article_1")), nil
			default:
				must.NotNil(t, scope)
				test.EqOp(t, subject.ID, query.ResourceID)

				return page(entry(*scope, "acted_on_in_"+scope.String(), 0, "admin_1", subject.ID)), nil
			}
		})

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
		test.SliceLen(t, 1, log.ListAcrossScopesCalls())
	})

	T.Run("an entry the subject acted on themselves is exported once", func(t *testing.T) {
		t.Parallel()

		both := entry(firstScope, "self", 0, subject.ID, subject.ID)

		log := logOf(func(
			context.Context, database.SQLQueryExecutor, *tenancy.Scope, *audit.Query, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return page(both), nil
		})

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

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			if query.ActorID != "" {
				return page(entry(firstScope, "mine", 0, subject.ID, "article_1")), nil
			}

			return page(entry(firstScope, "theirs", 1, "admin_1", subject.ID)), nil
		})

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

	// An operator acted as the subject. The entry is the subject's — filed
	// under them, about their account — and it is exported as what it was: an
	// act somebody else performed under their name, from somebody else's
	// address.
	T.Run("exports an act somebody performed as the subject as impersonated", func(t *testing.T) {
		t.Parallel()

		impersonated := entry(firstScope, "as_them", 0, subject.ID, "article_1")
		impersonated.Actor.Impersonator = "operator_1"

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			if query.ActorID == subject.ID {
				return page(impersonated), nil
			}

			return page(), nil
		})

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)

		test.EqOp(t, subject.ID, collected[0].Actor.ID)
		test.EqOp(t, "operator_1", collected[0].Actor.Impersonator,
			test.Sprint("an impersonated act must not read as the subject's own"))
		test.EqOp(t, "", collected[0].Actor.IP,
			test.Sprint("the address is the operator's, not the subject's"))
		test.EqOp(t, "203.0.113.7", impersonated.Actor.IP,
			test.Sprint("the reader's entry is not the collector's to edit"))
	})

	// The same entry, seen from the operator's side: filed under somebody else,
	// and found only because the collector also reads by impersonator.
	T.Run("exports what the subject did as somebody else, with their own address", func(t *testing.T) {
		t.Parallel()

		asSomebody := entry(firstScope, "as_customer", 0, "customer_1", "article_1")
		asSomebody.Actor.Impersonator = subject.ID

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			if query.ImpersonatorID == subject.ID {
				return page(asSomebody), nil
			}

			return page(), nil
		})

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)

		test.EqOp(t, "customer_1", collected[0].Actor.ID)
		test.EqOp(t, subject.ID, collected[0].Actor.Impersonator)
		test.EqOp(t, "203.0.113.7", collected[0].Actor.IP,
			test.Sprint("the request arrived from the subject, so the address is theirs"))
	})

	// An operator whose own scope is a staff directory: what they did as a
	// customer is filed in the customer's scope, which their resolver does not
	// name, and is still theirs to be shown.
	T.Run("exports what an operator did as somebody else in a scope the resolver did not name", func(t *testing.T) {
		t.Parallel()

		staffScope := tenancy.Of("staff")

		inStaff := entry(staffScope, "own_act", 0, subject.ID, "article_1")

		asCustomer := entry(secondScope, "as_customer_2", 7, "customer_2", "article_2")
		asCustomer.Actor.Impersonator = subject.ID
		asEarlierCustomer := entry(firstScope, "as_customer_1", 3, "customer_1", "article_1")
		asEarlierCustomer.Actor.Impersonator = subject.ID
		asLaterCustomer := entry(firstScope, "as_customer_1_again", 9, "customer_1", "user_9")
		asLaterCustomer.Actor.Impersonator = subject.ID
		asLaterCustomer.Changes = map[string]audit.Change{"email": {Old: "a@example.com", New: "b@example.com"}}

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			switch {
			case query.ImpersonatorID == subject.ID:
				return page(asCustomer, asLaterCustomer, asEarlierCustomer), nil
			case query.ActorID == subject.ID && scope.Owner() == staffScope.Owner():
				return page(inStaff), nil
			default:
				return page(), nil
			}
		})

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(staffScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))

		ids := make([]string, 0, len(collected))
		for i := range collected {
			ids = append(ids, collected[i].ID)
		}

		// The resolved scope first, then the others a scope at a time, each in
		// chain order.
		test.Eq(t, []string{"own_act", "as_customer_1", "as_customer_1_again", "as_customer_2"}, ids)

		must.SliceLen(t, 4, collected)
		test.EqOp(t, "203.0.113.7", collected[1].Actor.IP,
			test.Sprint("the request arrived from the operator, so the address is theirs"))
		test.EqOp(t, audit.Change{}, collected[2].Changes["email"],
			test.Sprint("the values are the customer's, not the operator's"))
	})

	T.Run("exports another person's changed fields without their values", func(t *testing.T) {
		t.Parallel()

		changed := map[string]audit.Change{"email": {Old: "old@example.com", New: "new@example.com"}}

		// The subject changed a colleague's email; somebody changed the subject's.
		onColleague := entry(firstScope, "on_colleague", 0, subject.ID, "user_2")
		onColleague.Changes = changed
		onColleague.Metadata = map[string]string{"reason": "requested by user_2"}

		onSubject := entry(firstScope, "on_subject", 1, "admin_1", subject.ID)
		onSubject.Changes = map[string]audit.Change{"email": {Old: "me@example.com", New: "me2@example.com"}}

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			if query.ActorID != "" {
				return page(onColleague), nil
			}

			return page(onSubject), nil
		})

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		// The values are in no part of the export, not merely absent from the
		// field that decoded.
		test.StrNotContains(t, string(fragment), "old@example.com")
		test.StrNotContains(t, string(fragment), "new@example.com")

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)

		// What the subject did is theirs to see, down to which field it touched.
		test.EqOp(t, "on_colleague", collected[0].ID)
		test.Eq(t, map[string]audit.Change{"email": {}}, collected[0].Changes)
		test.Eq(t, map[string]string{"reason": "requested by user_2"}, collected[0].Metadata)

		// What was done to the subject's own data keeps its values.
		test.EqOp(t, "on_subject", collected[1].ID)
		test.Eq(t, map[string]audit.Change{"email": {Old: "me@example.com", New: "me2@example.com"}}, collected[1].Changes)

		// The reader's entry is not rewritten under it.
		test.Eq(t, audit.Change{Old: "old@example.com", New: "new@example.com"}, changed["email"])
	})

	T.Run("an entry the subject acted on themselves keeps its values", func(t *testing.T) {
		t.Parallel()

		self := entry(firstScope, "self", 0, subject.ID, subject.ID)
		self.Changes = map[string]audit.Change{"name": {Old: "Ann", New: "Anne"}}

		log := logOf(func(
			context.Context, database.SQLQueryExecutor, *tenancy.Scope, *audit.Query, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return page(self), nil
		})

		collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)

		var collected []audit.Entry
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)
		test.Eq(t, map[string]audit.Change{"name": {Old: "Ann", New: "Anne"}}, collected[0].Changes)
	})

	T.Run("pages each read to its end", func(t *testing.T) {
		t.Parallel()

		log := logOf(func(
			_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, filter *filtering.QueryFilter,
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
		})

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

		log := logOf(func(
			context.Context, database.SQLQueryExecutor, *tenancy.Scope, *audit.Query, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return page(), nil
		})

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
			log := logOf(func(
				_ context.Context, _ database.SQLQueryExecutor, scope *tenancy.Scope, query *audit.Query, _ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				if (failing == "actor") == (query.ActorID != "") {
					return nil, errReaderUnavailable
				}

				return page(), nil
			})

			collector, err := privacy.NewCollector(log, &testReader{}, privacy.FixedScopes(firstScope))
			must.NoError(t, err)

			fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
			must.ErrorIs(t, err, errReaderUnavailable, must.Sprintf("failing the %s read", failing))
			test.StrContains(t, err.Error(), firstScope.String())
			test.Nil(t, fragment)
		}
	})
}
