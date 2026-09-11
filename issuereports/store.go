package issuereports

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the persistence seam for issue reports.
//
// This package ships a SQL implementation ([NewSQLStore]) together with the DDL
// it needs (issuereports/migrations), so adopting it does not mean writing this.
// The interface exists because a report queue and its storage are genuinely
// separable, and an application with its own schema conventions should not have
// to fork the package to keep them.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. There is no form of any write that opens a
// transaction of its own, and that absence is the point: a report is rarely the
// only row a consumer writes. An audit entry naming who filed or decided it and
// a data change event on an outbox somebody fans out are the ordinary
// companions, and a companion written after the report's own write has committed
// is a companion that can go missing while the report stays. A signature that
// cannot express that is better than a doc that warns against it.
//
// [Store.TransitionReport] is the write that makes the case, because a decision
// on a report is an event before it is a row: who moved it, when, and why. The
// move and the entry recording it belong in one transaction or the entry can be
// refused after the status has already changed, leaving a decision nobody can
// attribute.
//
// The read takes the wider type so that one method serves both moments. A
// console paging the triage queue holds no transaction and passes
// Client.Reader(); a consumer that has just filed a report passes the Tx it
// wrote through, and sees it. A read narrowed to Tx would have forced the first
// caller into a transaction it has no use for, and one narrowed to
// Client.Reader() would have read a database that does not yet hold the row its
// caller just wrote.
//
// A caller with genuinely nothing to join opens one with Client.WithTransaction
// and passes the Tx it is handed. A Store that is not a SQL store still takes
// these types; an implementation with no transaction of its own ignores the
// executor, and the seam stays one signature rather than one per backing.
//
// # Every write that names one report answers with it
//
// [Store.CreateReport], [Store.UpdateReport], [Store.TransitionReport] and
// [Store.ArchiveReport] each return the row the statement left, read back on the
// caller's transaction. Returning is the spelling rather than writing the answer
// onto the argument: the two deliver the same guarantee, and one of them is
// available to a write that takes an id rather than an entity, so this interface
// spells it one way. None of the four modifies the value it was handed.
//
// The reading is not "a write returns". It is that the row is what the caller
// acts on next and no read of theirs can reach it as the statement left it. A
// transition assigns a stamp and a note the caller did not supply. A revision
// moves last_updated_at, which is the database's. An archive hides the row from
// every keyed read this interface has, so the entry naming what was removed is
// written from the returned row or from a read taken before the write, describing
// a report that was still in the queue.
//
// [Store.DeleteReportsByReporter] is the one write that does not, and the reason
// is the shape of the answer rather than an exception to the rule: an erasure
// destroys a set rather than moving a row, and what a caller reports is how many
// went. Handing back the rows would be handing back the free text the erasure
// exists to remove.
//
// # The scope is an argument, on every method
//
// Every method takes a tenancy.Scope, and none of them offers a variant that
// omits it — an implementation must filter on it rather than treat it as a hint.
// A deployment with one tenant passes tenancy.Global() everywhere and behaves
// exactly as it would have without the column.
//
// That includes the two writes that take a whole [Report]. They read the scope
// off the argument rather than off Report.Scope, and the alternative — letting
// an entity that carries a scope supply its own, so that the explicit argument
// appears only where there is no entity — was considered and rejected. The
// module's rule is that a scope goes into the query bound as a tenancy.Scope
// rather than derived from some other value, and an entity field is exactly the
// derivation that rule exists to rule out: it makes "which tenant is this write
// for" answerable only by reading a struct the caller assembled somewhere else.
// A Report.Scope that disagrees with the argument is [ErrScopeMismatch] rather
// than either value quietly winning; an unset one adopts the argument.
//
// There is deliberately no cross-scope listing, and it is worth being clear
// about what that costs. An operator triaging every tenant's reports from one
// console is a real thing to want, and this interface will not answer it in one
// call: they list the scopes they administer and page each. The alternative is a
// read that omits the scope, which is the one read that cannot tell an
// operator's caller from a tenant's — and a paged list cannot bind a set of
// scopes either, because a bound set may not sit in a statement that also binds
// a cursor and a page size on two of the three dialects this package serves.
type Store interface {
	// CreateReport files one report through the caller's transaction, so the
	// report commits with whatever the caller writes beside it, and answers with
	// the row it wrote. It assigns the id where the caller left it empty and
	// sets the status to StatusOpen. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// A report is born open, so a value arriving in any other status is
	// ErrInvalidStatusTransition rather than a stored row: a report that started
	// resolved is one nobody resolved.
	//
	// The report handed in is not modified. Everything the write settled — the
	// id it minted, the scope it bound, the status it assigned, the stamp the
	// database wrote — is on the value returned, so a caller reads them from one
	// place rather than from an argument that changed under them.
	//
	// Both statements run on tx, so the row read back is the one this
	// transaction just wrote rather than a read of a row nothing else can see
	// yet.
	CreateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) (*Report, error)

	// GetReport reads one of the scope's live reports. It returns an error
	// wrapping ErrReportNotFound when the report does not exist, has been
	// archived, or belongs to another scope — which are the same answer from
	// here. A nil q is an error wrapping ErrNilExecutor.
	GetReport(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, reportID string) (*Report, error)

	// ListReports pages the scope's reports, in the direction the filter names.
	ListReports(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Report], error)

	// ListReportsByStatus is ListReports restricted to one status: the triage
	// queue.
	//
	// It is a separate method rather than a field on the filter because the
	// filter is this module's shared page description and a status is this
	// package's own. The count a console wants beside the queue is on the
	// result's pagination: the filtered count is of everything in that status,
	// not of the page.
	//
	// A status this package does not serve is ErrUnknownStatus rather than an
	// empty page, because an empty page is what a queue that has been quietly
	// misspelled looks like.
	ListReportsByStatus(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, status Status, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Report], error)

	// ListReportsByReporter pages the reports one person filed within the scope.
	// It is what a "your reports" view reads, and what the subject access
	// request collector pages through.
	ListReportsByReporter(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, reporter string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Report], error)

	// ListReportsBySubjectType pages every report about one kind of thing —
	// "everything anybody has said about recipes".
	ListReportsBySubjectType(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subjectType string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Report], error)

	// ListReportsForSubject pages every report about one particular thing.
	ListReportsForSubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subjectType, subjectID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Report], error)

	// UpdateReport revises what the reporter said — the kind, the details, and
	// what the report is about — through the caller's transaction, so the
	// revision and whatever the caller records about it commit together or not
	// at all, and answers with the revised row. A revision is an event as much
	// as it is a write: who changed what, and when, and the entry recording it
	// describes the row the statement left rather than the one the caller read
	// before it. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It does not move the status, and it cannot: the lifecycle has one door and
	// it is TransitionReport, which names the status it believed the row was in.
	// A whole-row write that also assigned the status would be a revision that
	// silently reopened a report somebody had just resolved. The row returned is
	// what makes that visible rather than assumed — it carries the status the
	// table holds, not the one the argument named.
	//
	// The report handed in is not modified, and last_updated_at is the
	// database's, so a response assembled from the argument would say the row
	// was last touched at the epoch.
	//
	// A report that is not in the scope — absent, archived, or somebody else's —
	// is an error wrapping ErrReportNotFound.
	UpdateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) (*Report, error)

	// TransitionReport moves a report from one status to another through the
	// caller's transaction and returns it as stored, so the move commits with
	// the entry naming who made it and why. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// from is the status the caller believes the report is in, and it is a
	// parameter rather than something this method reads for itself because that
	// is what makes the move safe under concurrency: the statement requires the
	// row to still hold it. Two triagers resolving the same report means one of
	// them gets ErrStatusConflict rather than both being told they won and the
	// second note overwriting the first.
	//
	// resolution is why — the note a triager leaves. A move into a terminal
	// status stamps ClosedAt and stores the note; a move out of one clears both,
	// because a reason that no longer holds is worse than none.
	//
	// A move the lifecycle does not admit is ErrInvalidStatusTransition and
	// nothing is written. See [Status.CanTransitionTo].
	//
	// The guard and the two reads around it — the one that separates a moved
	// report from an absent one when the guard matches nothing, and the read-back
	// of the row on a hit — all run on tx, so they see what the caller has
	// written and not yet committed. A report filed earlier in the same
	// transaction can be moved by this call, and the report handed back is the
	// row as this transaction has it, which is what a caller recording the
	// outcome beside it wants to be describing.
	//
	// It returns the transitioned report rather than only an error because the
	// row is what the caller acts on next: the entry it writes beside the move
	// describes the stamp and the note this call assigned.
	TransitionReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, reportID string, from, to Status, resolution string) (*Report, error)

	// ArchiveReport removes a report from the queue through the caller's
	// transaction, leaving the row for whoever asks later what was reported, and
	// answers with the row it hid. It is the write a moderation action reaches
	// for: the report leaves the queue and the entry naming who removed it land
	// together, or neither does. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It is not "closed": closing is a status, and an archived report is one
	// that should stop being listed at all. A report already archived is an
	// error wrapping ErrReportNotFound, because an archived report is not in the
	// queue and this method addresses the queue.
	//
	// It returns the row because this is the write whose result no ordinary read
	// here can reach. GetReport cannot see an archived report at all — that is
	// what archiving means — and the five lists reach it only for a caller who
	// set QueryFilter.IncludeArchived and then pages for it, which is a
	// different question from "what did I just remove". The details somebody
	// wrote are what a moderator's entry has to name, and the alternative is the
	// read a consumer takes a statement earlier, describing the row as it stood
	// before the write rather than as the write left it.
	//
	// The read-back runs on tx and only after the guard has matched, so a write
	// that moved nothing is ErrReportNotFound rather than somebody else's
	// archive reported as this one's.
	ArchiveReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, reportID string) (*Report, error)

	// DeleteReportsByReporter destroys every report one person filed within the
	// scope, archived ones included, and reports how many that was.
	//
	// It is a hard delete and it is the erasure path: the details are free text
	// somebody wrote, so what a right-to-be-forgotten request has to remove is
	// the text rather than a flag beside it. issuereports/privacy is the
	// dataprivacy.Eraser built on this; nothing else in this package deletes a
	// row.
	//
	// It runs inside the caller's transaction and must use the executor it is
	// given, so that a subject's reports and the rest of their footprint commit
	// or roll back together.
	//
	// That is also why it is the one method here that issuereports/grpc does not
	// serve. The other ten are on the wire; this is erasure machinery, and the
	// clause above is the whole of the reason — an RPC moves the delete into a
	// transaction of its own, at a moment the caller does not choose, so an
	// erasure run that failed halfway leaves a subject who has been told they
	// were forgotten and half was. It is reached through issuereports/privacy's
	// dataprivacy.Eraser, from the consumer's own erasure run, and
	// issuereports/grpc/doc.go carries the ruling.
	//
	// Zero is not an error: a person who never filed a report is a person with
	// nothing here to erase.
	DeleteReportsByReporter(ctx context.Context, tx database.Tx, scope tenancy.Scope, reporter string) (int64, error)
}
