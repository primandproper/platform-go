package identity_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// TestConformance_SQLiteSettlingArchival runs every suite against a deployment
// that settles an owner's accounts when the owner is archived, rather than
// refusing the archival the way this module's store does.
//
// It is the positive control for the archival assertions' second branch. Run
// against this module alone they only ever see the refusal, so without a
// subject that settles instead, the half of the invariant a consumer's
// succession rule is graded against would be compiled and never executed.
func TestConformance_SQLiteSettlingArchival(T *testing.T) {
	T.Parallel()

	db, err := sqlite.NewDatabaseClient(T.Context(),
		&testClientConfig{connectionString: filepath.Join(T.TempDir(), "conformance.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = db.Close() })

	runAgainstStore(T, db, dialect.SQLite, func(store identity.Store) identity.Store {
		return &settlingStore{Store: store}
	})
}

// settlingStore is a deployment's succession rule, in the shape a consumer
// writes one: before an owner is archived, each account they own passes to its
// longest-tenured remaining member, or is closed when nobody else is in it.
type settlingStore struct {
	identity.Store
}

func (s *settlingStore) ArchiveUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (*identity.User, error) {
	memberships, err := s.ListMembershipsForUser(ctx, tx, scope, userID)
	if err != nil {
		return nil, err
	}

	for _, membership := range memberships {
		account, getErr := s.GetAccount(ctx, tx, scope, membership.BelongsToAccount)
		if getErr != nil {
			return nil, getErr
		}

		if account.OwnerUserID != userID {
			continue
		}

		successor, successorErr := s.successor(ctx, tx, scope, account.ID, userID)
		if successorErr != nil {
			return nil, successorErr
		}

		if successor == "" {
			if _, err = s.ArchiveAccount(ctx, tx, scope, account.ID); err != nil {
				return nil, err
			}

			continue
		}

		if err = s.TransferAccountOwnership(ctx, tx, scope, account.ID, successor); err != nil {
			return nil, err
		}
	}

	return s.Store.ArchiveUser(ctx, tx, scope, userID)
}

// successor is the account's longest-tenured member other than leaving, or
// the empty string when there is none. One page, because a harness's rosters
// are a handful of members.
func (s *settlingStore) successor(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID, leaving string,
) (string, error) {
	filter := filtering.DefaultQueryFilter()
	filter.MaxResponseSize = new(filtering.MaxQueryFilterLimit)

	roster, err := s.ListAccountMembers(ctx, tx, scope, accountID, filter)
	if err != nil {
		return "", err
	}

	remaining := slices.DeleteFunc(slices.Clone(roster.Data), func(m *identity.MembershipWithUser) bool {
		return m.BelongsToUser == leaving
	})
	if len(remaining) == 0 {
		return "", nil
	}

	return slices.MinFunc(remaining, func(a, b *identity.MembershipWithUser) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	}).BelongsToUser, nil
}
