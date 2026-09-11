package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultTablePrefix is the namespace the identity tables carry when none is
// configured, which is none — rendering identity_users and its six siblings.
//
// The identity_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_identity_users, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema identity/migrations
// renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	q     identitydb.Querier
	o11y  observability.Observer
	clock clock.Clock

	unmatchedWritesCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger         logging.Logger
	tracerProvider tracing.Provider

	metricsProvider metrics.Provider

	// tablePrefix is what the generated statements' markers are substituted
	// with, and the only thing this store knows about the shape of its own SQL.
	// Nothing here renders a statement any more — see identity/internal/queries
	// — so the dialect is not kept either: it decides which generated querier is
	// built and is not needed again.
	tablePrefix string
}

// NewSQLStore builds a Store over the given database.
//
// The client is read at construction and not kept. It supplies the dialect,
// which decides which generated querier is built, and nothing else: every write
// runs on the database.Tx its caller hands over and every read on the
// database.SQLQueryExecutor its caller hands over, so there is no Writer() or
// Reader() of the store's own for either to fall back to. The one handle this
// store does keep is its clock, which stamps the columns the schema does not
// default — see WithClock.
//
// The dialect coming from the client means the two cannot disagree. The prefix
// must still match the one the migrations were rendered with — nothing here can
// check that, and a mismatch surfaces as a missing table on the first query
// rather than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and records to a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "identity dialect %q", d)
	}

	s := &SQLStore{
		clock:       clock.NewClock(),
		tablePrefix: DefaultTablePrefix,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.tablePrefix); err != nil {
		return nil, err
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see identity/internal/identitydb.
	qd, err := identitydbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := identitydb.New(qd, ddl.Qualify(s.tablePrefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the identity querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and it is the one nothing above this layer can see. Every
	// write here is guarded by "did this touch a row" — that is what turns a
	// write aimed at another directory into ErrUserNotFound instead of a
	// success — and the three reasons a write matches nothing arrive as one
	// answer. See SQLStore.guardCount.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.unmatchedWritesCounter, err = mp.NewInt64Counter(storeName + "_unmatched_writes"); err != nil {
		return nil, platformerrors.Wrap(err, "creating identity store unmatched write counter")
	}

	return s, nil
}

// operationAttr labels an instrument with the operation it was recorded in,
// since the places a measurement can be taken mean different things. Both
// layers of this package record through it — see operationKey.
func operationAttr(operation string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(operationKey, operation))
}

// now is the one clock read every write in this package goes through, in UTC —
// see the timestamp note in identity/doc.go on why the location matters to
// SQLite.
func (s *SQLStore) now() time.Time { return s.clock.Now().UTC() }

// notFound maps a driver's empty-result error onto this package's sentinel for
// the entity that was missing, leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a user
// as "no such account". The sentinel is per-entity because the caller's next
// move differs: a missing user during sign-in is a rejection, a missing
// membership is an authorization failure.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// requireExecutor is the guard on every method that runs inside the caller's
// transaction. A nil executor there is a wiring mistake, and letting it reach a
// method call means a nil dereference three frames down rather than the name of
// the parameter that was not supplied.
func requireExecutor(q database.SQLQueryExecutor) error {
	if q == nil {
		return ErrNilExecutor
	}

	return nil
}

// rolesFor groups a batched role read by owner, and answers an empty batch
// without running it.
//
// The read itself is the caller's closure rather than a table this takes, for
// the reason the keyed user reads enumerate: a query name is a Go method name,
// so the three role tables are three generated statements rather than one
// parameterized by a table. What is here is what the three share — the empty
// batch, and the grouping every caller would otherwise write.
//
// Batched deliberately: a roster page of thirty members read one query at a
// time is thirty round trips returning two rows each, and that is the shape a
// page read arrives at when roles are fetched inside the loop that converts
// rows. The generated statement is querygen's batched read, ordered by the
// owner and then by the role — see querygen.Generator.SetReadQuery.
//
// The empty batch is answered here because the statement cannot express one —
// the SQL the corpus carries has no rendering of an empty set, and what the
// generated code substitutes for one is a predicate matching no row. Sending it
// is a round trip whose answer was known before it left, on the read that
// exists to save round trips.
func rolesFor(ownerIDs []string, read func([]string) ([]ownedRole, error)) (map[string][]string, error) {
	byOwner := map[string][]string{}
	if len(ownerIDs) == 0 {
		return byOwner, nil
	}

	rows, err := read(ownerIDs)
	if err != nil {
		return nil, err
	}

	for i := range rows {
		byOwner[rows[i].ownerID] = append(byOwner[rows[i].ownerID], rows[i].role)
	}

	return byOwner, nil
}

