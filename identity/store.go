package identity

import (
	"context"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// Agreement names a document a user can accept.
//
// It is a closed set rather than a free string because the columns are fixed —
// a third document is a migration, not a new value — and because a typo in a
// string would record an acceptance nothing ever reads.
type Agreement string

const (
	// TermsOfService is the terms-of-service document.
	TermsOfService Agreement = "terms_of_service"

	// PrivacyPolicy is the privacy policy.
	PrivacyPolicy Agreement = "privacy_policy"
)

// Valid reports whether a is a known agreement.
func (a Agreement) Valid() bool {
	switch a {
	case TermsOfService, PrivacyPolicy:
		return true
	default:
		return false
	}
}

// Principal is a user together with the memberships that say what they may do:
// everything an authorization check, a session, or a request context needs
// about who is calling, assembled by one call.
//
// It exists because every application builds this by hand out of three queries
// on the hottest path it has — the one every authenticated request runs — and
// the hand-built version is where the active-account check gets forgotten. A
// Principal is only ever returned for an account the user is actually a member
// of; there is no way to obtain one that claims an account it has no membership
// in, which is the failure this type is shaped to prevent.
type Principal struct {
	_ struct{} `json:"-"`

	// User is who is calling, redacted — a Principal reaches a request context
	// and frequently a session store, and neither is a place for a password
	// hash. Read the credential fields through GetUser when a sign-in flow
	// genuinely needs them.
	User *User `json:"user"`

	// ActiveAccountID is the account this request is against: the one asked
	// for, or the user's default when none was.
	ActiveAccountID string `json:"activeAccountID"`

	// Memberships are every live membership the user holds, in this scope.
	Memberships []*Membership `json:"memberships"`
}

// ActiveMembership returns the membership for ActiveAccountID.
//
// It cannot be nil for a Principal a Store returned — resolving the active
// account is what the Store did to build one — so a nil here means the value
// was assembled by hand.
func (p *Principal) ActiveMembership() *Membership {
	if p == nil {
		return nil
	}

	for _, m := range p.Memberships {
		if m.BelongsToAccount == p.ActiveAccountID {
			return m
		}
	}

	return nil
}

// AccountRoles returns the role names the user holds in the active account.
func (p *Principal) AccountRoles() []string {
	if m := p.ActiveMembership(); m != nil {
		return m.Roles
	}

	return nil
}

// ServiceRoles returns the role names the user holds outside any account.
func (p *Principal) ServiceRoles() []string {
	if p == nil || p.User == nil {
		return nil
	}

	return p.User.ServiceRoles
}

// Roles returns every role name that applies to this request — the user's
// service roles followed by the roles they hold in the active account.
//
// It is what an authorization.PolicyResolver's PermissionsForRoles takes, and it
// is one method rather than two so that a caller cannot resolve permissions from
// half the answer. Passing only the account roles is the mistake that makes an
// operator's support access stop working inside a customer's account; passing
// only the service roles is the one that gives every user the same permissions
// everywhere.
func (p *Principal) Roles() []string {
	service, account := p.ServiceRoles(), p.AccountRoles()

	roles := make([]string, 0, len(service)+len(account))
	roles = append(roles, service...)
	roles = append(roles, account...)

	return roles
}

// AccountIDs returns every account the user belongs to, in the order the
// memberships were read — default account first.
func (p *Principal) AccountIDs() []string {
	if p == nil {
		return nil
	}

	ids := make([]string, 0, len(p.Memberships))
	for _, m := range p.Memberships {
		ids = append(ids, m.BelongsToAccount)
	}

	return ids
}

// Registrar writes the rows that make a new account exist.
//
// All three run in the caller's one transaction: a user without an account signs
// in to nothing, and an account without an owner has no one its ownership checks
// can resolve to. That is why they are one interface rather than three methods
// spread across the reader and writer seams — the grouping is what makes the
// invariant visible, and the parameter type is what makes it hold.
//
// Every write in this package takes a database.Tx now, so what used to be this
// interface's distinguishing property is the store's; what survives here is the
// grouping. See Store for the shape and for why it is the compiler's obligation
// rather than a doc comment's.
type Registrar interface {
	// CreateUser writes a new user through the caller's transaction.
	//
	// It takes a transaction because a registration is a user, an account, and a
	// membership, and a user who exists without an account is a user who signs
	// in to nothing. CreateAccount and CreateMembership take the same executor;
	// see the package documentation for the shape.
	//
	// The scope is the argument rather than User.Scope. A user naming none
	// adopts it; one naming a different directory is ErrScopeMismatch rather
	// than being moved into this one — see Store.
	//
	// The ID is generated if the user carries none, and both it and CreatedAt
	// are written back onto the value. A username or email address already
	// registered in this scope returns an error wrapping ErrUsernameTaken or
	// ErrEmailAddressTaken rather than a driver's constraint violation — the
	// caller's next move differs, and asking them to parse a SQLSTATE to find
	// out is how that check gets skipped.
	//
	// CreatedAt is the database's rather than the Store's clock: the column is
	// not in the insert and the schema defaults it, so that a row's creation
	// time and the filter window comparing against it come from one clock
	// rather than from however many application instances are writing. The
	// create reads it back, so the value the caller is holding is the value in
	// the row — a caller that serialized the struct straight into a response
	// would otherwise be rendering the zero time as a date.
	CreateUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error

	// CreateAccount writes a new account through the caller's transaction. The
	// ID is generated if the account carries none, and CreatedAt is read back
	// from the row — see CreateUser.
	CreateAccount(ctx context.Context, tx database.Tx, scope tenancy.Scope, account *Account) error

	// CreateMembership puts a user in an account through the caller's transaction.
	//
	// A user's first live membership becomes their default account whatever the
	// value says, because a user with memberships and no default has nowhere to
	// land — a state that is easy to write and confusing to debug. A subsequent
	// membership marked default moves the flag, which is what SetDefaultAccount
	// does and what accepting an invitation into a first account relies on.
	//
	// Both endpoints must live in the membership's own scope, and a user or an
	// account that does not returns an error wrapping ErrUserNotFound or
	// ErrAccountNotFound. The foreign keys prove that the ids exist somewhere,
	// which for a multi-directory deployment is not the question being asked —
	// a membership across two directories puts a stranger on a roster.
	CreateMembership(ctx context.Context, tx database.Tx, scope tenancy.Scope, membership *Membership) error
}

// CredentialStore is where the authentication engines put what they produce.
//
// This package never hashes, never compares, and never generates a TOTP secret;
// argon2, totp and webauthn do that and store nothing. These are the methods
// that persist their results, and they are separate from ProfileWriter for a
// reason that is a security property rather than tidiness: a credential must
// never be written by a read-modify-write over a whole User, because a caller
// writing back a value it read before a password rotation would restore the old
// hash. Every method here writes exactly one fact.
type CredentialStore interface {
	// GetUserByEmailVerificationToken reads the user a verification link names.
	// A token that has already been used matches nobody, because verifying
	// clears it.
	GetUserByEmailVerificationToken(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, token string) (*User, error)

	// UpdateUserPassword replaces the stored hash, stamps PasswordLastChangedAt,
	// and clears RequiresPasswordChange.
	//
	// Clearing the flag here rather than leaving it to the caller is what makes
	// a forced password change terminate: a flow that set the flag, prompted,
	// wrote the new hash, and forgot to clear it prompts again forever.
	UpdateUserPassword(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, hashedPassword string,
	) error

	// SetUserRequiresPasswordChange forces or releases a password change at next
	// sign-in.
	SetUserRequiresPasswordChange(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		requires bool,
	) error

	// UpdateUserTwoFactorSecret stores a new TOTP secret and marks it
	// unverified.
	//
	// Unverified is not optional and not a parameter. A newly issued secret has
	// by definition not been proven, and a method that let the caller say
	// otherwise would let a flow enroll a second factor nobody ever demonstrated
	// possession of.
	UpdateUserTwoFactorSecret(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, secret string,
	) error

	// MarkUserTwoFactorSecretVerified records that the user proved possession of
	// the secret they hold.
	MarkUserTwoFactorSecretVerified(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) error

	// SetUserEmailAddressVerificationToken stores the token a verification link
	// will carry, replacing any outstanding one — so re-sending a verification
	// email invalidates the previous link rather than leaving two live — and
	// dropping any proof the address already had, so the row never says both
	// "proven" and "a link is outstanding".
	//
	// A flow that changes an address and then verifies it mints the link in that
	// order. UpdateUser burns the outstanding token along with the stamp,
	// because the column records that a link was mailed and not which address it
	// went to, so a token minted before the change is one this package cannot
	// tell apart from a token minted for the address being left behind.
	SetUserEmailAddressVerificationToken(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, token string,
	) error

	// MarkUserEmailAddressVerified stamps the address as proven and clears the
	// token, so the link cannot be replayed.
	//
	// The token is a parameter and is compared in the statement's predicate: the
	// caller has already read the user by token, and re-checking it here is what
	// makes a verification that raced another one write once.
	MarkUserEmailAddressVerified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, token string,
	) error

	// MarkUserEmailAddressUnverified withdraws the proof from an address the
	// user keeps — an administrator acting on a bounce, a support decision, a
	// deliverability sweep.
	//
	// It takes no token and names no value the row must still hold, because
	// unlike the three guarded writes it is not answering anything: it is the
	// safe direction, and an unverify that raced another one has still left the
	// row where both callers wanted it. Any outstanding link survives, since it
	// was minted for this address and the address has not moved.
	MarkUserEmailAddressUnverified(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) error
}

