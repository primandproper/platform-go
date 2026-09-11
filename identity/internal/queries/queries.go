package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what identity's own prefix rendering starts from.
const (
	UsersTable           = "identity_users"
	UserRolesTable       = "identity_user_roles"
	AccountsTable        = "identity_accounts"
	MembershipsTable     = "identity_memberships"
	MembershipRolesTable = "identity_membership_roles"
	InvitationsTable     = "identity_invitations"
	InvitationRolesTable = "identity_invitation_roles"
)

// TableNames is every table identity owns, in the order the DDL creates them.
//
// Seven rather than the four declared below as [Table] values: the three role
// tables carry no columns worth describing here and are written by hand-rendered
// DELETE/INSERT pairs, but a table nothing generates queries for is still a
// table with rows in it. That is the distinction the querygen registry is built
// around, and this is the list [Render] feeds it — see the comment there.
//
// identity/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{
	UsersTable,
	UserRolesTable,
	AccountsTable,
	MembershipsTable,
	MembershipRolesTable,
	InvitationsTable,
	InvitationRolesTable,
}

// ScopeColumn is the tenancy dimension every table here carries and every
// statement is keyed on. It is a column, not a convention: an unscoped read of
// this schema is not expressible, because there is no statement that omits it.
const ScopeColumn = "scope"

// The invitation columns the keyed list variants below name. Exported because
// the store spells them too — its argument maps key on them — and two spellings
// of one column is the drift the rest of this package exists to prevent.
const (
	InvitationFromUserColumn = "from_user"
	InvitationToEmailColumn  = "to_email"
	InvitationStatusColumn   = "status"
)

// The user columns the keyed sign-in reads name. Exported for the same reason
// the invitation columns above are: the store spells them too, and two
// spellings of one column is the drift this package exists to prevent.
const (
	UserUsernameColumn               = "username"
	UserEmailAddressColumn           = "email_address"
	UserEmailVerificationTokenColumn = "email_address_verification_token"
)

// The role tables' columns. Each of the three is a child set of one parent row
// — a user, a membership, an invitation — keyed on that parent and carrying one
// role per row, with no id, no scope and no timestamps of its own — see
// [RoleTable]. RoleColumn is the grant itself, and the second half of every
// role table's primary key; the owner column is the first.
//
// They carry no scope because they are not read on their own: a role row is
// reached through the parent whose id it names, and that parent is scoped. What
// keys the batched reads below is the parent column, which is why each is
// spelled here rather than at the store — the read that binds it and the write
// that assigns it are two files.
const (
	UserRoleOwnerColumn       = "user_id"
	MembershipRoleOwnerColumn = "membership_id"
	InvitationRoleOwnerColumn = "invitation_id"
	RoleColumn                = "role"
)

// The two columns a membership is keyed on, unique on the pair live and
// archived alike. Together they are the row's natural key — one live membership
// per (user, account) pair — which is why every membership statement addresses
// a row by both rather than by the id the table also carries, and why the
// upsert converges on them: the schema's UNIQUE, the upsert's ON CONFLICT and
// the store's own reads all name these two columns, and a conflict target that
// disagreed with the key would insert a second row where it meant to revive the
// first. The junction lists name the pair too — in the join, in the key a list
// is read by, and in the order it comes back in — which is why they are here
// rather than at each call site.
//
// MembershipDefaultColumn is the flag that decides which account a user lands
// in: the one column the standard update assigns and a rejoin may carry over.
const (
	MembershipUserColumn    = "belongs_to_user"
	MembershipAccountColumn = querygen.BelongsToAccountColumn
	MembershipDefaultColumn = "default_account"
)

// RoleTable is one of the three tables a role grant lands in, and it is not a
// [Table]: it has no id, no scope, no convention triple, and no standard query
// of any kind. What it has is a parent, a grant, and the two writes that
// rewrite a role set wholesale.
//
// It is a type of its own rather than a Table with most of its fields empty,
// because every one of those fields decides something for a table that has a
// caller reading it on its own — and none of these does. A grant is read
// through its parent, filtered by nothing, and archived by the parent being
// archived; see identity/migrations on why the triple is absent from the
// schema too.
type RoleTable struct {
	// Name is the canonical, unprefixed table name.
	Name string
	// OwnerColumn is the parent the grant hangs off.
	OwnerColumn string
	// Singular names the entity the two statement names are built from:
	// InsertUserRole and DeleteUserRoles.
	Singular string
}

// Columns is the whole of the row, which is also its primary key.
func (t *RoleTable) Columns() []string {
	return []string{t.OwnerColumn, RoleColumn}
}

// The three of them, in the order the emitted .sql lists their statements.
var (
	// UserRoles is the roles a user holds outside any account — operator,
	// support, service administrator.
	UserRoles = RoleTable{Name: UserRolesTable, OwnerColumn: UserRoleOwnerColumn, Singular: "User"}
	// MembershipRoles is what a member may do inside one account.
	MembershipRoles = RoleTable{
		Name:        MembershipRolesTable,
		OwnerColumn: MembershipRoleOwnerColumn,
		Singular:    "Membership",
	}
	// InvitationRoles is what an invitation promises, fixed at invitation time
	// so that what somebody was invited to is what they get.
	InvitationRoles = RoleTable{
		Name:        InvitationRolesTable,
		OwnerColumn: InvitationRoleOwnerColumn,
		Singular:    "Invitation",
	}
)

// RoleTables is the three of them, in the order Render emits their statements.
var RoleTables = []*RoleTable{&UserRoles, &MembershipRoles, &InvitationRoles}