// roleStatements is one role table's pair of generated writes, with the owner
// column each binds already chosen: the clear, and the insert of one grant.
//
// A query name is a Go method name, so the three role tables cannot share a
// statement the way they shared a builder that took the table as an argument —
// there are six methods where there were two. What they can share is the
// operation, which is the part that can be got wrong: clear first, then write
// the set back, because an insert running first collides with the grants it is
// replacing. That lives once, in replaceRoles, and this is what differs per
// table.
type roleStatements struct {
	clear  func(ctx context.Context, db identitydb.DBTX, ownerID string) (int64, error)
	insert func(ctx context.Context, db identitydb.DBTX, ownerID, role string) error
}

// userRoleWrites is the service-role table's pair: the roles a user holds outside
// any account.
func (s *SQLStore) userRoleWrites() roleStatements {
	return roleStatements{
		clear: func(ctx context.Context, db identitydb.DBTX, ownerID string) (int64, error) {
			return s.q.DeleteUserRoles(ctx, db, identitydb.DeleteUserRolesParams{UserID: ownerID})
		},
		insert: func(ctx context.Context, db identitydb.DBTX, ownerID, role string) error {
			return s.q.InsertUserRole(ctx, db, identitydb.InsertUserRoleParams{UserID: ownerID, Role: role})
		},
	}
}

// membershipRoleWrites is the pair for what a member may do inside one account.
func (s *SQLStore) membershipRoleWrites() roleStatements {
	return roleStatements{
		clear: func(ctx context.Context, db identitydb.DBTX, ownerID string) (int64, error) {
			return s.q.DeleteMembershipRoles(ctx, db, identitydb.DeleteMembershipRolesParams{MembershipID: ownerID})
		},
		insert: func(ctx context.Context, db identitydb.DBTX, ownerID, role string) error {
			return s.q.InsertMembershipRole(ctx, db,
				identitydb.InsertMembershipRoleParams{MembershipID: ownerID, Role: role})
		},
	}
}

// invitationRoleWrites is the pair for what an invitation promises.
func (s *SQLStore) invitationRoleWrites() roleStatements {
	return roleStatements{
		clear: func(ctx context.Context, db identitydb.DBTX, ownerID string) (int64, error) {
			return s.q.DeleteInvitationRoles(ctx, db, identitydb.DeleteInvitationRolesParams{InvitationID: ownerID})
		},
		insert: func(ctx context.Context, db identitydb.DBTX, ownerID, role string) error {
			return s.q.InsertInvitationRole(ctx, db,
				identitydb.InsertInvitationRoleParams{InvitationID: ownerID, Role: role})
		},
	}
}

// replaceRoles clears an owner's roles and writes the new set, which is how all
// three role tables are written — SetMembershipRoles replaces rather than
// merges, and so do the writes behind a registration and an accepted invitation.
//
// The clear's row count is discarded on purpose. An owner with no grants yet is
// the ordinary case — a registration, a first invitation — so zero means "there
// was nothing to clear" rather than "the row was not found", and there is no
// caller for whom the two differ.
//
// The insert runs once per role rather than once per call. The multi-row VALUES
// list it replaced was assembled from the caller's cardinality, which is dynamic
// SQL by construction: no static text for sqlc to check and none for querygen to
// emit. What it costs is a round trip per role, inside the transaction the
// parent's write already opened, at cardinalities that are single-digit.
func (s *SQLStore) replaceRoles(
	ctx context.Context,
	q database.SQLQueryExecutor,
	statements roleStatements,
	ownerID string,
	roles []string,
) error {
	if _, err := statements.clear(ctx, q, ownerID); err != nil {
		return platformerrors.Wrap(err, "clearing identity roles")
	}

	for _, role := range roles {
		if err := statements.insert(ctx, q, ownerID, role); err != nil {
			return platformerrors.Wrap(err, "writing identity roles")
		}
	}

	return nil
}