// SignInReader is the read side of authenticating a request.
//
// Two lookups by the handles a sign-in form submits, and the one read every
// authenticated request afterwards makes. It is deliberately the smallest
// interface here, because it is the one a middleware depends on, and a
// middleware that can only do these three things cannot accidentally do
// anything else.
type SignInReader interface {
	// GetUserByUsername reads a user by the handle they sign in with. Archived
	// users are excluded: this is the sign-in read, and a deleted account must
	// not authenticate.
	GetUserByUsername(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, username string) (*User, error)

	// GetUserByEmailAddress is GetUserByUsername for the address. It is the read
	// a password-reset flow starts from.
	GetUserByEmailAddress(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, emailAddress string) (*User, error)

	// GetPrincipal reads a user with their memberships and resolves which
	// account the request is against.
	//
	// One call, four statements, no shared snapshot. The user, the service
	// roles they hold outside any account, their memberships, and the roles on
	// those memberships are four reads, each taken from the read side on its
	// own rather than from a single transaction — so a write landing partway
	// through shows in whichever of the four have yet to run and not in the
	// ones already back. That is the shape to size a connection pool and a
	// consistency expectation against, on the path every authenticated request
	// runs. Whether it should become fewer statements is a question about this
	// method rather than about its callers: the answer changes here and nothing
	// above it moves.
	//
	// An empty activeAccountID means the user's default account. A named one
	// must be an account the user is a live member of; otherwise the read
	// returns an error wrapping ErrMembershipNotFound rather than a Principal
	// with an account the caller asked for and has no right to. That check is
	// the reason this method exists rather than the caller joining the pieces:
	// it is the one every hand-built session context eventually forgets, and
	// forgetting it hands one account's data to another account's member.
	GetPrincipal(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID, activeAccountID string,
	) (*Principal, error)
}

