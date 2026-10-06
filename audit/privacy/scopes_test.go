package privacy_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/audit/privacy"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"
	identitymock "github.com/primandproper/platform-go/v15/identity/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// directoryOf is an identity directory whose subject belongs to accounts.
func directoryOf(t *testing.T, q database.SQLQueryExecutor, accounts ...*identity.Account) *identitymock.StoreMock {
	t.Helper()

	return &identitymock.StoreMock{
		ListAccountsForUserFunc: func(
			_ context.Context,
			got database.SQLQueryExecutor,
			scope tenancy.Scope,
			userID string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Account], error) {
			test.EqOp(t, q, got)
			test.EqOp(t, tenancy.Global(), scope)
			test.EqOp(t, subject.ID, userID)
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

func TestMembershipScopeResolver(T *testing.T) {
	T.Parallel()

	T.Run("own chain, then accounts, then every chain acted in, once each", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = &testReader{}

		directory := directoryOf(t, q,
			&identity.Account{ID: "acct_1", OwnerUserID: "somebody_else"},
			&identity.Account{ID: "acct_2", OwnerUserID: subject.ID},
		)

		log := &auditmock.ReaderMock{
			ListAcrossScopesFunc: func(
				_ context.Context,
				got database.SQLQueryExecutor,
				query *audit.Query,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[audit.Entry], error) {
				test.EqOp(t, q, got)
				test.Eq(t, &audit.Query{ActorID: subject.ID}, query)

				// Two pages, so a resolver that read only the first would
				// miss the colleague's chain.
				if filter.Cursor == nil {
					first := page(
						entry(tenancy.Of(subject.ID), "e1", 1, subject.ID, "article_1"),
					)
					first.MaxResponseSize = 1

					return first, nil
				}

				return page(
					entry(tenancy.Of("acct_1"), "e2", 1, subject.ID, "article_2"),
					entry(tenancy.Of("colleague_1"), "e3", 4, subject.ID, "colleague_1"),
					entry(tenancy.Global(), "e4", 9, subject.ID, "setting_1"),
				), nil
			},
		}

		scopes, err := privacy.MembershipScopeResolver(directory, log).On(q)(t.Context(), tenancy.Of("acct_9"), subject)
		must.NoError(t, err)

		test.Eq(t, []tenancy.Scope{
			tenancy.Of(subject.ID),
			tenancy.Of("acct_1"),
			tenancy.Of("acct_2"),
			tenancy.Of("colleague_1"),
			tenancy.Global(),
		}, scopes)
		test.SliceLen(t, 2, log.ListAcrossScopesCalls())
		test.SliceLen(t, 0, log.ListCalls())
	})

	T.Run("a subject in nothing resolves to their own chain", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = &testReader{}

		log := logOf(func(context.Context, database.SQLQueryExecutor, *tenancy.Scope, *audit.Query, *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return page(), nil
		})

		scopes, err := privacy.MembershipScopeResolver(directoryOf(t, q), log)(t.Context(), q, tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of(subject.ID)}, scopes)
	})

	T.Run("refuses what it cannot answer for", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = &testReader{}

		log := &auditmock.ReaderMock{}
		directory := &identitymock.StoreMock{}

		_, err := privacy.MembershipScopeResolver(nil, log)(t.Context(), q, tenancy.Scope{}, subject)
		test.ErrorIs(t, err, privacy.ErrNilDirectory)

		_, err = privacy.MembershipScopeResolver(directory, nil)(t.Context(), q, tenancy.Scope{}, subject)
		test.ErrorIs(t, err, privacy.ErrNilReader)

		_, err = privacy.MembershipScopeResolver(directory, log)(t.Context(), nil, tenancy.Scope{}, subject)
		test.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.MembershipScopeResolver(directory, log)(t.Context(), q, tenancy.Scope{}, dataprivacy.Subject{})
		test.ErrorIs(t, err, privacy.ErrAnonymousSubject)
	})

	T.Run("a failing read is reported rather than resolved short", func(t *testing.T) {
		t.Parallel()

		var q database.SQLQueryExecutor = &testReader{}

		log := logOf(func(context.Context, database.SQLQueryExecutor, *tenancy.Scope, *audit.Query, *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return nil, errReaderUnavailable
		})

		_, err := privacy.MembershipScopeResolver(directoryOf(t, q), log)(t.Context(), q, tenancy.Scope{}, subject)
		test.ErrorIs(t, err, errReaderUnavailable)
		test.StrContains(t, err.Error(), "chains the subject acted in")

		failing := &identitymock.StoreMock{
			ListAccountsForUserFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter) (*filtering.QueryFilteredResult[identity.Account], error) {
				return nil, errReaderUnavailable
			},
		}

		_, err = privacy.MembershipScopeResolver(failing, log)(t.Context(), q, tenancy.Scope{}, subject)
		test.ErrorIs(t, err, errReaderUnavailable)
		test.StrContains(t, err.Error(), "accounts the subject belongs to")
	})
}