// ensureUsernameFree and ensureEmailAddressFree are the two collision checks a
// write runs before it writes, so a taken handle reports ErrUsernameTaken or
// ErrEmailAddressTaken rather than a driver-specific constraint violation the
// caller would have to parse a SQLSTATE out of.
//
// exceptID is the row being updated, which is what lets a user save their own
// profile without colliding with themselves, and is empty at creation because
// there is no row yet. It reaches the statement as an argument that may be
// absent rather than as a predicate that may be missing, so both callers run
// the same checked statement — see the uniqueness checks in
// identity/internal/queries.
//
// They enumerate rather than take a column, for the reason every other read
// here does: a query name is a Go method name, so a check parameterized on the
// column cannot be one generated statement.
func (s *SQLStore) ensureUsernameFree(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	username, exceptID string,
) error {
	_, err := s.q.GetUserIDByUsername(ctx, q, identitydb.GetUserIDByUsernameParams{
		Username:     username,
		Scope:        scope,
		ExceptUserID: exceptUserID(exceptID),
	})

	return uniquenessResult(err, ErrUsernameTaken)
}

func (s *SQLStore) ensureEmailAddressFree(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	emailAddress, exceptID string,
) error {
	_, err := s.q.GetUserIDByEmailAddress(ctx, q, identitydb.GetUserIDByEmailAddressParams{
		EmailAddress: emailAddress,
		Scope:        scope,
		ExceptUserID: exceptUserID(exceptID),
	})

	return uniquenessResult(err, ErrEmailAddressTaken)
}

// exceptUserID renders the id a collision check excludes as the argument the
// statement takes: absent where there is no row to exclude.
//
// The empty string would do the same thing — the statement coalesces an absent
// argument to it — but sending it would say the caller is excluding a row whose
// id is empty, which is a row no write in this schema can produce. Absent says
// what is true.
func exceptUserID(exceptID string) *string {
	if exceptID == "" {
		return nil
	}

	return &exceptID
}

// uniquenessResult reads a collision check's outcome: no row is a free handle,
// a row is taken, and anything else is the read failing.
//
// The index is what actually guarantees uniqueness; this read is what turns the
// ordinary case into a sentinel. Two registrations racing for the same handle
// still reach the index, and the loser gets the driver's error — which is
// correct, rare, and the reason this check is not presented as the guarantee.
func uniquenessResult(err, taken error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return platformerrors.Wrap(err, "checking identity uniqueness")
	default:
		return taken
	}
}

// adoptScope settles which tenant a write is for, and writes the answer back
// onto the entity.
//
// The scope the call named is the one the statement binds, so an entity that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one directory's user and writing it into another, which is a
// stale value or a mix-up and is not a thing to guess at. An entity that names
// none adopts the argument. tenancy.Scope tells its zero value apart from
// Global(), so "unset" here is genuinely unset rather than the global scope
// spelled shortly.
//
// It takes the entity's field rather than the entity because the six writes that
// carry one carry four different types between them, and the rule is the row's
// rather than any one of theirs — see comments.Store.CreateComment, which
// settled it for every store in this module.
func adoptScope(scope tenancy.Scope, carried *tenancy.Scope, entity string) error {
	if *carried != (tenancy.Scope{}) && *carried != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"%s names %q, the write names %q", entity, *carried, scope)
	}

	*carried = scope

	return nil
}

// newID returns the identifier a write should carry: the one the caller set, or
// a fresh one.
//
// Accepting a caller-supplied ID matters for the registration flow, where the
// user ID is often minted before the transaction so that an outbox message or a
// search document can reference it.
func newID(existing string) string {
	if existing != "" {
		return existing
	}

	return identifiers.New()
}

// What follows is the machinery the nine implementation files share — the
// column names spelled in more than one of them, the reads two seams both need,
// and the two guards every paged read and every guarded write goes through.
// Anything reached from one file only lives in that file.

// The username and the email address used to be here too, aliased for the
// collision check's hand-built predicate. Both checks are generated statements
// now, so no column a user's uniqueness turns on is spelled above the SQL any
// more — nor is the verification token, which went the same way.

// liveUser is what the three single-user reads keyed on something other than the
// id share: the executor and scope checks, the not-found mapping, the service
// roles, and the span.
//
// The roles are read on the caller's executor, which is the same one the user
// was read on. Reading them from a handle of the store's own would answer half
// of one call from the caller's transaction and half from outside it, so a
// registration that granted a service role and then read the user back would
// see the user and not the grant.
//
// The read itself is the caller's closure rather than a column this takes, and
// that is the shape the canonical corpus asks for. A query name is a Go method
// name, so a builder parameterized on a column cannot be one generated
// statement — it is three, and the store calls the one it means by name. What
// the parameterized builder was protecting is not lost by enumerating: the
// scope predicate and the archived clause come from querygen, rendered from one
// column list, so the sign-in read cannot be the one that forgot either.
func (s *SQLStore) liveUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	description string,
	read func(context.Context) (*User, error),
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "%s", description)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "%s", description)
	}

	user, err := read(ctx)
	if err != nil {
		return nil, op.Error(notFound(err, ErrUserNotFound), "%s", description)
	}

	if err = s.attachServiceRoles(ctx, q, []*User{user}); err != nil {
		return nil, op.Error(err, "%s", description)
	}

	op.SpanOnly(userIDKey, user.ID)

	return user, nil
}