// DirectoryReader reads the directory without changing it.
//
// Users, accounts, and the memberships between them, all scoped and all paged
// through filtering.QueryFilter where they return more than one row. Nothing
// here writes, so a console, an export, or a support tool can be handed this
// and nothing else.
type DirectoryReader interface {
	// GetUser reads one of the scope's live users, credentials included. It
	// returns an error wrapping ErrUserNotFound when the user does not exist —
	// including when they exist in another scope, which is the same answer as
	// far as this scope is concerned.
	//
	// Archived users are not returned. Reading one row by id is not a filtered
	// list, and a caller that wants an archived user back wants a different
	// read rather than a flag on this one: ListUsers with IncludeArchived is
	// that read. This is a change — the statement behind it used to carry no
	// archived clause — and it reaches every read by id, GetPrincipal included.
	GetUser(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string) (*User, error)

	// ListUsers pages the scope's directory, users redacted.
	//
	// Ordered by id and cursor-paginated on it, which is what makes the page and
	// the cursor name a position in the same order. The filter's created and
	// updated windows and its IncludeArchived flag all apply, and both counts
	// come back with the page rather than from a second query that would be
	// counting a table the page has already moved on from.
	//
	// The filter's SortBy decides which way that order runs: an id sorts by
	// creation time, so "desc" is newest first. It reaches every paged read on
	// this interface, and none of them defaults to anything but ascending.
	ListUsers(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[User], error)

	// ListUsersByIDs reads a batch of the scope's users in one query, redacted,
	// skipping IDs that name nobody in this scope.
	//
	// It exists because the alternative is a loop around GetUser, and every
	// application has the read that needs it: rendering a list where each row
	// names the user who created it. A partial answer rather than an error for a
	// missing ID is deliberate — the caller is hydrating references, and one
	// deleted author should not empty the page.
	ListUsersByIDs(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userIDs []string,
	) ([]*User, error)

	// SearchUsersByUsername pages the users in scope whose username begins with
	// prefix, redacted.
	//
	// A prefix match rather than a substring one, because a prefix uses the
	// index on (scope, username) and a substring cannot. An application that
	// needs fuzzy search over its directory wants this module's search package
	// pointed at it, not a LIKE '%x%' that scans the table on every keystroke.
	//
	// The page is ordered by the username rather than by the id, so its cursor
	// is a username and the filter's SortBy reverses the alphabet rather than
	// the creation order — which is what a direction means for a read ordered by
	// something other than an id.
	SearchUsersByUsername(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		prefix string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[User], error)

	// GetAccount reads one of the scope's live accounts, returning an error
	// wrapping ErrAccountNotFound when it does not exist here.
	//
	// Archived accounts are not returned, as archived users are not and for the
	// same reason — see GetUser. A caller reconciling an invoice against a
	// closed account reaches it through ListAccounts with IncludeArchived, and
	// checks Account.Archived on what comes back.
	GetAccount(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, accountID string) (*Account, error)

	// ListAccounts pages the scope's accounts, ordered by id and filtered
	// through the whole QueryFilter — see ListUsers.
	ListAccounts(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Account], error)

	// ListAccountsForUser pages the accounts a user is a live member of.
	ListAccountsForUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Account], error)

	// GetMembership reads the live membership between a user and an account,
	// returning an error wrapping ErrMembershipNotFound when there is none.
	GetMembership(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID, accountID string,
	) (*Membership, error)

	// ListMembershipsForUser returns every live membership a user holds, default
	// account first.
	//
	// It returns a slice rather than a page because a user belongs to a handful
	// of accounts, and paging a handful means a caller who forgets to loop reads
	// some of somebody's memberships and authorizes against the rest as if they
	// did not exist.
	ListMembershipsForUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) ([]*Membership, error)

	// ListAccountMembers pages an account's roster, each membership joined to
	// the redacted user it names.
	ListAccountMembers(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		accountID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[MembershipWithUser], error)
}

