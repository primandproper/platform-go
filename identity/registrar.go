package identity

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's Registrar: the three writes that make a registration, each
// through the caller's transaction so that they commit or fail together.
var _ Registrar = (*SQLStore)(nil)

// CreateUser writes a new user through the caller's transaction and answers
// with the row it wrote.
//
// The User it is handed is read and not written to. What comes back is the row
// as the database holds it — the id this write minted, the creation time the
// schema stamped, the defaults the columns supplied — rather than the caller's
// value with a timestamp copied onto it, which is the difference between
// answering from the database and answering from the argument.
func (s *SQLStore) CreateUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	if user == nil {
		return nil, op.Error(ErrNilUser, "creating identity user")
	}

	// The caller's value is left alone, so what the validation, the defaults
	// and the minted id are applied to is a copy. A create that adopted the
	// scope onto the argument would still be writing to it, which is the half
	// of the old shape that was easiest to keep by accident.
	written := *user

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	// Before EnsureDefaults and the validation, because both read the scope: a
	// user that named none is not yet valid, and one that named another
	// directory is refused rather than defaulted into this one.
	if err := adoptScope(scope, &written.Scope, "user"); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	written.EnsureDefaults()

	if err := written.ValidateWithContext(ctx); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	written.ID = newID(written.ID)

	op.Set(userIDKey, written.ID).Set(usernameKey, written.Username)

	if err := s.ensureUsernameFree(ctx, tx, scope, written.Username, ""); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	if err := s.ensureEmailAddressFree(ctx, tx, scope, written.EmailAddress, ""); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	if err := s.q.CreateUser(ctx, tx, createUserParams(&written)); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	// Written through the caller's transaction with the row, so a registration
	// that granted a default service role cannot commit the user without it.
	// That holds because the parameter is a database.Tx: the sentence used to
	// be true only of a caller who had opened one, and nothing stopped a caller
	// who had not.
	//
	// It runs before the read-back rather than after it, so the roles the read
	// attaches are the roles this transaction just wrote.
	if err := s.replaceRoles(ctx, tx, s.userRoleWrites(), written.ID, written.ServiceRoles); err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	// The read-back is the ordinary keyed read rather than a statement of its
	// own. A row this transaction just inserted is not archived, so GetUser
	// reaches it on the transaction that wrote it, and the whole row costs what
	// reading created_at alone used to cost plus the roles — which is what makes
	// the value handed back the value a later read returns rather than a
	// hand-assembled likeness of it. GetUserCreatedAt is gone with the last
	// caller that wanted one column of a row it was about to be given in full.
	created, err := s.readUser(ctx, tx, scope, written.ID)
	if err != nil {
		return nil, op.Error(err, "creating identity user")
	}

	return created, nil
}

// CreateAccount writes a new account through the caller's transaction and
// answers with the row it wrote, leaving the Account it was handed alone — see
// CreateUser.
func (s *SQLStore) CreateAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	account *Account,
) (*Account, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	if account == nil {
		return nil, op.Error(ErrNilAccount, "creating identity account")
	}

	written := *account

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	if err := adoptScope(scope, &written.Scope, "account"); err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	written.EnsureDefaults()

	if err := written.ValidateWithContext(ctx); err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	written.ID = newID(written.ID)

	op.Set(accountIDKey, written.ID)

	if err := s.q.CreateAccount(ctx, tx, createAccountParams(&written)); err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	created, err := s.readAccount(ctx, tx, scope, written.ID)
	if err != nil {
		return nil, op.Error(err, "creating identity account")
	}

	return created, nil
}

// CreateMembership puts a user in an account through the caller's transaction
// and answers with the membership it wrote, leaving the Membership it was
// handed alone — see CreateUser.
//
// The row that comes back is the one that matters more here than anywhere else
// in this package, because the upsert converges: a user rejoining an account
// revives the membership they had, which keeps the id it was created with and
// the moment it was first created. Neither is the value the caller assembled.
func (s *SQLStore) CreateMembership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	membership *Membership,
) (*Membership, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	if membership == nil {
		return nil, op.Error(ErrNilMembership, "creating identity membership")
	}

	written := *membership

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	if err := adoptScope(scope, &written.Scope, "membership"); err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	if err := written.ValidateWithContext(ctx); err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	written.ID = newID(written.ID)

	op.Set(userIDKey, written.BelongsToUser).
		Set(accountIDKey, written.BelongsToAccount)

	// A user's first live membership is their default whatever the value says.
	// A user with memberships and no default has nowhere to land, and it is a
	// state that is easy to write and confusing to debug — GetPrincipal reports
	// ErrNoDefaultAccount and the caller has no obvious way to have caused it.
	existing, err := s.hasLiveMembership(ctx, tx, scope, written.BelongsToUser)
	if err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	if !existing {
		written.DefaultAccount = true
	}

	created, err := s.writeMembership(ctx, tx, &written)
	if err != nil {
		return nil, op.Error(err, "creating identity membership")
	}

	return created, nil
}