// The columns the field-specific writes assign, and the arguments the guarded
// ones compare against.
//
// A guard's argument cannot be spelled with the column it names. Requiring the
// invitation to still be pending while setting its status is one comparison
// against the stored value and one assignment of the new one, and under a single
// argument name the statement would set the column to the value it had just
// required it to already hold — legal SQL that guards nothing. So the guards are
// named apart, and the "current_" prefix says which end of the comparison each
// one is.
const (
	hashedPasswordColumn           = "hashed_password"
	requiresPasswordChangeColumn   = "requires_password_change"
	passwordLastChangedAtColumn    = "password_last_changed_at"
	twoFactorColumn                = "two_factor_secret"
	twoFactorVerifiedAtColumn      = "two_factor_secret_verified_at"
	accountStatusColumn            = "account_status"
	accountStatusExplanationColumn = "account_status_explanation"
	ownerUserIDColumn              = "owner_user_id"
	invitationStatusNoteColumn     = "status_note"
	invitationToUserColumn         = "to_user"

	// The two agreement stamps, one per statement. A document is accepted on
	// its own — a registration that accepts both runs both statements — so the
	// column a caller's Agreement resolves to is the statement it names rather
	// than a SET list assembled from however many documents were named.
	termsOfServiceColumn = "last_accepted_terms_of_service"
	privacyPolicyColumn  = "last_accepted_privacy_policy"

	// The four billing columns, one per statement. They are named here rather
	// than at the call site for the reason every other column in this block is:
	// the statement that assigns one and the store that binds it are two files,
	// and a column spelled twice is a column that can be spelled differently.
	billingStatusColumn              = "billing_status"
	subscriptionPlanIDColumn         = "subscription_plan_id"
	paymentProcessorCustomerIDColumn = "payment_processor_customer_id"
	billingSyncedAtColumn            = "last_payment_provider_synced_at"

	currentEmailVerificationTokenArg = "current_" + UserEmailVerificationTokenColumn
	currentOwnerUserIDArg            = "current_" + ownerUserIDColumn
	currentInvitationStatusArg       = "current_" + InvitationStatusColumn
)

// exceptUserIDArg is the argument the collision checks exclude a row through:
// the id of the user being updated, absent when there is not one yet.
//
// It is the one argument in this schema whose absence is meaningful rather than
// a caller forgetting to bind it — see uniquenessChecks.
const exceptUserIDArg = "except_user_id"

// exceptAccountIDArg is the argument the default-flag clear spares a membership
// through: the account whose membership keeps the flag, absent when none does.
//
// The second argument in this schema whose absence is meaningful, and for the
// same reason the first one's is — see membershipWrites, where one statement
// serves both the write that has an account to spare and the archival that has
// none.
const exceptAccountIDArg = "except_account_id"

// EmailAddressVerifiedAtColumn is the proof a user's address is reachable.
//
// Spelled once because three declarations below name it — it is nullable, it is
// updatable, and it is in the projection — and because the store reasons about
// its value rather than only assigning it: moving an address clears it.
const EmailAddressVerifiedAtColumn = "email_address_verified_at"

// Users is the directory's people.
//
// Everything after last_name is either a credential, a proof, or a status, and
// each is written by the method that owns it — which is why the standard update
// assigns four profile columns and two derived ones. email_address_verified_at
// and email_address_verification_token are updatable on purpose, and as a pair:
// moving an address has to clear the proof that went with it, or a user could
// take an address they have never proven and stay verified — and it has to
// clear the outstanding token in the same statement, or the link minted for the
// address being left behind proves the one being moved to.
var Users = Table{
	Name:     UsersTable,
	Singular: "User",
	Plural:   "Users",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		UserUsernameColumn,
		UserEmailAddressColumn,
		"first_name",
		"last_name",
		"hashed_password",
		"requires_password_change",
		"password_last_changed_at",
		"two_factor_secret",
		"two_factor_secret_verified_at",
		EmailAddressVerifiedAtColumn,
		UserEmailVerificationTokenColumn,
		accountStatusColumn,
		accountStatusExplanationColumn,
		termsOfServiceColumn,
		privacyPolicyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{
		passwordLastChangedAtColumn,
		twoFactorVerifiedAtColumn,
		EmailAddressVerifiedAtColumn,
		termsOfServiceColumn,
		privacyPolicyColumn,
	},
	Updatable: []string{
		UserUsernameColumn,
		UserEmailAddressColumn,
		"first_name",
		"last_name",
		EmailAddressVerifiedAtColumn,
		UserEmailVerificationTokenColumn,
	},
	Omitted: []querygen.StandardQuery{querygen.ExistsQuery},
}

// Accounts is what users belong to and what invoices are addressed to.
//
// The owner and every billing column are immutable to the standard update:
// transferring ownership names the current owner in its predicate so two
// concurrent transfers cannot both win, and a processor webhook carrying one
// field must not read-modify-write the rest. Each of the four billing columns
// is assigned by a statement of its own in fieldWrites, one per event a
// processor actually reports.
var Accounts = Table{
	Name:     AccountsTable,
	Singular: "Account",
	Plural:   "Accounts",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		"name",
		"owner_user_id",
		"billing_status",
		"subscription_plan_id",
		"payment_processor_customer_id",
		"last_payment_provider_synced_at",
		"address_line1",
		"address_line2",
		"address_city",
		"address_state",
		"address_postal_code",
		"address_country",
		"address_phone",
		"time_zone",
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{
		"subscription_plan_id",
		"last_payment_provider_synced_at",
	},
	Updatable: []string{
		"name",
		"address_line1",
		"address_line2",
		"address_city",
		"address_state",
		"address_postal_code",
		"address_country",
		"address_phone",
		"time_zone",
	},
	Omitted: []querygen.StandardQuery{querygen.ExistsQuery},
}

// Invitations is an offer of membership addressed to an email address.
//
// It gets no standard update and no archive. An invitation is answered rather
// than edited, and the answer is a status-guarded write — still pending in the
// predicate, so two clicks on one link produce one membership — which is
// AnswerInvitation in fieldWrites rather than the whole-row assignment the
// standard set would emit.
var Invitations = Table{
	Name:     InvitationsTable,
	Singular: "Invitation",
	Plural:   "Invitations",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		"belongs_to_account",
		"from_user",
		"to_email",
		"to_name",
		"to_user",
		"token",
		"status",
		"note",
		invitationStatusNoteColumn,
		"expires_at",
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{"to_user"},
	Omitted: []querygen.StandardQuery{
		querygen.ExistsQuery,
		querygen.UpdateQuery,
		querygen.ArchiveQuery,
	},
}