// ProfileWriter writes what a user or an account may change about itself.
//
// Names, addresses, time zones, and the record of an accepted document — the
// fields where losing a concurrent write costs a stale display name rather than
// a security regression. What is deliberately not reachable from here:
// credentials (CredentialStore), billing state (BillingWriter), account
// ownership (MembershipWriter), and status (AdminWriter). UpdateUser and
// UpdateAccount ignore those columns entirely rather than trusting the caller
// to have read them recently.
type ProfileWriter interface {
	// UpdateUser writes the user's profile: username, email address, and names.
	//
	// It cannot write a credential, deliberately. A caller holding a User read
	// some time ago and writing it back would otherwise restore a password that
	// has since been changed — the classic read-modify-write, on the one field
	// where losing the newer value is a security regression rather than a stale
	// display name. Credentials move through UpdateUserPassword,
	// UpdateUserTwoFactorSecret, and their siblings.
	//
	// Changing the username or email address to one already registered in this
	// scope returns ErrUsernameTaken or ErrEmailAddressTaken. Changing the email
	// address clears its verification: the new address has not been proven. The
	// outstanding verification token goes with it, in the same statement — the
	// column records that a link was mailed, not which address it went to, so a
	// token that survived the move would let the link sent to the old address
	// prove the new one. A flow that means to verify the new address issues a
	// fresh link after this write, not before.
	//
	// Neither verification column is a parameter. Both are read from the row and
	// decided here, and whatever the User in hand carries in them is ignored.
	//
	// A redacted user round-trips. The value every bulk read and Principal.User
	// hands back has its credentials cleared, so the obvious profile handler —
	// take the principal's user, change the name, save — is the one that has to
	// work; it validates the columns it assigns rather than the whole user, and
	// a caller therefore needs neither a password hash nor a status to save a
	// display name.
	//
	// The scope is the argument rather than User.Scope, as it is at creation.
	UpdateUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error

	// UpdateAccount writes the account's name and billing address.
	//
	// It writes neither the billing state nor the owner, for the same reason
	// UpdateUser writes no credential: both are changed by flows that do not
	// hold the rest of the account, and a read-modify-write over them loses
	// whatever a processor webhook or an ownership transfer did in between. See
	// BillingWriter and TransferAccountOwnership.
	UpdateAccount(ctx context.Context, tx database.Tx, scope tenancy.Scope, account *Account) error

	// RecordAgreement stamps the user's acceptance of one or more documents, as
	// of the Store's clock.
	//
	// It runs on the caller's transaction like every other write here, so a
	// registration that collects acceptance at sign-up may either call it inside
	// that transaction or set LastAcceptedTermsOfService and
	// LastAcceptedPrivacyPolicy on the User and let CreateUser write them with
	// the row. The second is still one statement fewer. What this method is for
	// is the later acceptance, when a new version of a document is published and
	// the user agrees to it in a request of its own.
	//
	// Naming several documents stamps them all with one clock read, so accepting
	// two in one call records one moment rather than two a later comparison
	// could order.
	RecordAgreement(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		agreements ...Agreement,
	) error
}

