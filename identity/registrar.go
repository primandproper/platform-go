package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's Registrar: the three writes that make a registration, each
// through the caller's transaction so that they commit or fail together.
var _ Registrar = (*SQLStore)(nil)

// CreateUser writes a new user through the caller's transaction.
func (s *SQLStore) CreateUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "creating identity user")
	}

	if user == nil {
		return op.Error(ErrNilUser, "creating identity user")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating identity user")
	}

	// Before EnsureDefaults and the validation, because both read the scope: a
	// user that named none is not yet valid, and one that named another
	// directory is refused rather than defaulted into this one.
	if err := adoptScope(scope, &user.Scope, "user"); err != nil {
		return op.Error(err, "creating identity user")
	}

	user.EnsureDefaults()

	if err := user.ValidateWithContext(ctx); err != nil {
		return op.Error(err, "creating identity user")
	}

	user.ID = newID(user.ID)

	op.Set(userIDKey, user.ID).Set(usernameKey, user.Username)

	if err := s.ensureUsernameFree(ctx, tx, scope, user.Username, ""); err != nil {
		return op.Error(err, "creating identity user")
	}

	if err := s.ensureEmailAddressFree(ctx, tx, scope, user.EmailAddress, ""); err != nil {
		return op.Error(err, "creating identity user")
	}

	if err := s.q.CreateUser(ctx, tx, createUserParams(user)); err != nil {
		return op.Error(err, "creating identity user")
	}

	// The creation time is the database's, and it is read back so the caller's
	// copy agrees with the row rather than holding the zero time — see
	// stampCreatedAt.
	created, readErr := s.q.GetUserCreatedAt(ctx, tx, identitydb.GetUserCreatedAtParams{ID: user.ID})
	if err := stampCreatedAt(&user.CreatedAt, created.CreatedAt, readErr); err != nil {
		return op.Error(err, "creating identity user")
	}

	// Written through the caller's transaction with the row, so a registration
	// that granted a default service role cannot commit the user without it.
	// That holds because the parameter is a database.Tx: the sentence used to
	// be true only of a caller who had opened one, and nothing stopped a caller
	// who had not.
	if err := s.replaceRoles(ctx, tx, s.userRoleWrites(), user.ID, user.ServiceRoles); err != nil {
		return op.Error(err, "creating identity user")
	}

	return nil
}

// CreateAccount writes a new account through the caller's transaction.
func (s *SQLStore) CreateAccount(ctx context.Context, tx database.Tx, scope tenancy.Scope, account *Account) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "creating identity account")
	}

	if account == nil {
		return op.Error(ErrNilAccount, "creating identity account")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating identity account")
	}

	if err := adoptScope(scope, &account.Scope, "account"); err != nil {
		return op.Error(err, "creating identity account")
	}

	account.EnsureDefaults()

	if err := account.ValidateWithContext(ctx); err != nil {
		return op.Error(err, "creating identity account")
	}

	account.ID = newID(account.ID)

	op.Set(accountIDKey, account.ID)

	if err := s.q.CreateAccount(ctx, tx, createAccountParams(account)); err != nil {
		return op.Error(err, "creating identity account")
	}

	created, readErr := s.q.GetAccountCreatedAt(ctx, tx, identitydb.GetAccountCreatedAtParams{ID: account.ID})
	if err := stampCreatedAt(&account.CreatedAt, created.CreatedAt, readErr); err != nil {
		return op.Error(err, "creating identity account")
	}

	return nil
}

// CreateMembership puts a user in an account through the caller's transaction.
func (s *SQLStore) CreateMembership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	membership *Membership,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "creating identity membership")
	}

	if membership == nil {
		return op.Error(ErrNilMembership, "creating identity membership")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating identity membership")
	}

	if err := adoptScope(scope, &membership.Scope, "membership"); err != nil {
		return op.Error(err, "creating identity membership")
	}

	if err := membership.ValidateWithContext(ctx); err != nil {
		return op.Error(err, "creating identity membership")
	}

	membership.ID = newID(membership.ID)

	op.Set(userIDKey, membership.BelongsToUser).
		Set(accountIDKey, membership.BelongsToAccount)

	// A user's first live membership is their default whatever the value says.
	// A user with memberships and no default has nowhere to land, and it is a
	// state that is easy to write and confusing to debug — GetPrincipal reports
	// ErrNoDefaultAccount and the caller has no obvious way to have caused it.
	existing, err := s.hasLiveMembership(ctx, tx, scope, membership.BelongsToUser)
	if err != nil {
		return op.Error(err, "creating identity membership")
	}

	if !existing {
		membership.DefaultAccount = true
	}

	if err = s.writeMembership(ctx, tx, membership); err != nil {
		return op.Error(err, "creating identity membership")
	}

	return nil
}