// Memberships is the many-to-many between the two, with facts of its own.
//
// It gets no standard queries — not one of its statements keys on the id the
// table carries — but everything else it runs is emitted from it: the keyed
// reads, the upsert that revives an archived membership when a user rejoins,
// the three junction lists that cross it, and the default-flag and archival
// writes in membershipWrites. Its columns are the canonical corpus's as well as
// the store's.
//
// Updatable is the one column a rejoin may carry over, and stating it is what
// keeps the upsert's conflict branch off the rest: the id is what the
// membership's roles hang from and must survive a revival, and the scope is
// immutable here as it is everywhere else in this schema.
var Memberships = Table{
	Name:     MembershipsTable,
	Singular: "Membership",
	Plural:   "Memberships",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		MembershipUserColumn,
		MembershipAccountColumn,
		MembershipDefaultColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Updatable: []string{MembershipDefaultColumn},
}

// Emitted is the tables the canonical .sql covers with the standard set, in the
// order they appear in it.
//
// Memberships is deliberately absent and still contributes one statement — see
// [membershipUpsert]. The list is what gets a set, not what gets a statement.
var Emitted = []*Table{&Users, &Accounts, &Invitations}

// Stamped is the tables whose create still reads its creation time back on its
// own, which is now the invitation alone.
//
// It used to be every emitted table. The user's and the account's creates
// return the row they wrote, read back through the keyed read that already
// exists — a row this transaction just inserted is not archived, so GetUser and
// GetAccount reach it — and a whole row costs the round trip the stamp alone
// cost. Two statements that read one column of a row the caller is being handed
// in full are two statements nothing runs, so they are not emitted; see
// [createdAtReads] and [archivedReads].
//
// The invitation's create still hands its argument back with the stamp written
// onto it, so it is the one table left here. When it stops doing that, this list
// and the loop that reads it go with it.
var Stamped = []*Table{&Invitations}

// Render returns the canonical sqlc input for d: every emitted table's standard
// queries, and beside them every statement this schema runs that a standard set
// does not describe — the keyed reads and lists, the field-specific and guarded
// writes, the role rewrites, and the membership upsert and the writes that
// follow it — in one file's worth of text.
//
// It is what identity/internal/queriesgen writes to the .sql beside this file
// and what CI regenerates to check the committed copy still matches. That .sql
// is sqlc-gen-unison's input, so what the store executes is this text exactly:
// the generated identitydb package carries it per dialect, with the consumer's
// table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table identity owns, not the three the loop below emits for.
	// StandardCRUD registers what it emits, which leaves memberships and the
	// three role tables out — and those are tables with rows in them, so a
	// consumer reading the registry back to truncate a database would miss four
	// of seven. Registering the whole list here is what keeps that list fed by
	// the tables existing rather than by what currently produces their SQL: the
	// role tables are due to join Emitted, and nothing about this line has to
	// change when they do.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query
	for _, table := range Emitted {
		rendered = append(rendered, g.StandardCRUD(table.Name, table.Columns, table.Options()...)...)
	}

	rendered = append(rendered, keyedInvitationLists(g)...)
	rendered = append(rendered, createdAtReads(g)...)
	rendered = append(rendered, archivedReads(g)...)
	rendered = append(rendered, keyedUserReads(g)...)
	rendered = append(rendered, uniquenessChecks(g)...)
	rendered = append(rendered, keyedAccountReads(g)...)
	rendered = append(rendered, keyedMembershipReads(g)...)
	rendered = append(rendered, batchedReads(g)...)
	rendered = append(rendered, junctionLists(g)...)
	rendered = append(rendered, usernamePrefixSearch(g)...)
	rendered = append(rendered, fieldWrites(g)...)
	rendered = append(rendered, userErasure(g))
	rendered = append(rendered, roleWrites(g)...)
	rendered = append(rendered, membershipUpsert(g))
	rendered = append(rendered, membershipWrites(g)...)

	return querygen.RenderFile(rendered)
}

// keyedInvitationLists is the two paged invitation reads the store actually
// runs — pending invitations from one user, and pending invitations addressed
// to one email — each in both directions, since a paged list is two statements. They are list variants rather than standard queries — a keyed
// column and a status predicate on top of the standard list — and before they
// were rendered here, the canonical .sql carried only the unkeyed list while
// the store executed these, which is exactly the checked-versus-executed gap
// the canonical file exists to close.
//
// The status is a bound argument rather than the literal 'pending', for the
// same reason the store binds it: a quoted literal in SQL text is one more
// place a status spelling lives.
func keyedInvitationLists(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}
	status := querygen.Match{Column: InvitationStatusColumn}

	fromUser := g.ListQueries("ListInvitationsByFromUser", InvitationsTable, Invitations.Columns,
		scope, querygen.Match{Column: InvitationFromUserColumn}, status)

	toEmail := g.ListQueries("ListInvitationsByToEmail", InvitationsTable, Invitations.Columns,
		scope, querygen.Match{Column: InvitationToEmailColumn}, status)

	return append(fromUser, toEmail...)
}

// membershipUpsert is the write that puts a user in an account, and the one
// statement in this schema whose three renderings differ beyond their
// placeholders: Postgres and SQLite take ON CONFLICT on the pair, MySQL takes
// ON DUPLICATE KEY UPDATE and no target at all.
//
// It has to converge rather than insert, because the pair is unique across live
// and archived rows alike. A plain INSERT fails when a user rejoins an account
// they once left; a DELETE followed by an INSERT loses when they first joined
// and takes the membership's roles with it, since those hang off the id. So the
// conflict branch clears archived_at and leaves the id and created_at as they
// were, which is what makes a rejoin a revival of the old relationship.
//
// The conflict target is the key rather than the key plus the scope, and it is
// not free to be otherwise: Postgres matches ON CONFLICT against a unique index
// the table actually has, and this schema's is on the pair. Nothing is lost by
// it — a user and an account are each scoped rows, so a pair naming both has
// already named one directory — and the scope is not assigned in the conflict
// branch, so a converging write cannot move a membership between them.
func membershipUpsert(g *querygen.Generator) *querygen.Query {
	return g.UpsertQuery("UpsertMembership", MembershipsTable,
		Memberships.Columns,
		Memberships.InsertColumns(),
		Memberships.UpdateColumns(),
		Memberships.Nullable,
		querygen.Match{Column: MembershipUserColumn},
		querygen.Match{Column: MembershipAccountColumn},
	)
}