// MembershipWriter changes who belongs to an account and what they may do
// there.
//
// This is the authorization-shaped half of the store: the roles these methods
// write are the roles a PolicyResolver later reads. It is separated from
// AdminWriter because the two answer to different people — an account
// administrator manages their own roster, an operator manages the deployment —
// and a service that exposes the first should not be holding the second.
type MembershipWriter interface {
	// SetMembershipRoles replaces the roles a user holds in an account.
	//
	// It replaces rather than merges. A caller adding a role reads the
	// membership and writes the union, which is visible in their code; a merging
	// setter makes removing a role impossible through the same method and is the
	// reason revocation flows end up issuing raw SQL.
	//
	// An empty set is refused with errors.ErrEmptyInputParameter for everybody
	// but the account's owner, who may hold none: ownership is itself the
	// standing, and it is the role set TransferAccountOwnership mints when it
	// makes a non-member the owner. For anybody else a roleless membership is a
	// user who belongs to an account and may do nothing in it, which surfaces as
	// an authorization bug far from the call that wrote it. Removing somebody is
	// RemoveMembership.
	SetMembershipRoles(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, accountID string,
		roles []string,
	) error

	// SetDefaultAccount marks one of a user's accounts as the one they land in,
	// clearing the flag from the others in the same transaction — the invariant
	// being one default per user, not one per call.
	SetDefaultAccount(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, accountID string,
	) error

	// TransferAccountOwnership moves an account to a new owner, giving the new
	// owner a membership if they lack one and leaving the old owner's in place.
	//
	// The old owner keeps their membership deliberately: transferring ownership
	// and ejecting somebody are different acts, and doing both here would make
	// the common case — handing over to a colleague and staying on — impossible
	// to express.
	//
	// The new owner must be a live user in the account's scope; one who is not
	// returns an error wrapping ErrUserNotFound, the same answer a read of them
	// from here would give.
	//
	// A minted membership carries no roles, because ownership is the standing
	// and this package does not know what a role of yours means. A new owner who
	// was already a member keeps the roles they had. Either way, granting them
	// more is SetMembershipRoles.
	//
	// A minted membership is the new owner's default when it is the first they
	// hold anywhere, which is the rule CreateMembership and AcceptInvitation
	// apply to the memberships they mint. A new owner who already belongs
	// somewhere keeps the default they chose.
	TransferAccountOwnership(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		accountID, newOwnerUserID string,
	) error

	// RemoveMembership ends a user's membership in an account.
	//
	// Removing the account's owner returns an error wrapping
	// ErrLastAccountOwner. An ownerless account fails every permission check
	// that resolves through its owner, and the failure surfaces far from the
	// removal that caused it. Transfer ownership first.
	//
	// Removing a user's default account moves the default to another live
	// membership, so a user is never left with memberships and nowhere to land.
	RemoveMembership(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, accountID string,
	) error
}