// attachServiceRoles fills in the ServiceRoles of a batch of users with one
// query, rather than one per user — which is what a directory page would
// otherwise cost.
func (s *SQLStore) attachServiceRoles(ctx context.Context, q database.SQLQueryExecutor, users []*User) error {
	ids := make([]string, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.ID)
	}

	byUser, err := rolesFor(ids, func(ids []string) ([]ownedRole, error) {
		rows, err := s.q.ListUserRolesByUserIDs(ctx, q, identitydb.ListUserRolesByUserIDsParams{IDs: ids})
		if err != nil {
			return nil, err
		}

		return userRolesFromRows(rows), nil
	})
	if err != nil {
		return err
	}

	for _, user := range users {
		user.ServiceRoles = byUser[user.ID]
	}

	return nil
}

// readMembership is the read of one live membership, keyed on the (user,
// account) pair rather than on the id the table also carries.
//
// It maps a missing row onto ErrMembershipNotFound here rather than at each
// caller, because two of the three read it as an error and the third reads it
// as a question — TransferAccountOwnership asks whether the new owner is
// already a member — and a sentinel is what lets the third one branch on the
// answer without knowing which driver produced it.
//
// Roles are not attached: they live in their own table and are read for a whole
// page at once, so the caller that needs them says so.
func (s *SQLStore) readMembership(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, accountID string,
) (*Membership, error) {
	row, err := s.q.GetMembershipByUserAndAccount(ctx, q, identitydb.GetMembershipByUserAndAccountParams{
		Scope:            scope,
		BelongsToUser:    userID,
		BelongsToAccount: accountID,
	})
	if err != nil {
		return nil, notFound(err, ErrMembershipNotFound)
	}

	return membershipFromRow(&row), nil
}

// readMembershipsForUser is the read behind both ListMembershipsForUser and
// GetPrincipal, roles attached.
func (s *SQLStore) readMembershipsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*Membership, error) {
	rows, err := s.q.ListMembershipsForUser(ctx, q, listMembershipsForUserParams(scope, userID))
	if err != nil {
		return nil, err
	}

	memberships := make([]*Membership, 0, len(rows))
	for i := range rows {
		memberships = append(memberships, membershipFromListRow(&rows[i]))
	}

	if err = s.attachMembershipRoles(ctx, q, memberships); err != nil {
		return nil, err
	}

	return memberships, nil
}

// attachMembershipRoles fills in the Roles of a batch of memberships with one
// query, rather than one per membership.
func (s *SQLStore) attachMembershipRoles(ctx context.Context, q database.SQLQueryExecutor, memberships []*Membership) error {
	ids := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		ids = append(ids, membership.ID)
	}

	byMembership, err := rolesFor(ids, func(ids []string) ([]ownedRole, error) {
		rows, err := s.q.ListMembershipRolesByMembershipIDs(ctx, q, identitydb.ListMembershipRolesByMembershipIDsParams{IDs: ids})
		if err != nil {
			return nil, err
		}

		return membershipRolesFromRows(rows), nil
	})
	if err != nil {
		return err
	}

	for _, membership := range memberships {
		membership.Roles = byMembership[membership.ID]
	}

	return nil
}

// requireMembershipEndpoints reads the user and the account a membership links,
// in the membership's own scope.
//
// The foreign keys prove existence and not congruence: belongs_to_user
// references identity_users (id) and nothing else, so a membership naming a
// user who lives in another directory is a row the database accepts. What it
// produces is a roster displaying a stranger — the junction join matches on the
// user id alone, so the projected user_scope disagrees with every other column
// around it — and a Principal assembled from memberships their own directory
// cannot see.
//
// Everywhere else this store answers "exists, but in another directory" with
// not-found, and this is the write path agreeing with the reads. It maps a miss
// onto the entity-shaped sentinel, so a caller learns which endpoint was the
// stranger without learning that it exists somewhere else.
//
// It lives at the write rather than at each of its callers because it is the
// invariant of the row rather than of any one caller: the next thing to reach
// writeMembership inherits it instead of having to remember it.
func (s *SQLStore) requireMembershipEndpoints(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, accountID string,
) error {
	if _, err := s.readUser(ctx, q, scope, userID); err != nil {
		return err
	}

	if _, err := s.readAccount(ctx, q, scope, accountID); err != nil {
		return err
	}

	return nil
}