// userErasure is the one statement in this schema that destroys a row rather
// than stamping it: the hard DELETE a right-to-be-forgotten request runs.
//
// It is keyed on the id and the scope, and on nothing else. The archived
// predicate every other single-row statement here carries would make this the
// one write unable to reach the rows it exists for, since an erasure follows an
// archival: dataprivacy hides the subject first and destroys them afterwards.
// querygen's delete renders no such predicate — see [querygen.Generator.DeleteQuery].
//
// The user's memberships and every role hanging off them go with the row,
// through ON DELETE CASCADE. That is the one place this schema asks the
// database to finish a deletion, and it is deliberate: an erasure that left a
// membership behind would leave the subject on the rosters of the accounts they
// belonged to.
func userErasure(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("EraseUser", UsersTable, Users.Columns, querygen.Match{Column: ScopeColumn})
}

// roleWrites is the pair of statements each of the three role tables needs: the
// clear that empties an owner's grants, and the insert that writes one back.
//
// Together they are how a role set is replaced wholesale rather than diffed.
// Diffing means reading the current set first and computing two statements from
// it, which is three round trips to express "these are the roles now" and a
// read-modify-write besides.
//
// The insert is one statement per role rather than one multi-row INSERT per
// call. The multi-row form was assembled per call from the caller's cardinality,
// so it had no static text — nothing for sqlc to check and nothing for this
// package to emit, which is the same objection that retired the conditional
// billing SET. What replaces it costs a round trip per role, inside the
// transaction the parent's write already opened, at the cardinalities a role set
// actually has.
//
// Neither statement is scoped, and neither can be: a role table carries no scope
// column, because a grant is reached only through the parent whose own
// statements are all keyed on one. The owner id these bind is a value the store
// read back from a scoped statement.
func roleWrites(g *querygen.Generator) []*querygen.Query {
	rendered := make([]*querygen.Query, 0, len(RoleTables)*2)

	for _, table := range RoleTables {
		owner := querygen.Match{Column: table.OwnerColumn}

		rendered = append(rendered,
			g.DeleteQuery("Delete"+table.Singular+"Roles", table.Name, table.Columns(), owner),
			g.InsertQuery("Insert"+table.Singular+"Role", table.Name, table.Columns(), nil),
		)
	}

	return rendered
}

// fieldWrites is the writes that assign one fact about a user, an account or an
// invitation rather than the row, plus the three that guard the assignment on
// the value being replaced. The membership's own field write is in
// membershipWrites, with the archivals it shares a transaction with.
//
// They are the largest block of statements this schema has and the standard set
// contains none of them, because "assign every mutable column" is the wrong
// shape for all of them. A password change writes the hash, the stamp and the
// forced-change release together, and must not touch a profile column; a status
// move writes the status and its explanation, and must not touch a credential.
// The struct a caller is holding is often a redacted copy, so a whole-row write
// reached from one of these paths blanks whatever it left out.
//
// Three of them are guarded, and the guard is the mechanism rather than a
// belt-and-braces check:
//
//	MarkUserEmailAddressVerified  names the token in the predicate, so two
//	                              clicks on one verification link write once —
//	                              the second finds it already cleared
//	TransferAccountOwnership      names the owner being moved away from, so two
//	                              concurrent transfers cannot both succeed and
//	                              leave the account owned by whichever committed
//	                              last
//	AnswerInvitation              names the pending status, so two answers
//	                              produce one membership rather than an
//	                              acceptance overwritten by a rejection
//
// Each reports zero rows when it loses, which every caller reads as the entity
// not being there — see SQLStore's guardCount.
//
// The values the guards compare against are bound rather than written into the
// SQL as literals, for the reason the keyed invitation lists bind the status: a
// quoted literal is one more place a spelling lives.
func fieldWrites(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	return []*querygen.Query{
		g.UpdateQuery("UpdateUserPassword", UsersTable, Users.Columns,
			[]string{hashedPasswordColumn, requiresPasswordChangeColumn, passwordLastChangedAtColumn},
			Users.Nullable, scope),

		g.UpdateQuery("SetUserRequiresPasswordChange", UsersTable, Users.Columns,
			[]string{requiresPasswordChangeColumn}, Users.Nullable, scope),

		// The secret and its verification move together. Two statements would
		// leave a window in which a freshly issued secret reads as already
		// proven, which is a window in which a second factor is bypassed by
		// re-enrolling.
		g.UpdateQuery("UpdateUserTwoFactorSecret", UsersTable, Users.Columns,
			[]string{twoFactorColumn, twoFactorVerifiedAtColumn}, Users.Nullable, scope),

		// The proof that a secret was exercised, guarded on there being a
		// secret and on its not having been proven already. Neither conjunct
		// is an equality against a value the caller holds: one is the
		// not-empty sentinel and the other is IS NULL, and together they are
		// what makes a replayed verification write nothing and report zero
		// rows rather than move the timestamp forward.
		g.UpdateQuery("MarkUserTwoFactorSecretVerified", UsersTable, Users.Columns,
			[]string{twoFactorVerifiedAtColumn}, Users.Nullable,
			scope,
			querygen.Match{Column: twoFactorColumn, Against: querygen.EmptyString, Exclude: true},
			querygen.Match{Column: twoFactorVerifiedAtColumn, Against: querygen.NoValue}),

		// Issuing a link drops the proof, for the reason enrolling a second
		// factor drops its verification: the two columns are one state, and a
		// row holding both says the address is proven and has an outstanding
		// link at the same time. Which of the two a reader believes is then a
		// question about which column it happened to look at.
		g.UpdateQuery("SetUserEmailAddressVerificationToken", UsersTable, Users.Columns,
			[]string{UserEmailVerificationTokenColumn, EmailAddressVerifiedAtColumn}, Users.Nullable, scope),

		g.UpdateQuery("MarkUserEmailAddressVerified", UsersTable, Users.Columns,
			[]string{EmailAddressVerifiedAtColumn, UserEmailVerificationTokenColumn}, Users.Nullable,
			scope,
			querygen.Match{Column: UserEmailVerificationTokenColumn, Arg: currentEmailVerificationTokenArg}),

		// The other direction, and the one nothing else can express: the proof
		// comes off an address the user keeps. Unguarded on purpose — it is the
		// safe direction, so an unverify that lost a race to another unverify
		// has still left the row where the caller wanted it.
		//
		// It writes the proof alone rather than the pair. Any outstanding link
		// was minted for this address, which has not changed, so burning it
		// would cost the user a round trip to prove an address they are already
		// being asked to prove.
		g.UpdateQuery("MarkUserEmailAddressUnverified", UsersTable, Users.Columns,
			[]string{EmailAddressVerifiedAtColumn}, Users.Nullable, scope),

		g.UpdateQuery("UpdateUserAccountStatus", UsersTable, Users.Columns,
			[]string{accountStatusColumn, accountStatusExplanationColumn}, Users.Nullable, scope),

		g.UpdateQuery("TransferAccountOwnership", AccountsTable, Accounts.Columns,
			[]string{ownerUserIDColumn}, Accounts.Nullable,
			scope,
			querygen.Match{Column: ownerUserIDColumn, Arg: currentOwnerUserIDArg}),

		// The billing writes, one statement per event rather than one statement
		// per column. They were a single statement whose SET list was assembled
		// per call from four optional fields, which is dynamic SQL by
		// construction — there was no static text for sqlc to check or for this
		// package to emit.
		//
		// The tempting static rewrite is one statement with a
		// COALESCE(sqlc.narg(column), column) per field, and it is wrong here in
		// a way that only shows up on the write nobody tests: subscription_plan_id
		// is nullable, so under that encoding NULL means "leave it alone" and a
		// cancellation — set the plan to NULL — becomes inexpressible. Naming the
		// columns keeps the whole domain writable, because the argument saying
		// what a column becomes is not also the argument saying whether to write
		// it.
		//
		// The shapes are the events an application actually handles, which is
		// not the same list as the columns. A processor delivery reports a
		// subscription's standing and the plan it is on together, and the
		// reconciliation stamp goes with them: those three columns move as one
		// fact, so a statement per column would write them in three round trips
		// with two intermediate states — an account that is paid on last month's
		// plan being the one somebody notices.
		g.UpdateQuery("RecordAccountSubscription", AccountsTable, Accounts.Columns,
			[]string{billingStatusColumn, subscriptionPlanIDColumn, billingSyncedAtColumn},
			Accounts.Nullable, scope),

		// The standing alone, for the move no processor reports: an operator
		// suspending an account. It stamps no reconciliation because none
		// happened — nothing was asked of the processor — and a statement that
		// stamped one would date the account's billing state to an operator's
		// click.
		g.UpdateQuery("SetAccountBillingStatus", AccountsTable, Accounts.Columns,
			[]string{billingStatusColumn}, Accounts.Nullable, scope),

		g.UpdateQuery("SetAccountPaymentProcessorCustomerID", AccountsTable, Accounts.Columns,
			[]string{paymentProcessorCustomerIDColumn}, Accounts.Nullable, scope),

		// The stamp alone, for the reconciliation that found nothing moved —
		// which is the run whose result the next run needs most.
		g.UpdateQuery("MarkAccountBillingSynced", AccountsTable, Accounts.Columns,
			[]string{billingSyncedAtColumn}, Accounts.Nullable, scope),

		// The answer writes its own note, never the sender's. `note` is the
		// message the invitation was sent with — the one rendered into the
		// email and shown beside the invitation afterwards — and a SET list
		// naming it here would destroy that message every time somebody
		// replied.
		g.UpdateQuery("AnswerInvitation", InvitationsTable, Invitations.Columns,
			[]string{InvitationStatusColumn, invitationStatusNoteColumn, invitationToUserColumn},
			Invitations.Nullable,
			scope,
			querygen.Match{Column: InvitationStatusColumn, Arg: currentInvitationStatusArg}),

		// The agreement stamps, one statement per document rather than one
		// statement whose SET list was chosen per call from the documents a
		// caller named. That assembly was the last update in this schema with
		// no static text — accepting the terms rendered one SQL string and
		// accepting both rendered another, so there was nothing for sqlc to
		// check and nothing for this package to emit. Two statements run in one
		// transaction say the same thing, and each of them is text.
		g.UpdateQuery("RecordUserTermsOfServiceAgreement", UsersTable, Users.Columns,
			[]string{termsOfServiceColumn}, Users.Nullable, scope),

		g.UpdateQuery("RecordUserPrivacyPolicyAgreement", UsersTable, Users.Columns,
			[]string{privacyPolicyColumn}, Users.Nullable, scope),
	}
}