// AdminWriter is the operator's half: the writes an account holder cannot make
// about themselves.
//
// Banning, terminating, granting a service role, soft-deleting, and erasing.
// These are the methods whose exposure through an ordinary request handler is a
// privilege escalation, so they are named as a group precisely to make that
// exposure a deliberate act rather than a consequence of depending on Store.
type AdminWriter interface {
	// UpdateUserAccountStatus moves a user between statuses, recording the
	// explanation shown to them.
	UpdateUserAccountStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		status AccountStatus,
		explanation string,
	) error

	// SetUserServiceRoles replaces the roles a user holds outside any account.
	//
	// It replaces rather than merges, for the reason SetMembershipRoles does: a
	// merging setter cannot revoke, and revocation is the operation that matters
	// most on the role set that grants operator access. An empty set is allowed
	// here where an empty membership role set is not — a user with no service
	// roles is the ordinary case, and it is how an operator's access is
	// withdrawn.
	SetUserServiceRoles(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		roles []string,
	) error

	// ArchiveUser soft-deletes a user and ends every membership they hold, in
	// one transaction. A user archived with live memberships would still appear
	// in the accounts they belonged to, which is the state an application
	// discovers when a deleted colleague is still on the roster.
	//
	// Archiving a user who still owns a live account returns an error wrapping
	// ErrLastAccountOwner, naming the account. It is the guard RemoveMembership
	// carries, for the identical failure: the account stays live and answers to
	// a user every scoped read now reports as absent, and the ownership checks
	// that resolve through it fail somewhere else entirely. Transfer the account
	// or archive it first.
	ArchiveUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) error

	// EraseUser destroys the user row through the caller's transaction, returning
	// how many rows went.
	//
	// Every write here takes the caller's transaction, and this is the one where
	// the reason predates the convention: a right-to-be-forgotten erasure shares
	// one transaction with every other domain's eraser and with the record that
	// the erasure happened. A subject erased from the directory and present in
	// another domain's table has no coherent status, so the whole thing has to be
	// able to roll back together. See this module's dataprivacy package.
	//
	// Unlike ArchiveUser it cannot refuse, and so it does not: accounts the
	// subject owned survive the erasure with an owner_user_id that resolves to
	// nothing. That is the post-condition rather than an oversight — an erasure
	// a store could decline would make a subject's rights conditional on an
	// account, and archiving those accounts here would take other members
	// offline because one of them exercised a right. Resolve the subject's
	// accounts before erasing them.
	EraseUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error)

	// ArchiveAccount soft-deletes an account and ends every membership in it, in
	// one transaction.
	//
	// A member whose default account this was has their default moved to
	// another live membership, which is what RemoveMembership does for the one
	// member it removes: an account going away is that removal performed on
	// everybody at once, and neither leaves a user with memberships and nowhere
	// to land. A member who belonged to nothing else keeps no default, because
	// there is no membership left to point at.
	ArchiveAccount(ctx context.Context, tx database.Tx, scope tenancy.Scope, accountID string) error
}

// BillingWriter is what a payment processor's webhook handler needs, and
// nothing else.
//
// One method per billing event, and the events are what an application handles
// rather than what the table holds: a processor reports that a subscription is
// current on a plan, or that it has ended, or a reconciliation runs and finds
// nothing moved, or an operator suspends an account, or the account is created
// at the processor for the first time. A webhook endpoint is unauthenticated
// and public, so what it can reach is worth being able to enumerate.
//
// It used to be one method taking a struct of four optional fields, which is the
// same set of writes with two differences that both cost. The statement's SET
// list was assembled per call, so it was dynamic SQL: there was no static text
// for sqlc to check or for identity/internal/queries to emit. And the encoding
// of "leave this alone" was a nil, which on the one nullable column of the four
// collided with the value a cancellation has to write — an update naming no plan
// and an update cancelling the plan were the same struct.
//
// Each method writes the columns its event moves and no others, so a delivery
// carrying a standing cannot silently restate a plan; and the columns one event
// does move go in one statement, so there is no intermediate state in which an
// account is paid on the plan it just left.
type BillingWriter interface {
	// RecordAccountSubscription writes what a processor delivery reported: the
	// standing, the plan, and the reconciliation, together. The plan is
	// required — an ended subscription is RecordAccountSubscriptionEnded, so a
	// handler passing through an unchecked payload cannot cancel a subscription
	// while believing it renewed one.
	RecordAccountSubscription(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		accountID string,
		status BillingStatus,
		planID string,
	) error

	// RecordAccountSubscriptionEnded writes the delivery that says there is no
	// subscription any more: the new standing, no plan, and the reconciliation.
	// The account is left on no plan rather than on the one it stopped paying
	// for, which is what an entitlement check downstream would otherwise read.
	RecordAccountSubscriptionEnded(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		accountID string,
		status BillingStatus,
	) error

	// SetAccountBillingStatus moves an account between standings without
	// touching its plan or its reconciliation stamp. This is the operator's
	// move — a suspension, which no processor reports and so has nothing to
	// stamp.
	SetAccountBillingStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		accountID string,
		status BillingStatus,
	) error

	// SetAccountPaymentProcessorCustomerID attaches the account to its customer
	// at the processor, which is the write that happens the first time it is
	// created there. The identifier is required.
	SetAccountPaymentProcessorCustomerID(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		accountID, customerID string,
	) error

	// MarkAccountBillingSynced stamps a reconciliation that moved nothing, as of
	// the Store's clock. It is the write a reconciler owes the next run: without
	// it, an account that has been current for a year is indistinguishable from
	// one nobody has looked at since.
	MarkAccountBillingSynced(ctx context.Context, tx database.Tx, scope tenancy.Scope, accountID string) error
}

