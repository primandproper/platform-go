package waitlists

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the whole of what this package persists: the lists, and the people
// queueing on them.
//
// It is two interfaces because they have two callers. A ListStore is reached by
// whatever administers a deployment — an admin console, a seeding job, whoever
// decides a launch is happening — and a SignupStore is reached on the request
// path, by the form somebody fills in and by the operator working through the
// queue. Splitting them is what lets a component depend on the half it uses;
// Store is here for the wiring that provides both.
//
// # Every write takes the caller's transaction
//
// Each write below reads (ctx, tx database.Tx, scope tenancy.Scope, ...), and
// there is no form that opens a transaction of its own. A database.Tx is
// producible only by database.RunInTransaction — which is to say, by
// database.Client.WithTransaction — so the signature is a compile-time claim
// that the caller is already inside one.
//
// That is the point rather than a formality. A row in a consumer's schema is
// rarely written alone: an audit entry naming who did what and a data change
// event on an outbox somebody fans out are the ordinary companions of a signup,
// and a companion is worth what its atomicity with the row is worth. A store
// that committed on its own behalf would put them in a second transaction, so a
// refused audit entry would leave a signup nothing downstream had been told
// about. Invite and Convert are exactly the moments an application sends an
// email and writes an audit entry, which is why this store is the one where
// that mattered most.
//
// A caller with nothing to commit alongside opens one anyway, and it is one
// line:
//
//	err := client.WithTransaction(ctx, func(tx database.Tx) error {
//		_, joinErr := store.Join(ctx, tx, scope, listID, signup)
//
//		return joinErr
//	})
//
// A nil tx is an error wrapping ErrNilExecutor rather than a fallback to a
// connection of the store's own, because there is no such connection: see
// NewSQLStore.
//
// # Every read takes an executor
//
// Each read reads (ctx, q database.SQLQueryExecutor, scope tenancy.Scope, ...),
// which is the wider type deliberately. A database.Tx satisfies it, so one read
// serves both a caller holding Client.Reader() and a caller inside a
// transaction — and the second sees that transaction's own uncommitted writes.
// A read narrowed to the reader would be reading a database that does not yet
// hold the row its caller just wrote, which is what "GetSignup right after Join"
// was before this shape.
//
// The reads the writes make on their own behalf run on the same executor: a Join
// against a list created earlier in the same transaction finds it, and a
// Withdraw that matched nothing explains itself against the row as this
// transaction sees it. The signups counter is fed when the statement lands,
// which is before the caller commits — see SQLStore.countSignups.
//
// # Nine writes answer with the row they wrote
//
// Every write here but one hands back a row: the two creates, the two updates,
// the two transitions, the withdrawal and the two retirements. None of them
// touches the value it was handed — mutating the argument and returning deliver
// the same guarantee, and returning is the one spelling available to a write
// addressed by an id rather than by an entity, so it is the one this module
// uses.
//
// What they have in common is that the answer is not something the caller could
// have assembled. A create settles an id and a creation time the database
// stamped. An update leaves the columns it deliberately does not assign, and
// last_updated_at is the database's. A transition stamps StatusChangedAt from
// this Store's clock, which is the one value on the row a caller cannot name and
// is the instant a consumer schedules a reminder off. A retirement puts the row
// beyond every single-row read here, so the moment it returns is the last one at
// which anything answers for it by its id.
//
// Withdraw is the sharpest case and the one that decided the shape. It blanks
// the contact, the notes and the subject reference, so a read afterwards answers
// with a status and a digest and nothing else — the row it hands back is
// therefore the row as it stood *before* the statement, read on the same
// transaction immediately in front of it. That is the only moment at which the
// values a consumer's own record needs still exist.
//
// A refused write answers with a nil row. The sentinels are unchanged and none
// of them arrives beside a value: the row comes back only alongside a nil error,
// so a guarded transition that matched nothing still reports ErrWrongStatus,
// ErrAlreadyWithdrawn or ErrSignupNotFound and hands back nothing.
//
// WithdrawSignupsForSubject is the one write that could answer with what it
// erased and does not. It reports how many rows went, because the rows are the
// subject's own data — the address and the account reference — and handing them
// back would be handing back exactly what the erasure exists to remove.
//
// # The scope is an argument, including where a whole entity carries one
//
// CreateList, UpdateList and Join take both a tenancy.Scope and a value with a
// Scope field of its own, and it is the argument the statement binds. Reading it
// off the entity instead was the rejected alternative, and it is named here so
// that it is not re-proposed: a scope derived from a field the caller assembled
// somewhere else is exactly the derivation the column rule exists to rule out.
//
// An entity naming a different scope than the write is refused with
// ErrScopeMismatch rather than corrected, because the two disagreeing is a
// caller holding one tenant's row and writing it into another — a stale value or
// a mix-up, and not a thing to guess at. One naming none adopts the argument;
// tenancy.Scope tells its zero value from Global(), so "unset" there is genuinely
// unset rather than the global scope spelled shortly.
type Store interface {
	ListStore
	SignupStore
}