// membershipWrites is what happens to a membership row after the upsert has put
// it there: which of a user's accounts they land in, and the end of the
// relationship.
//
// Every one of them keys on Memberships.KeyedColumns() rather than on the id,
// like the keyed reads above and for the same reason — the pair is the row's
// natural key, and the id is what a membership's roles hang off rather than
// what anything addresses it by. The three archivals differ only in how much of
// the pair they name: one membership, every membership a user holds, every
// membership in an account.
//
// The flag's new value is bound rather than written into the statement as
// FALSE, because an assignment querygen renders is an assignment to an
// argument. What keeps that from being a statement that can set the flag as
// easily as clear it is the guard beside it: the same argument again, excluded,
// so the write reaches only the rows whose flag differs from the one being
// assigned. Clearing therefore touches the rows that were TRUE and nothing
// else, which is what the hand-built statement's `default_account = TRUE`
// predicate said, spelled as one argument rather than two.
//
// The archivals no longer clear the flag themselves — a soft delete stamps
// archived_at and nothing else, here as everywhere else in this schema — so the
// clear is the statement before them in the transaction. That is one more round
// trip and one fewer place the archived stamp comes from the application's
// clock instead of the server's.
func membershipWrites(g *querygen.Generator) []*querygen.Query {
	var (
		columns = Memberships.KeyedColumns()
		flag    = []string{MembershipDefaultColumn}
		scope   = querygen.Match{Column: ScopeColumn}
		user    = querygen.Match{Column: MembershipUserColumn}
		account = querygen.Match{Column: MembershipAccountColumn}

		// The rows whose flag is not already what it is about to become.
		changing = querygen.Match{Column: MembershipDefaultColumn, Exclude: true}
	)

	return []*querygen.Query{
		g.UpdateQuery("SetMembershipDefaultAccount", MembershipsTable, columns,
			flag, Memberships.Nullable, scope, user, account),

		// The other side of "one default per user": the flag comes off every
		// other membership the user holds. The spared account is optional
		// because two callers want the same statement with and without one —
		// a write that has just marked one membership as the default spares it,
		// and an archival that is ending all of them spares none. An absent
		// argument coalesces to the empty string, which is an account id no row
		// has.
		g.UpdateQuery("ClearMembershipDefaultAccountsForUser", MembershipsTable, columns,
			flag, Memberships.Nullable,
			scope, user, changing,
			querygen.Match{
				Column:  MembershipAccountColumn,
				Arg:     exceptAccountIDArg,
				Against: querygen.OptionalArgument,
				Exclude: true,
			}),

		g.UpdateQuery("ClearMembershipDefaultAccountsForAccount", MembershipsTable, columns,
			flag, Memberships.Nullable, scope, account, changing),

		g.ArchiveQuery("ArchiveMembership", MembershipsTable, columns, scope, user, account),
		g.ArchiveQuery("ArchiveMembershipsForUser", MembershipsTable, columns, scope, user),
		g.ArchiveQuery("ArchiveMembershipsForAccount", MembershipsTable, columns, scope, account),
	}
}