// InvitationStore covers an invitation's whole life: issued, looked up,
// answered.
//
// AcceptInvitation is the one whose two writes have to commit together — a
// membership write and a status write — and it takes the roles off the
// invitation rather than from a parameter: what somebody was invited to is what
// they get.
type InvitationStore interface {
	// CreateInvitation writes an invitation. The ID is generated if it carries
	// none, and CreatedAt is read back from the row — see Registrar.CreateUser.
	//
	// Note is the sender's message and is written here; StatusNote is the
	// answer's and is not. An invitation carrying one at creation is refused
	// with an error wrapping errors.ErrUnrecognizedInputValue, for the same
	// reason one carrying a terminal status is: nothing has answered it yet.
	CreateInvitation(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		invitation *Invitation,
	) error

	// GetInvitation reads one of the scope's live invitations by ID, for the
	// sender looking at what they have sent. An archived one is not returned —
	// see DirectoryReader.GetUser.
	GetInvitation(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		invitationID string,
	) (*Invitation, error)

	// GetInvitationByToken reads the invitation a link names, comparing the
	// token.
	//
	// The ID and the token are both required, and the ID is what the row is
	// found by. Looking up by token alone would make the token an index key —
	// which is a secret in an index and a timing signal on every miss — where
	// naming the row first means one constant-time-ish comparison against one
	// value.
	//
	// An expired invitation returns an error wrapping ErrInvitationExpired
	// rather than ErrInvitationNotFound, so the recipient can be told to ask for
	// another rather than that their link was wrong.
	GetInvitationByToken(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		invitationID, token string,
	) (*Invitation, error)

	// ListInvitationsFromUser pages the pending invitations a user has sent,
	// redacted.
	ListInvitationsFromUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Invitation], error)

	// ListInvitationsForEmailAddress pages the pending invitations addressed to
	// an email address, redacted — what a newly registered user is shown.
	ListInvitationsForEmailAddress(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		emailAddress string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Invitation], error)

	// AcceptInvitation marks an invitation accepted and writes the membership it
	// promised, through the caller's transaction, returning that membership.
	//
	// The two are one call rather than two because an accepted invitation
	// without a membership is a user who was told they joined and did not, and
	// the transaction is the caller's because a registration that accepts an
	// invitation writes the user in the same one.
	//
	// The roles come from the invitation rather than from a parameter: what
	// somebody was invited to is what they get, and a parameter here is where an
	// escalation goes in.
	//
	// The accepting user must be a live user in the invitation's scope, and one
	// who is not returns an error wrapping ErrUserNotFound rather than a
	// membership spanning two directories.
	//
	// statusNote is why the answer went the way it did, and it lands in
	// Invitation.StatusNote. The sender's Note is untouched — an invite email's
	// message is still readable beside the acceptance that answered it.
	AcceptInvitation(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		invitationID, token, acceptingUserID, statusNote string,
	) (*Membership, error)

	// SetInvitationStatus answers an invitation without producing a membership:
	// rejection by the recipient, cancellation by the sender.
	//
	// It refuses InvitationAccepted, returning ErrInvalidInvitationStatus —
	// accepting is AcceptInvitation, and a status write that produced no
	// membership would leave exactly the state that method exists to prevent.
	//
	// statusNote is the answer's, and lands in Invitation.StatusNote beside a
	// sender's Note it does not touch.
	SetInvitationStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		invitationID string,
		status InvitationStatus,
		statusNote string,
	) error
}

