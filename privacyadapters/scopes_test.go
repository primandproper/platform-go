package privacyadapters_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"
	identitymock "github.com/primandproper/platform-go/v15/identity/mock"
	"github.com/primandproper/platform-go/v15/privacyadapters"
	recordingcfg "github.com/primandproper/platform-go/v15/recording/config"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// A subject who owns one account alone and shares another they own, belongs to
// a third, and acted in somebody else's chain: the sets the two resolvers
// disagree about.
func subjectFixtures() (*identitymock.StoreMock, *auditmock.ReaderMock) {
	directory := &identitymock.StoreMock{
		ListAccountsForUserFunc: func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.Account], error) {
			return &filtering.QueryFilteredResult[identity.Account]{Data: []*identity.Account{
				{ID: "account-owned", OwnerUserID: "user-1"},
				{ID: "account-shared", OwnerUserID: "user-1"},
				{ID: "account-joined", OwnerUserID: "user-7"},
			}}, nil
		},
		ListAccountMembersFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			accountID string,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
			members := []*identity.MembershipWithUser{{BelongsToUser: "user-1"}}
			if accountID != "account-owned" {
				members = append(members, &identity.MembershipWithUser{BelongsToUser: "user-7"})
			}

			return &filtering.QueryFilteredResult[identity.MembershipWithUser]{Data: members}, nil
		},
	}

	log := &auditmock.ReaderMock{
		ListAcrossScopesFunc: func(
			context.Context,
			database.SQLQueryExecutor,
			*audit.Query,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{
				{Scope: tenancy.Of("user-9")},
			}}, nil
		},
	}

	return directory, log
}

func TestAuditScopeResolvers(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "user-1"}

	// The point of handing the two out together: the export reaches every
	// chain the subject is on, and the erasure only the ones that are theirs.
	T.Run("filing by subject hands out that rule's pair", func(t *testing.T) {
		t.Parallel()

		directory, log := subjectFixtures()

		collect, erase, err := privacyadapters.AuditScopeResolvers(recordingcfg.FileBySubject, directory, log)
		must.NoError(t, err)

		q := database.NewTxForTesting(nil)

		collected, err := collect.On(q)(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{
			tenancy.Of("user-1"),
			tenancy.Of("account-owned"),
			tenancy.Of("account-shared"),
			tenancy.Of("account-joined"),
			tenancy.Of("user-9"),
		}, collected)

		erased, err := erase(t.Context(), q, tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of("user-1"), tenancy.Of("account-owned")}, erased)
	})

	T.Run("filing by write has no shipped pair", func(t *testing.T) {
		t.Parallel()

		directory, log := subjectFixtures()

		for _, fileBy := range []recordingcfg.FileBy{recordingcfg.FileByWrite, ""} {
			collect, erase, err := privacyadapters.AuditScopeResolvers(fileBy, directory, log)
			test.ErrorIs(t, err, privacyadapters.ErrNoShippedScopeResolvers)
			test.StrContains(t, err.Error(), string(recordingcfg.FileByWrite))
			test.Nil(t, collect)
			test.Nil(t, erase)
		}
	})

	T.Run("refuses at wiring what the resolvers would refuse on first use", func(t *testing.T) {
		t.Parallel()

		directory, log := subjectFixtures()

		_, _, err := privacyadapters.AuditScopeResolvers(recordingcfg.FileBySubject, nil, log)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)

		_, _, err = privacyadapters.AuditScopeResolvers(recordingcfg.FileBySubject, directory, nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}