// createdAtReads is the read-back of the one column an emitted table's create
// does not carry: the creation time the database assigned it.
//
// created_at is database-owned — it is not in any create's column list, and the
// schema gives it a DEFAULT — so the value the caller handed over still holds
// the zero time when the INSERT returns, and the store reads it back inside the
// same transaction. One per table in [Stamped], because a query name is a Go
// method name and the table is not a parameter of one.
//
// It keys on the id alone. The scope is absent because this is not a read a
// caller reaches: it is the create's read-back of the row it has just written,
// by the id it minted for it, and the row is not visible to anything else until
// the transaction commits. The column list is the id and nothing else, which is
// also what leaves the archived predicate off a row that cannot be archived yet.
func createdAtReads(g *querygen.Generator) []*querygen.Query {
	rendered := make([]*querygen.Query, 0, len(Stamped))

	for _, table := range Stamped {
		rendered = append(rendered, g.ReadQuery(
			"Get"+table.Singular+"CreatedAt", table.Name,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
		))
	}

	return rendered
}

// archivedReads is the pair of reads the two archivals answer with: the user or
// the account the write just hid, on the transaction that hid it.
//
// They exist because archiving is the write whose result no other read here can
// see. Every single-row statement over these two tables filters archived_at IS
// NULL — which is what makes an archived user absent from GetUser and from every
// Principal built out of it — so a store that archived a row and read it back
// through the ordinary keyed read would find nothing, and once the transaction
// commits the row is unreachable through this package's reads entirely. What a
// consumer's audit entry says was removed is that row.
//
// Each is rendered from no column list at all, which is the trick the uniqueness
// checks play for the opposite half of its reason: querygen derives the archived
// predicate from the columns it is handed, so a read that must see archived rows
// is one keyed entirely on its matches. What takes that predicate's place is its
// complement — archived_at IS NOT NULL — so the read-back asserts the thing it
// was called to confirm, and a guard that matched nothing cannot be read back as
// a success.
//
// There is no third for the memberships an archival ends alongside the row, and
// no companion for a create or an update. Neither of those leaves the directory,
// so the ordinary keyed read reaches both on the transaction that wrote them,
// and the read-back each makes is that statement rather than one of its own —
// which is why two of the three createdAtReads above are gone: a create that
// reads the whole row back has no use for a statement that reads one column of
// it.
func archivedReads(g *querygen.Generator) []*querygen.Query {
	var (
		scope    = querygen.Match{Column: ScopeColumn}
		archived = querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true}
	)

	return []*querygen.Query{
		g.ReadQuery("GetArchivedUser", UsersTable, nil,
			querygen.Read{Projection: Users.Columns},
			querygen.Match{Column: querygen.IDColumn}, scope, archived),

		g.ReadQuery("GetArchivedAccount", AccountsTable, nil,
			querygen.Read{Projection: Accounts.Columns},
			querygen.Match{Column: querygen.IDColumn}, scope, archived),
	}
}

// keyedUserReads is the three single-user reads that key on something other than
// the id: the two sign-in reads and the one behind an email verification.
//
// They were one builder parameterized on the column, which is not a thing a
// query name can be — sqlc turns a name into a Go method — so they enumerate
// here into one named read each. What the builder was protecting is preserved
// by construction instead: the scope predicate and the archived clause are
// querygen's, rendered from one column list, so the sign-in read cannot be the
// one that forgot either.
func keyedUserReads(g *querygen.Generator) []*querygen.Query {
	read := querygen.Read{Projection: Users.Columns}

	// Statement name to the column it keys on, in the order the file lists
	// them. A map would lose that order, and the .sql is compared byte for byte
	// against its committed copy.
	named := [][2]string{
		{"GetUserByUsername", UserUsernameColumn},
		{"GetUserByEmailAddress", UserEmailAddressColumn},
		{"GetUserByEmailVerificationToken", UserEmailVerificationTokenColumn},
	}

	rendered := make([]*querygen.Query, 0, len(named))
	for i := range named {
		rendered = append(rendered, g.ReadQuery(named[i][0], UsersTable, Users.KeyedColumns(), read,
			querygen.Match{Column: named[i][1]}, querygen.Match{Column: ScopeColumn}))
	}

	return rendered
}

// uniquenessChecks is the pair of collision reads CreateUser and UpdateUser run
// before writing, so a taken handle reports ErrUsernameTaken or
// ErrEmailAddressTaken rather than a driver's constraint violation.
//
// Two things about them are not the rest of this file's shape.
//
// Neither filters on archived_at, and that is the schema's requirement rather
// than an omission. The unique indexes cover archived rows — freeing a username
// when its owner is soft-deleted means a later registrant can take it, and every
// audit row naming that handle then refers to two people — so a check that
// skipped archived rows would report the handle free and hand the write to the
// index, which is the driver error these reads exist to prevent. The column list
// is empty for exactly that reason: querygen renders the archived predicate from
// the column list, so a read that must see archived rows is a read rendered from
// no columns at all, keyed entirely on its matches.
//
// And the row being updated is excluded through an argument the caller may
// leave unset, which is what lets one statement serve both callers. A profile
// save must not collide with itself; a registration has no id to exclude yet.
// Under the presence-conditional comparand the absent argument coalesces to the
// empty string and excludes an id no row has, so the statement a registration
// runs is the statement a save runs, checked once.
//
// They are two named statements rather than one parameterized on the column, for
// the reason keyedUserReads enumerates: a query name is a Go method name, and a
// column is not a parameter of one.
func uniquenessChecks(g *querygen.Generator) []*querygen.Query {
	read := querygen.Read{Projection: []string{querygen.IDColumn}}

	exceptID := querygen.Match{
		Column:  querygen.IDColumn,
		Against: querygen.OptionalArgument,
		Arg:     exceptUserIDArg,
		Exclude: true,
	}

	// Statement name to the column it keys on, in the order the file lists
	// them — a map would lose that order, and the .sql is compared byte for
	// byte against its committed copy.
	named := [][2]string{
		{"GetUserIDByUsername", UserUsernameColumn},
		{"GetUserIDByEmailAddress", UserEmailAddressColumn},
	}

	rendered := make([]*querygen.Query, 0, len(named))
	for i := range named {
		rendered = append(rendered, g.ReadQuery(named[i][0], UsersTable, nil, read,
			querygen.Match{Column: named[i][1]}, querygen.Match{Column: ScopeColumn}, exceptID))
	}

	return rendered
}