// ListStore is the catalog: what lists exist, what they are for, and when each
// stops taking signups.
//
// Every method takes a tenancy.Scope, and a deployment with one catalog passes
// tenancy.Global() to all of them. There is deliberately no unscoped variant of
// any of these — see the tenancy package.
type ListStore interface {
	// CreateList opens a waitlist in the caller's transaction and returns it as
	// stored, with the id it was minted under and the creation time the database
	// assigned.
	//
	// It refuses a list with no name and one with no closing time. There is no
	// default for the latter — see ErrEmptyClosesAt. A list naming a different
	// scope than the write is ErrScopeMismatch; see Store.
	CreateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) (*List, error)

	// GetList reads one live list by id, on the caller's executor — so a caller
	// inside a transaction reads the list that transaction has written and not
	// yet committed.
	GetList(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, listID string) (*List, error)

	// ListLists pages the scope's catalog, open and closed alike. It is the
	// administrative read.
	ListLists(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[List], error)

	// ListOpenLists pages the lists still taking signups: live, and not yet
	// past their closing time as of the store's clock.
	//
	// It is the public read — what a "join the waitlist" page offers — and it
	// is a separate statement rather than a filter applied to ListLists'
	// results, because a page filtered after the fact is a page whose size the
	// caller cannot rely on.
	ListOpenLists(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[List], error)

	// UpdateList rewrites a list's name, description and closing time, in the
	// caller's transaction, and answers with the list as stored.
	//
	// Moving the closing time is how a list is extended or brought in, and it
	// is not guarded against the signups already on it: a list closed early
	// keeps everybody who joined while it was open, and reopening one lets the
	// next person through. What it will not do is revive an archived list.
	//
	// The List passed in is read and not written to. The row that comes back
	// carries the creation time and the last-updated stamp, both of which are
	// the database's rather than anything the caller assembled — see Store.
	UpdateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) (*List, error)

	// ArchiveList retires a list, in the caller's transaction, and answers with
	// the list it retired.
	//
	// The signups against it are left alone and stay readable, because archiving
	// is not erasure. What it does do is close the list to new signups
	// immediately, whatever its closing time says — see List.OpenAt.
	//
	// The list it hands back is the last thing here that answers for that row by
	// its id. GetList excludes archived lists, as every single-row read here
	// does, so once this commits the only way back to the name the surviving
	// signups were queueing for is to page the whole catalog with the filter's
	// IncludeArchived set.
	ArchiveList(ctx context.Context, tx database.Tx, scope tenancy.Scope, listID string) (*List, error)
}