// Store is the whole persistence seam for the identity directory: every
// interface above, in one name.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL
// it needs (identity/migrations), so adopting identity does not mean writing
// this. The interface exists because an application with its own schema
// conventions — or one whose directory is LDAP, or an upstream identity
// provider it mirrors — should not have to fork the package to keep them.
//
// # Depend on a narrower one
//
// Store is forty-eight methods, which is the right size for the thing that
// implements it and the wrong size for almost everything that calls it. It is a
// union of nine interfaces, each named for a job rather than for a table, and a
// caller should name the smallest one that covers what it does: a sign-in
// middleware takes a SignInReader, a processor webhook takes a BillingWriter, a
// support console takes a DirectoryReader.
//
// This is not only about the size of a test double, though a three-method fake
// beats a forty-eight-method mock. It is that the narrow interface is a
// statement about reach, checked by the compiler: a handler that holds a
// DirectoryReader cannot ban a user, and one that holds a ProfileWriter cannot
// write a credential or move an account's ownership. Depending on Store gives
// away all of that at once, so it is what a container registers and a store
// implements, not what a handler asks for.
//
// A SQL implementation is deliberately one type behind all nine. The writes
// that make a registration span three tables in one transaction, so splitting
// the implementation would split a transaction; only the seam divides. That the
// transaction now arrives from outside does not change it — the caller supplies
// one Tx, and three stores holding three handles could not all run on it.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. No write here opens a transaction of its own,
// and there is no variant of one that does.
//
// Five of these writes always took a Tx, for reasons that read as local and were
// not: a registration is three rows, an accepted invitation is two, and an
// erasure spans every domain a subject appears in. What the other twenty-five
// had instead was a transaction each, which is the same argument answered
// wrongly — a consumer's write almost never travels alone. Every dinnerdonebetter
// write carries an audit entry and a data change event in the row's transaction,
// so a credential rotation that opened its own left its provenance in a second
// one, and a refused audit entry left the new password hash committed with
// nothing recording who changed it.
//
// TransferAccountOwnership is the sharpest instance and needs no consumer to
// see: it is two membership writes that must commit together, and while it owned
// the transaction they shared, they were reachable together and in no other
// combination.
//
// A database.Tx is producible only by database.RunInTransaction, so the
// obligation is the compiler's rather than a doc comment's. A caller with
// nothing to join opens one with Client.WithTransaction and passes the Tx it is
// handed.
//
// The read takes the wider type so that one method serves both moments. A
// sign-in middleware holds no transaction and passes Client.Reader(); a
// registration that has just written a user passes the Tx it wrote through, and
// sees it. A read narrowed to Tx would have forced the first caller into a
// transaction it has no use for, and one narrowed to Client.Reader() would have
// read a database that does not yet hold the row its caller just wrote.
//
// That widening is load-bearing rather than incidental, because some of these
// reads are the checks the writes make. CreateUser's uniqueness checks,
// UpdateUser's read of the address it is replacing, TransferAccountOwnership's
// read of the new owner, and AcceptInvitation's read of the invitation all run
// on the executor their write runs on — which is what lets a registration
// mint a user and read them back inside one transaction, and what keeps a
// uniqueness check from reporting a handle free that the same transaction has
// already taken.
//
// A Store that is not a SQL store still takes these types; an implementation
// with no transaction of its own ignores the executor, and the seam stays one
// signature rather than one per backing.
//
// # The scope is an argument, on every method
//
// Every method takes a tenancy.Scope, and none of them offers a variant that
// omits it. A deployment with one directory passes tenancy.Global() everywhere
// and behaves exactly as it would have without the column.
//
// That includes the six writes that take a whole entity. They read the scope
// off the argument rather than off User.Scope and its siblings, for the reason
// comments.Store gives about Comment.Scope: an entity field is exactly the
// derivation the column rule exists to rule out, since it makes "which directory
// is this write for" answerable only by reading a struct the caller assembled
// somewhere else. An entity naming no scope adopts the argument, and one naming
// a different scope is ErrScopeMismatch rather than being corrected.
//
// There is no ListAllUsers, and the absence is deliberate: an operator console
// listing every user in a deployment is listing one directory at a time, and the
// method that spans directories is the one whose missing predicate serves one
// customer's user list to another.
type Store interface {
	Registrar
	CredentialStore
	SignInReader
	DirectoryReader
	ProfileWriter
	MembershipWriter
	AdminWriter
	BillingWriter
	InvitationStore
}