// keyedAccountReads is the one account read that keys on something other than
// the id: an account the user owns.
//
// It is the guard behind ArchiveUser, and it is a read rather than an existence
// check because the answer a caller needs is which account blocked. An owner
// archived out from under their accounts leaves them live and owned by a user
// every scoped read reports as absent, so the refusal has to name the account
// whose ownership has to move first — the same sentence RemoveMembership's
// refusal already carries, which can name it because it was handed it.
//
// A user may own several; the ordering is what makes "one of them" a row rather
// than whichever one the planner reached first, so a refusal repeated against
// an unchanged directory names the same account twice.
func keyedAccountReads(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.ReadQuery("GetOwnedAccountIDForUser", AccountsTable, Accounts.KeyedColumns(),
			querygen.Read{
				Projection: []string{querygen.IDColumn},
				Order:      querygen.IDColumn,
			},
			querygen.Match{Column: ScopeColumn},
			querygen.Match{Column: ownerUserIDColumn}),
	}
}

// keyedMembershipReads is the four reads over the table that has no standard
// queries at all: the membership itself, the id a revived one kept, the account
// a user falls back to when the default is being removed, and whether the user
// belongs to anything at all.
//
// None of them keys on the id the table carries — the (user, account) pair is
// the row's natural key, and the last of them keys on the user alone — which is
// why each is rendered from Memberships.KeyedColumns() while projecting from
// Memberships.Columns.
func keyedMembershipReads(g *querygen.Generator) []*querygen.Query {
	var (
		columns = Memberships.KeyedColumns()
		scope   = querygen.Match{Column: ScopeColumn}
		user    = querygen.Match{Column: MembershipUserColumn}
		account = querygen.Match{Column: MembershipAccountColumn}
	)

	return []*querygen.Query{
		g.ReadQuery("GetMembershipByUserAndAccount", MembershipsTable, columns,
			querygen.Read{Projection: Memberships.Columns}, scope, user, account),

		// The id the row actually carries, which is not always the id the
		// caller generated: a user rejoining an account revives the archived
		// membership, and it keeps the id it was created with. The roles are
		// written against this one.
		g.ReadQuery("GetMembershipIDByUserAndAccount", MembershipsTable, columns,
			querygen.Read{Projection: []string{querygen.IDColumn}}, scope, user, account),

		// Another live membership for the user, for moving the default off the
		// one being removed — so a user is never left with memberships and
		// nowhere to land. The account is excluded rather than matched, and the
		// order is what makes "another" a row rather than whichever one the
		// planner reached first.
		g.ReadQuery("GetMembershipFallbackAccountID", MembershipsTable, columns,
			querygen.Read{
				Projection: []string{MembershipAccountColumn},
				Order:      MembershipAccountColumn,
			},
			scope, user, querygen.Match{Column: MembershipAccountColumn, Exclude: true}),

		// Whether the user belongs to anything yet, which is how a membership
		// write finds out that the one it is about to make is their first and
		// therefore their default.
		//
		// It reads an id rather than counting, because the caller compares
		// against zero and a COUNT that answers seven has spent the server's
		// time distinguishing seven from one. Ordering makes "one of them" a
		// row rather than whichever the planner reached first; no rows is the
		// answer the caller acts on.
		g.ReadQuery("GetMembershipIDForUser", MembershipsTable, columns,
			querygen.Read{
				Projection: []string{querygen.IDColumn},
				Order:      querygen.IDColumn,
			},
			scope, user),
	}
}

// batchedReads is the four reads that answer for a whole batch of keys at once:
// the users a page's rows refer to, and the roles hanging off a page of users,
// memberships or invitations.
//
// Each is the read a page would otherwise make one query at a time. A roster of
// thirty members whose roles were fetched inside the loop that converts rows is
// thirty round trips returning two rows each, and the loop is where that shape
// arrives without anybody choosing it — which is why the batched form is the
// one that exists rather than an optimization applied later.
//
// All four were hand-assembled IN lists until querygen learned the shape, and
// they were the last reads in this package that no generator could render. The
// store still owes them the one thing the statement cannot express: an empty
// batch, answered without a query rather than sent as one whose answer is
// already known — see querygen.Generator.SetReadQuery.
func batchedReads(g *querygen.Generator) []*querygen.Query {
	// The users a page's rows point at, read for hydration rather than as a
	// listing: "who created each of these rows". The column list has no
	// archived_at in it, which is how a statement says the archived predicate
	// does not apply — hiding a soft-deleted user here turns "created by a
	// departed colleague" into "created by nobody". The projection is still the
	// whole table, archived_at included, so a caller can see that they are
	// looking at one.
	rendered := []*querygen.Query{
		g.SetReadQuery("ListUsersByIDs", UsersTable,
			Users.ColumnsExcept(querygen.ArchivedAtColumn),
			querygen.Read{Projection: Users.Columns},
			querygen.SetKey{Column: querygen.IDColumn},
			querygen.Match{Column: ScopeColumn}),
	}

	// Statement name to the table and the parent column it keys on, in the
	// order the file lists them. One named read each rather than one builder
	// over a table parameter, for the reason keyedUserReads enumerates: a query
	// name is a Go method name, and a method name is not a parameter.
	//
	// None carries a scope: a role table has no scope column, because the
	// parent whose id keys the read is the scoped row. The store reaches these
	// with ids it read through a scoped statement.
	named := [][3]string{
		{"ListUserRolesByUserIDs", UserRolesTable, UserRoleOwnerColumn},
		{"ListMembershipRolesByMembershipIDs", MembershipRolesTable, MembershipRoleOwnerColumn},
		{"ListInvitationRolesByInvitationIDs", InvitationRolesTable, InvitationRoleOwnerColumn},
	}

	for i := range named {
		// Ordered by the parent and then by the role, which is what makes the
		// roles of one owner arrive together and in a stable order — a set the
		// caller compares against another set, or renders, should not come back
		// in whichever order the planner found convenient.
		rendered = append(rendered, g.SetReadQuery(named[i][0], named[i][1],
			[]string{named[i][2], RoleColumn},
			querygen.Read{Order: RoleColumn},
			querygen.SetKey{Column: named[i][2]}))
	}

	return rendered
}