// writeMembership upserts the membership row, replaces its roles, and answers
// with the row the database now holds.
//
// Both endpoints are read in the membership's scope before anything is written;
// see requireMembershipEndpoints for why the foreign keys are not enough.
//
// The row is read back rather than assumed, because what the upsert settled is
// not what this process sent. It converges on the (user, account) pair, so a
// user rejoining an account revives the archived membership and it keeps the ID
// it was created with — writing the roles against the ID the caller generated
// would attach them to a membership that does not exist — and created_at is
// database-owned here as everywhere else in this schema, so even the inserting
// branch stores the server's clock rather than the one the caller was handed.
//
// The roles are the set this call just wrote rather than a second read of the
// table it wrote them to. readMembership does not attach roles either: a grant
// lives in its own table and is read for a whole page at once, so the read-back
// that carried them would be one round trip to be told what the statement
// before it had just said.
//
// The Membership it is handed is read and not written to, which is what lets
// every caller of it answer with a row rather than with their own argument.
func (s *SQLStore) writeMembership(
	ctx context.Context,
	q database.SQLQueryExecutor,
	membership *Membership,
) (*Membership, error) {
	if err := s.requireMembershipEndpoints(
		ctx, q, membership.Scope, membership.BelongsToUser, membership.BelongsToAccount,
	); err != nil {
		return nil, err
	}

	if err := s.q.UpsertMembership(ctx, q, upsertMembershipParams(membership)); err != nil {
		return nil, platformerrors.Wrap(err, "writing identity membership")
	}

	row, readErr := s.q.GetMembershipByUserAndAccount(ctx, q, identitydb.GetMembershipByUserAndAccountParams{
		Scope:            membership.Scope,
		BelongsToUser:    membership.BelongsToUser,
		BelongsToAccount: membership.BelongsToAccount,
	})
	if readErr != nil {
		return nil, platformerrors.Wrap(readErr, "reading back identity membership")
	}

	written := membershipFromRow(&row)
	written.Roles = membership.Roles

	if err := s.replaceRoles(ctx, q, s.membershipRoleWrites(), written.ID, written.Roles); err != nil {
		return nil, err
	}

	if !written.DefaultAccount {
		return written, nil
	}

	if err := s.clearDefaultAccountsForUser(
		ctx, q, written.Scope, written.BelongsToUser, written.BelongsToAccount,
	); err != nil {
		return nil, err
	}

	return written, nil
}

// clearDefaultAccountsForUser takes the default flag off every live membership
// a user holds, sparing the one in exceptAccountID. That is the other half of
// "one default per user": a caller marking one membership as the default does
// not have to remember to unmark the rest.
//
// An empty exception spares nothing, which is what the archival wants — every
// one of the user's memberships is about to stop being live, so there is no
// membership for the flag to stay on.
//
// The row count is discarded. The statement only reaches memberships whose flag
// differs from the one being assigned, so zero means the user had no other
// default rather than that anything is missing — and every caller has already
// established that the user exists.
func (s *SQLStore) clearDefaultAccountsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, exceptAccountID string,
) error {
	_, err := s.q.ClearMembershipDefaultAccountsForUser(ctx, q, identitydb.ClearMembershipDefaultAccountsForUserParams{
		Scope:           scope,
		BelongsToUser:   userID,
		DefaultAccount:  false,
		ExceptAccountID: sparedAccount(exceptAccountID),
	})
	if err != nil {
		return platformerrors.Wrap(err, "clearing other identity default accounts")
	}

	return nil
}

// sparedAccount renders the account a default-flag clear leaves alone as the
// argument the statement takes: absent when there is none.
//
// Absence is meaningful here rather than a caller forgetting to bind — an
// archival is clearing every one of the user's memberships and has none to
// spare — and the statement coalesces an absent argument to the empty string,
// which is an account id no row has. See identity/internal/queries.
func sparedAccount(accountID string) *string {
	if accountID == "" {
		return nil
	}

	return &accountID
}

