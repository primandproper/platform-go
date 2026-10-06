package recordingcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"
	identitymock "github.com/primandproper/platform-go/v15/identity/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// A subject who owns one account, belongs to another, and acted in somebody
// else's chain: the three sets the two resolvers disagree about.
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
				{ID: "account-joined", OwnerUserID: "user-7"},
			}}, nil
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

func TestConfig_ScopeResolvers(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "user-1"}

	// The point of handing the two out together: the export reaches every
	// chain the subject is on, and the erasure only the ones that are theirs.
	T.Run("filing by subject hands out that rule's pair", func(t *testing.T) {
		t.Parallel()

		directory, log := subjectFixtures()

		collect, erase, err := (&Config{FileBy: FileBySubject}).ScopeResolvers(directory, log)
		must.NoError(t, err)

		q := database.NewTxForTesting(nil)

		collected, err := collect.On(q)(t.Context(), tenancy.Scope{}, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{
			tenancy.Of("user-1"),
			tenancy.Of("account-owned"),
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

		for _, cfg := range []*Config{{FileBy: FileByWrite}, {}} {
			collect, erase, err := cfg.ScopeResolvers(directory, log)
			test.ErrorIs(t, err, ErrNoShippedScopeResolvers)
			test.StrContains(t, err.Error(), string(FileByWrite))
			test.Nil(t, collect)
			test.Nil(t, erase)
		}
	})

	T.Run("refuses at wiring what the resolvers would refuse on first use", func(t *testing.T) {
		t.Parallel()

		directory, log := subjectFixtures()
		cfg := &Config{FileBy: FileBySubject}

		_, _, err := cfg.ScopeResolvers(nil, log)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)

		_, _, err = cfg.ScopeResolvers(directory, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)

		var unset *Config

		_, _, err = unset.ScopeResolvers(directory, log)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})
}
