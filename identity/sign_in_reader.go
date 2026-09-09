package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's SignInReader: the two lookups a sign-in form's handles reach
// for, and the read every authenticated request afterwards makes.
var _ SignInReader = (*SQLStore)(nil)

// GetUserByUsername reads a live user by the handle they sign in with.
func (s *SQLStore) GetUserByUsername(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	username string,
) (*User, error) {
	return s.liveUser(ctx, q, scope, "reading identity user by username", func(ctx context.Context) (*User, error) {
		row, err := s.q.GetUserByUsername(ctx, q, identitydb.GetUserByUsernameParams{
			Username: username,
			Scope:    scope,
		})
		if err != nil {
			return nil, err
		}

		return userFromUsernameRow(&row), nil
	})
}

// GetUserByEmailAddress reads a live user by their email address.
func (s *SQLStore) GetUserByEmailAddress(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	emailAddress string,
) (*User, error) {
	return s.liveUser(ctx, q, scope, "reading identity user by email address", func(ctx context.Context) (*User, error) {
		row, err := s.q.GetUserByEmailAddress(ctx, q, identitydb.GetUserByEmailAddressParams{
			EmailAddress: emailAddress,
			Scope:        scope,
		})
		if err != nil {
			return nil, err
		}

		return userFromEmailAddressRow(&row), nil
	})
}

// GetPrincipal reads a user with their memberships and resolves the active
// account.
//
// Four statements on the caller's executor, not one and not a snapshot: the
// user and their service roles, then the memberships and the roles on those.
// The interface's doc carries what that means for a caller — and a caller
// passing a database.Tx gets the one shared snapshot the four otherwise lack.
func (s *SQLStore) GetPrincipal(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (*Principal, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "reading identity principal")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading identity principal")
	}

	user, err := s.readUser(ctx, q, scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading identity principal")
	}

	memberships, err := s.readMembershipsForUser(ctx, q, scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading identity principal")
	}

	principal := &Principal{User: user.Redacted(), Memberships: memberships}

	if principal.ActiveAccountID, err = resolveActiveAccount(memberships, activeAccountID); err != nil {
		return nil, op.Error(err, "reading identity principal")
	}

	op.SpanOnly(accountIDKey, principal.ActiveAccountID)

	return principal, nil
}

// resolveActiveAccount picks the account a request is against.
//
// An empty request is answered with the user's default. A named one must appear
// among the live memberships — this is the check every hand-built session
// context eventually forgets, and forgetting it is what serves one account's
// data to another account's member, because everything downstream then trusts
// the ID it was handed.
func resolveActiveAccount(memberships []*Membership, requested string) (string, error) {
	if requested == "" {
		// The read orders default_account first, so the head is the default when
		// there is one.
		if len(memberships) > 0 && memberships[0].DefaultAccount {
			return memberships[0].BelongsToAccount, nil
		}

		return "", ErrNoDefaultAccount
	}

	for _, membership := range memberships {
		if membership.BelongsToAccount == requested {
			return requested, nil
		}
	}

	return "", platformerrors.Wrapf(ErrMembershipNotFound, "account %q", requested)
}