// junctionLists is the four unpaged-or-joined reads over the membership
// junction: an account's roster, the accounts a user belongs to, a user's own
// memberships, and the memberships that name one account as their default.
//
// The first two cross the junction; the last two are the same builder with no
// join at all, which is what an unpaged list keyed on a match is here. They sit
// together because a page of one table reached through another and a whole
// answer over one table are the same statement with everything a page implies
// removed — see [querygen.Generator.JunctionListAllQuery].
//
// The joined pair were the last statements identity ran that no generator could render,
// because every other query here projects one table's columns and these span
// two. That is what kept a hand-paired two-entity scanner alive after everything
// single-table had been ported — a projection and a list of scan targets written
// in two files, where a mismatch is a runtime scan error rather than a failed
// build.
//
// Which table is listed and which is joined is decided by what a page is a page
// of, and the two differ here. A roster is a page of memberships with the member
// attached, so memberships is listed and its cursor is the membership id; a
// user's account list is a page of accounts reached through memberships, so
// accounts is listed and the membership contributes only its key. Reversing
// either would page over an id the caller never sees.
func junctionLists(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	// The roster. The user's columns are projected beside the membership's
	// under a user_ prefix, so a page of thirty members is one query rather
	// than thirty-one, and the two tables' shared column names — id, scope,
	// created_at — stay distinguishable in the row type.
	roster := g.JunctionListQueries("ListAccountMembers", MembershipsTable, Memberships.Columns,
		&querygen.Junction{
			Table:    UsersTable,
			Column:   querygen.IDColumn,
			OnColumn: MembershipUserColumn,
			Columns:  Users.Columns,
			Prefix:   "user",
		},
		scope, querygen.Match{Column: MembershipAccountColumn})

	// The accounts a user is a live member of. The membership's columns are
	// declared and not projected: the caller wants accounts, and what the
	// junction owes the statement is its key and the requirement that the
	// membership itself has not been archived — a user removed from an account
	// they are still nominally listed against would otherwise keep seeing it in
	// their switcher.
	forUser := g.JunctionListQueries("ListAccountsForUser", AccountsTable, Accounts.Columns,
		&querygen.Junction{
			Table:    MembershipsTable,
			Column:   MembershipAccountColumn,
			OnColumn: querygen.IDColumn,
			Columns:  Memberships.Columns,
			Matches:  []querygen.Match{{Column: MembershipUserColumn}},
		},
		scope)

	// A user's memberships, default account first — so a caller that takes the
	// first row gets the one the user lands in. Unpaged and unjoined: it answers
	// "where may this principal act", which is every row or none of them, and
	// the account behind each is read separately when it is read at all.
	//
	// It is the one list here with no descending half, and the reason is that it
	// is not paged: it takes no filtering.QueryFilter, so there is no direction
	// to answer. Its order is the caller's, named right here.
	memberships := g.JunctionListAllQuery("ListMembershipsForUser", MembershipsTable, Memberships.Columns,
		nil,
		[]querygen.Order{
			{Column: MembershipDefaultColumn, Descending: true},
			{Column: MembershipAccountColumn},
		},
		scope, querygen.Match{Column: MembershipUserColumn})

	// The members who land in one account, which is who archiving it is about
	// to strand. The archival moves each of their defaults to another live
	// membership, so the answer it needs is every one of these rows rather than
	// a page of them — unpaged for the same reason the list above it is.
	//
	// The flag is a bound argument rather than a literal TRUE, the way every
	// other predicate over it here is one: a value spelled into SQL text is a
	// value that can disagree with the one the store binds.
	//
	// It orders by the member, so an archival that has to be repeated against
	// an unchanged directory does the same work in the same order.
	stranded := g.JunctionListAllQuery("ListDefaultMembershipsForAccount", MembershipsTable, Memberships.Columns,
		nil,
		[]querygen.Order{{Column: MembershipUserColumn}},
		scope,
		querygen.Match{Column: MembershipAccountColumn},
		querygen.Match{Column: MembershipDefaultColumn})

	return append(append(roster, forUser...), memberships, stranded)
}

// usernamePrefixSearch is the directory's search: the page of users whose
// username begins with what somebody typed, and the count of everything that
// prefix matched.
//
// It is a pair rather than a list variant because a search is not the standard
// list keyed on another column. It orders by the username, which is the column
// its cursor pages over — a search that ordered by id would page in creation
// order while the caller reads a list sorted by name — and its count is a
// second statement rather than a subquery riding on the rows, since the number
// a caller wants is of everything the prefix matched rather than of what is
// left after the cursor.
//
// The scope is a Match like every other statement's here, so a search cannot
// answer across directories.
func usernamePrefixSearch(g *querygen.Generator) []*querygen.Query {
	return g.PrefixSearchQueries(UsersTable, Users.Columns,
		querygen.PrefixSearch{
			Column:    UserUsernameColumn,
			Name:      "SearchUsersByUsername",
			CountName: "CountSearchUsersByUsername",
		},
		querygen.Match{Column: ScopeColumn})
}

// FileName is the file one dialect's rendered queries are committed to.
//
// The _generated suffix is in the path rather than only in the header comment,
// because a path is what a reviewer sees in a diff, what CI's glob selects, and
// what a reader scanning this directory reads first — and these are the files
// whose answer to "this line is wrong" is to edit something else.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}