// hasLiveMembership reports whether the user belongs to any account yet, which
// is how a membership write finds out that the one it is making is their first
// and therefore their default.
//
// It reads an id rather than a count, because the caller's question is "any"
// and a count that answers seven has spent the server's time distinguishing
// seven from one.
func (s *SQLStore) hasLiveMembership(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (bool, error) {
	_, err := s.q.GetMembershipIDForUser(ctx, q, identitydb.GetMembershipIDForUserParams{
		Scope:         scope,
		BelongsToUser: userID,
	})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, platformerrors.Wrap(err, "reading identity memberships")
	}

	return true, nil
}

// pageFilter is the filter a paged read is answered under: the caller's, with
// the page-size ceiling every other paged read in this module applies.
//
// It works on a copy. The clamp has to be applied to what the query binds and
// to what the result reports, and doing that by writing through the caller's
// pointer would hand them back a filter they did not pass — a store that
// rewrites its argument is a store whose caller cannot reuse one.
//
// A page size that is present and zero is left alone and returns no rows, which
// is the loud reading of an explicit zero and the same distinction
// filtering.ClampResponseSize draws. Only absence is defaulted.
//
// The sort direction passes through untouched, and is read where it is used:
// filtering.QueryFilter.SortsDescending answers an absent or unrecognized one
// ascending, which is the reading filtering.QueryFilter.Normalize applies, so
// there is nothing for this to correct on the way past.
func pageFilter(filter *filtering.QueryFilter) *filtering.QueryFilter {
	if filter == nil {
		return filtering.DefaultQueryFilter()
	}

	bounded := *filter

	size := uint16(filtering.DefaultQueryFilterLimit)
	if bounded.MaxResponseSize != nil {
		size = filtering.ClampResponseSize(uint64(*bounded.MaxResponseSize))
	}

	bounded.MaxResponseSize = &size

	return &bounded
}

// identitydbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already
// rejected anything d.Valid() declines — so the default arm is reachable only
// when this module learns a dialect the generated package was not generated
// for. That is a construction failure like any other, and it names the
// dialect, rather than panicking or leaning on identitydb.New refusing the
// empty string.
func identitydbDialect(d dialect.Dialect) (identitydb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return identitydb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return identitydb.DialectMySQL, nil
	case dialect.SQLite:
		return identitydb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated identity queries for dialect %q", d)
	}
}

// guardCount is how every write in this store learns whether it touched a row.
// The statement ran through identitydb, whose :execrows methods already asked
// the driver for the affected count, so what is left is mapping "touched
// nothing" onto the sentinel for the entity that was not there — every
// predicate here includes the scope, and without this a write aimed at another
// directory's row reports success.
//
// A driver that declines to report the count reaches this as an error rather
// than as an acknowledged unknown, because the generated method has no seam
// between running the statement and reading the count. None of the three
// supported drivers declines; the tolerance the hand-built path carried guarded
// a hypothetical, and it went with the last statement this store rendered
// itself.
//
// It counts the writes that matched nothing, which is the one place this store
// collapses answers a caller might want apart. A write reaches zero rows
// because the row is not there, because it belongs to another directory, or —
// for the guarded ones — because it lost a race to a write that got there
// first, and all three become the same sentinel. The counter is labeled with
// the operation, so an application that starts serving not-founds on a write
// path can see which one without the store having to invent an error for each
// cause.
func (s *SQLStore) guardCount(ctx context.Context, count int64, err, missing error, operation string) error {
	if err != nil {
		return platformerrors.Wrap(err, operation)
	}

	if count == 0 {
		s.unmatchedWritesCounter.Add(ctx, 1, operationAttr(operation))

		return missing
	}

	return nil
}

// stampCreatedAt writes the creation time the database assigned onto the value
// the caller handed over.
//
// The column is the database's — see identity/internal/queries — so the create
// does not carry it, and the alternative to this read is a caller whose struct
// says 0001-01-01 for a row that was written a moment ago. That is the worse
// answer by some distance: CreatedAt is exported and a service serializes the
// value it just created straight into a response, where a zero time renders as
// a date rather than reading as an absence. So the create reads it back, and
// the field means what it says on both sides of the call.
//
// It costs one round trip inside a transaction the write already needed, and it
// reads its own uncommitted row on all three servers.
//
// It takes the read's result rather than performing it, because the statement is
// one per emitted table — a query name is a Go method name, so the table cannot
// be a parameter — and each create calls the one for its own.
func stampCreatedAt(at *time.Time, created time.Time, err error) error {
	if err != nil {
		return platformerrors.Wrap(err, "reading back the assigned creation time")
	}

	*at = created.UTC()

	return nil
}