// SignupStore is the queue: who is on a list, where they stand, and what
// happens when they ask to come off it.
//
// Every method takes both the scope and the list, save the two that are about
// a person rather than a place in a queue. The list is half of what addresses a
// signup, and a read that omitted it would be a read that could hand one list's
// row to a caller holding another list's id.
type SignupStore interface {
	// Join adds somebody to a list, in the caller's transaction — so the signup
	// commits with whatever the caller writes beside it.
	//
	// It refuses a list that is closed or missing (ErrListClosed,
	// ErrListNotFound), a contact already on the list (ErrAlreadySignedUp), and
	// a contact that has withdrawn from it (ErrContactWithdrawn) — the last of
	// which is the obligation this package is shaped around and outlives the
	// address it is about.
	//
	// The list read and the suppression check run on tx, so a signup against a
	// list opened earlier in the same transaction goes in.
	//
	// The signup's Contact is stored as it was given and digested as
	// [Normalize] renders it, so two capitalizations of one address are one
	// person. Status and the timestamps are the store's; whatever the caller
	// set on them is ignored. The scope is the argument's, and a signup naming a
	// different one is ErrScopeMismatch; see Store.
	Join(ctx context.Context, tx database.Tx, scope tenancy.Scope, listID string, signup *Signup) (*Signup, error)

	// GetSignup reads one live signup by id, on the list it belongs to, through
	// the caller's executor.
	GetSignup(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, listID, signupID string) (*Signup, error)

	// GetSignupByContact reads one live signup by the address it was made with,
	// which is the read behind "am I already on this list" and the read an
	// unsubscribe link resolves.
	//
	// The contact is normalized and digested, so it is found by whichever
	// capitalization the caller has. A withdrawn signup is still a live row and
	// comes back as one, with its contact blank and its status saying why —
	// which is what lets an unsubscribe page tell somebody they are already off
	// the list rather than that they were never on it.
	GetSignupByContact(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, listID, contact string) (*Signup, error)

	// ListSignups pages one list's live signups, oldest first by default, which
	// is the order they joined in.
	ListSignups(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, listID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Signup], error)

	// ListSignupsForSubject pages the signups belonging to one principal across
	// every list in the scope. It is the read a profile page makes and the one a
	// data privacy export walks.
	//
	// Live signups by default. The filter's IncludeArchived reaches the archived
	// ones too, and an export sets it: an archived signup still holds the
	// address it was made with, and an export that omitted it would be missing
	// data the table holds. waitlists/privacy is the collector built on this.
	//
	// A withdrawn signup is never among them: a withdrawal blanks the subject
	// reference along with the contact, so the row that remembers a suppression
	// no longer says whose it was.
	ListSignupsForSubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Signup], error)

	// UpdateSignupNotes rewrites the operator's note against a signup, in the
	// caller's transaction, and answers with the signup as stored.
	//
	// It is the one write that touches a signup without moving it, and it
	// deliberately leaves StatusChangedAt alone — a typo fixed in a note must
	// not reschedule the reminder somebody's invitation started. The row it
	// hands back is what shows that: the note is the new one and the status pair
	// is where it was.
	UpdateSignupNotes(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		listID, signupID, notes string,
	) (*Signup, error)

	// Invite moves a waiting signup to invited and stamps the moment, in the
	// caller's transaction — so the invitation and the record of who sent it
	// land together or not at all — and answers with the signup it moved.
	//
	// It refuses anything that is not waiting with ErrWrongStatus, and the
	// refusal is the affected-row count of a guarded update rather than a
	// decision made on a read — so two requests inviting the same person send
	// one email between them. A refused move answers with a nil signup; the row
	// comes back only beside a nil error.
	//
	// That row is what the invitation is actually sent from. It carries the
	// contact to write to and the StatusChangedAt this statement stamped, which
	// is the instant a reminder is scheduled off and is read from this Store's
	// clock rather than from anything the caller holds.
	Invite(ctx context.Context, tx database.Tx, scope tenancy.Scope, listID, signupID string) (*Signup, error)

	// Convert moves an invited signup to converted and stamps the moment, in the
	// caller's transaction — which is the ordinary one, since what somebody
	// converted into is a row of the caller's written in the same transaction —
	// and answers with the signup it moved.
	//
	// It refuses anything that is not invited with ErrWrongStatus, guarded the
	// same way Invite is, and hands back the row for the same reason.
	Convert(ctx context.Context, tx database.Tx, scope tenancy.Scope, listID, signupID string) (*Signup, error)

	// Withdraw takes somebody off the list at their own request, in the caller's
	// transaction, and erases what the row said about them.
	//
	// It blanks the contact, the notes and the subject reference and keeps the
	// contact digest, which is what lets a later signup from the same address be
	// refused with ErrContactWithdrawn instead of quietly re-subscribing
	// somebody who asked to be left alone. It moves a signup in any status; a
	// second call reports ErrAlreadyWithdrawn rather than restamping the moment
	// they left.
	//
	// The signup it answers with is the row **as it stood before the blanking**,
	// read on the caller's transaction immediately before the statement. That is
	// deliberate and is the reason this write returns anything at all: a
	// post-blank row is a status and a digest, so a consumer recording who came
	// off which list would be recording nothing. A caller wanting to see what is
	// left instead reads the signup afterwards, which GetSignup still reaches.
	//
	// Nothing is decided on that read. The guarded update's own count still
	// refuses a row that has already been withdrawn or has moved out from under
	// this caller, so the signup comes back only beside a nil error.
	Withdraw(ctx context.Context, tx database.Tx, scope tenancy.Scope, listID, signupID string) (*Signup, error)

	// WithdrawSignupsForSubject withdraws every signup one principal holds in
	// the scope — archived signups included — and reports how many that was.
	//
	// It is the erasure path, and it is a withdrawal rather than a delete for
	// the reason Withdraw is: a delete frees the unique key, so somebody erased
	// at their own request could be re-subscribed by the next form submission,
	// which is the opposite of an erasure. Each row is left as Withdraw leaves
	// one — contact, notes and subject blank, the digest kept, the status
	// withdrawn — so the suppression outlives the erasure on every list the
	// person was on. waitlists/privacy is the dataprivacy.Eraser built on this.
	//
	// It reaches archived signups where Withdraw does not, because an archived
	// signup still holds the address it was made with and an erasure that left
	// it would be reporting completion over a row still naming somebody.
	//
	// The anonymous subject is refused rather than answered — bound to nobody,
	// the statement would withdraw every signup nobody claimed. Zero is not an
	// error: a person who never joined a list is a person with nothing here to
	// erase.
	WithdrawSignupsForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject Subject) (int64, error)

	// ArchiveSignup retires a signup administratively, in the caller's
	// transaction, and answers with the row it hid.
	//
	// It is not a withdrawal and must not be used as one: it hides the row from
	// every read that does not ask for archived rows and changes nothing about
	// what the row holds, so the contact is still stored and nothing suppresses
	// a re-signup — the uniqueness covers archived rows, so what the next
	// attempt gets is ErrAlreadySignedUp. Somebody asking to come off a list
	// wants Withdraw.
	//
	// The row it hands back is read through a statement of its own, because
	// GetSignup is written not to see it, as every single-row read here is. What
	// is left afterwards is a page: ListSignups and ListSignupsForSubject reach
	// an archived signup with the filter's IncludeArchived set, and the second of
	// those reaches nothing for a signup naming no subject, which is what a
	// public form produces.
	ArchiveSignup(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		listID, signupID string,
	) (*Signup, error)
}
