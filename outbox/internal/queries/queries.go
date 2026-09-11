package queries

import (
	"fmt"
	"slices"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// OutboxTable is the one table this package owns, at its canonical spelling —
// what the emitted .sql names, and what the Writer and the Relay render their
// consumer's prefix onto.
//
// The outbox_ segment is the schema's own rather than the caller's, so a table
// says which package created it even in a database shared between
// applications; the consumer's namespace goes in front of it. See
// outbox/migrations.
const OutboxTable = "outbox_messages"

// TableNames is every table this package owns, which is the one above.
//
// It is a list rather than a bare constant because the querygen registry takes
// one, and because the thing a registry has to survive is a table being added
// without anybody remembering to register it.
var TableNames = []string{OutboxTable}

// The columns of an outbox row, beyond the id every keyed statement here is
// derived from.
//
// Spelled here rather than at the Writer and the Relay because both halves read
// them: the corpus below binds them, and the generated querier's argument
// structs and row types name the fields it derived from them. Two spellings of
// one column is the drift this package exists to prevent.
const (
	// TopicColumn names the destination the Relay resolves a publisher for.
	TopicColumn = "topic"
	// PartitionKeyColumn groups messages that must be published in order
	// relative to one another. The empty string means unordered, which is why
	// the claim's per-key predicate tests for it rather than for NULL: the
	// column is NOT NULL DEFAULT '', so "no key" is a value rather than an
	// absence and the predicate is one comparison instead of two.
	PartitionKeyColumn = "partition_key"
	// PayloadColumn holds the marshaled message, republished verbatim.
	PayloadColumn = "payload"
	// NextAttemptColumn is the instant a message becomes claimable again. A new
	// message carries its creation time here, so it is eligible the moment it
	// commits; a failed one carries the backoff the relay computed.
	NextAttemptColumn = "next_attempt"
	// ClaimedUntilColumn is the lease horizon a relay writes when it takes a
	// message, and the column a later claim compares against to decide whether
	// that lease has lapsed. It is nullable, and an unset lease is the
	// unclaimed state rather than a lease that expired at the zero time.
	ClaimedUntilColumn = "claimed_until"
	// PublishedAtColumn is stamped when the broker accepted the message. Rows
	// are marked rather than deleted so a duplicate or a gap can be
	// investigated afterwards, and the reap removes them once they age out.
	PublishedAtColumn = "published_at"
	// AttemptsColumn counts claims rather than failures — the claim increments
	// it, so a relay that dies mid-publish has still consumed one.
	AttemptsColumn = "attempts"
	// LastErrorColumn holds the truncated reason the last publish failed, and
	// is cleared when one succeeds.
	LastErrorColumn = "last_error"
	// QuarantinedColumn marks a message no future claim will admit. It is the
	// terminal state a message reaches once it has exhausted its attempts,
	// without which one permanently broken message holds the head of its
	// partition forever.
	QuarantinedColumn = "quarantined"
)

// The arguments the authored statements bind. A rendered statement takes its
// argument names from its columns; these are the names that have nowhere else
// to come from, so they are written down once rather than at each interpolation
// that spells one.
const (
	// NowArg is the instant a claim compares a message's next attempt against:
	// a message is due when it has reached it.
	NowArg = "now"
	// LeaseExpiredByArg is the same instant, compared against a lease that may
	// not be there. It is a second name for one moment because claimed_until is
	// nullable and next_attempt is not, and no analyzer here gives one argument
	// two nullabilities — see selectClaimable. The Relay binds both from one
	// clock read, which is what keeps them the same moment in fact.
	LeaseExpiredByArg = "lease_expired_by"
	// CreatedAtArg is the creation instant the insert binds. See createInsert
	// on why this one column is not the database's to stamp.
	CreatedAtArg = querygen.CreatedAtColumn
	// BeforeArg is the retention horizon the reap deletes behind. It is named
	// for the instant the pass is asking about rather than for published_at,
	// which is a row's own stamp — the two are not the same value.
	BeforeArg = "before"
	// IDsArg is the set of message ids a claim leases, reads back, and retires.
	IDsArg = querygen.IDsArg
)

// Columns is every column, in the order the DDL declares them and every
// statement here names them.
var Columns = []string{
	querygen.IDColumn,
	TopicColumn,
	PartitionKeyColumn,
	PayloadColumn,
	querygen.CreatedAtColumn,
	NextAttemptColumn,
	ClaimedUntilColumn,
	PublishedAtColumn,
	AttemptsColumn,
	LastErrorColumn,
	QuarantinedColumn,
}

// InsertColumns is what one enqueued row supplies values for, in the order the
// INSERT lists them.
//
// created_at is among them, which is where this table parts company with the
// module's convention, and querygen.ForInsert would strip it — which is why the
// insert below is written out rather than rendered. See createInsert.
var InsertColumns = []string{
	querygen.IDColumn,
	TopicColumn,
	PartitionKeyColumn,
	PayloadColumn,
	querygen.CreatedAtColumn,
	NextAttemptColumn,
}

// FailureColumns is what a failed publish assigns: the lease released, the
// retry scheduled, the reason recorded, and the terminal flag.
//
// It is the table's mutable set less published_at and attempts, and both
// absences are load-bearing. A failure that assigned published_at would retire
// the row it just rescheduled, and one that assigned attempts would hand the
// counter back to a relay that has already consumed a claim — the increment
// belongs to the claim, so that a relay dying mid-publish still costs an
// attempt.
var FailureColumns = []string{
	ClaimedUntilColumn,
	NextAttemptColumn,
	LastErrorColumn,
	QuarantinedColumn,
}

// ClaimedColumns is what the read of a leased batch projects, which is a
// message to publish and not a message row.
//
// The four state columns are left out because the Relay is holding the rows
// rather than asking about them: it wrote the lease, it computes the next
// attempt from the count, and published_at is what it is about to write. The
// attempt count is here because the failure path compares it against the
// quarantine threshold.
var ClaimedColumns = []string{
	querygen.IDColumn,
	TopicColumn,
	PartitionKeyColumn,
	PayloadColumn,
	AttemptsColumn,
}

// Nullable names the columns a write may set to NULL, which lives in the schema
// neither this package nor querygen reads. A NOT NULL column bound through
// sqlc.narg yields a parameter that can express a NULL the server will reject,
// and a nullable one bound through sqlc.arg yields one that cannot express the
// NULL the column takes; both are quiet.
var Nullable = []string{ClaimedUntilColumn, PublishedAtColumn, LastErrorColumn}

// Render returns the canonical sqlc input for one dialect: every statement the
// Writer and the Relay execute, in the order below, as the bytes the committed
// .sql beside this file holds.
//
// The order is the order a message goes through: enqueued, selected, leased,
// read back, retired or rescheduled, counted in the backlog, and finally
// collected once it has aged past retention.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The table registers itself here rather than through StandardCRUD, which
	// is what registers a conventional table and which this one gets none of. A
	// consumer reading the registry back to truncate a database between
	// integration tests has to find this table whether or not the statements
	// over it happen to come from the standard set.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		createInsert(),
		selectClaimable(g, false),
		selectClaimable(g, true),
		claimMessages(g),
		fetchClaimed(g),
		markPublished(g),
		recordFailure(g),
		backlog(),
		reapPublished(g),
	})
}

// createInsert renders the enqueue of one message, immediately eligible: its
// first next_attempt is its creation time.
//
// It is written out for the one column querygen will not let a caller supply.
// created_at here is not "when the row landed" — it is the instant the
// transaction that emitted the event chose, and three separate things depend on
// every row of one Enqueue carrying that same instant. The claim orders on
// (created_at, id) and its per-key predicate compares that tuple, the backlog
// probe reports the oldest of them as the queue's age, and next_attempt is that
// same value, so a message is claimable the moment its transaction commits. A
// database default would stamp each row separately, and the age a dashboard
// reads would become the age of the write rather than of the event.
//
// So the instant is bound once and used twice, under one argument name: the two
// columns are the same moment, and two arguments would be two ways to disagree
// about it.
//
// One row per statement, executed once per message inside the transaction the
// caller already has open. A multi-row VALUES list makes the statement's text a
// function of how many messages are in the call, which is the dynamic SQL this
// corpus exists to retire; the dialects that could bind a batch as arrays are
// not all three of them.
func createInsert() *querygen.Query {
	values := make([]string, 0, len(InsertColumns))
	for _, column := range InsertColumns {
		// next_attempt takes the creation instant rather than an argument of
		// its own, which is what makes a new message due immediately.
		if column == NextAttemptColumn {
			column = CreatedAtArg
		}

		values = append(values, binding(column))
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "InsertOutboxMessage", Type: querygen.ExecType},
		Content: fmt.Sprintf("INSERT INTO %s (\n\t%s\n) VALUES (\n\t%s\n);",
			OutboxTable,
			strings.Join(InsertColumns, ",\n\t"),
			strings.Join(values, ",\n\t"),
		),
	}
}

// selectClaimable renders the statement that picks the next batch of message
// ids to lease, in the two forms the claim modes are.
//
// It is written out because it reads the outbox table through itself, and a
// correlated NOT EXISTS over a self-join is a shape querygen does not render
// and should not learn to.
//
// The ordering guarantee lives in this predicate. A row with a partition key is
// claimable only when no earlier unpublished row shares that key, so at most one
// row per key is ever in flight across every relay in the fleet — keyed messages
// are strictly ordered even under concurrent skip-locked relays. Unkeyed rows
// skip the check entirely and claim freely.
//
// "Earlier" is (created_at, id), not created_at alone, and the tuple is what
// makes the guarantee hold. One Enqueue stamps every row with a single instant,
// so two messages sharing a key and an Enqueue also share a created_at; under a
// bare `<` neither would block the other, both would be claimable at once, and a
// failure on the first would publish the second ahead of it. The tiebreak is id
// because that is what the ORDER BY breaks ties on — the predicate and the
// publish order have to agree on "earlier" or a batch can contain a pair it is
// about to reorder.
//
// The claim's two time comparisons are one instant and two arguments, and that
// is the analyzers' doing rather than a distinction anybody wants. next_attempt
// is NOT NULL and claimed_until is not, so a single argument compared against
// both is one every engine here types twice — once as an instant and once as an
// optional one — and unison refuses the divergence rather than picking. So the
// lease horizon binds under its own name, and the Relay passes the same moment
// to both; see the arguments' declarations.
//
// # Why there are two of them
//
// The lock clause is statement text rather than a bound value, so a relay
// configured for one mode or the other picks between two generated methods the
// way a paged read picks between its two directions. Rendering one statement
// and appending the clause at run time would put the outbox back to composing
// SQL in Go, over the one predicate whose exactness a fleet's disjointness
// depends on.
//
// The locked form carries FOR UPDATE SKIP LOCKED only where the dialect has it,
// which on SQLite is nowhere: one writer at a time is that engine's whole
// storage model, so there is nothing to skip. Both statements are rendered on
// all three dialects regardless, so that the roster of names does not vary by
// dialect — RelayConfig narrows a skip-locked relay to the lease mode before it
// can reach the statement there, which is where the unreachability is decided
// rather than here.
func selectClaimable(g *querygen.Generator, skipLocked bool) *querygen.Query {
	const (
		claimed = "m"
		earlier = "prior"
	)

	statement := fmt.Sprintf(`SELECT %[1]s.%[3]s
FROM %[2]s AS %[1]s
WHERE %[1]s.%[4]s IS NULL
	AND %[1]s.%[5]s = FALSE
	AND %[1]s.%[6]s <= sqlc.arg(%[7]s)
	AND (%[1]s.%[8]s IS NULL OR %[1]s.%[8]s <= sqlc.arg(%[9]s))
	AND (%[1]s.%[10]s = '' OR NOT EXISTS (
		SELECT 1
		FROM %[2]s AS %[11]s
		WHERE %[11]s.%[10]s = %[1]s.%[10]s
			AND %[11]s.%[4]s IS NULL
			AND %[11]s.%[5]s = FALSE
			AND (%[11]s.%[12]s < %[1]s.%[12]s
				OR (%[11]s.%[12]s = %[1]s.%[12]s AND %[11]s.%[3]s < %[1]s.%[3]s))
	))
ORDER BY %[1]s.%[12]s, %[1]s.%[3]s
%[13]s`,
		claimed,
		OutboxTable,
		querygen.IDColumn,
		PublishedAtColumn,
		QuarantinedColumn,
		NextAttemptColumn,
		NowArg,
		ClaimedUntilColumn,
		LeaseExpiredByArg,
		PartitionKeyColumn,
		earlier,
		querygen.CreatedAtColumn,
		g.LimitClause(),
	)

	name := "SelectClaimableOutboxMessages"

	if skipLocked {
		name = SkipLockedName(name)

		if g.Dialect().SupportsSkipLocked() {
			statement += "\nFOR UPDATE SKIP LOCKED"
		}
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ManyType},
		Content:    statement + ";",
	}
}

// SkipLockedName is the locked form's name, derived from the unlocked one.
//
// It is derived rather than written twice for querygen.DescendingName's reason:
// the two statements are one decision, and the Relay picking between them by
// mode has to spell the same pair of names this file emitted.
func SkipLockedName(name string) string {
	return name + "SkipLocked"
}

// claimMessages leases the selected rows.
//
// It is written out because it assigns an expression: the attempt count is
// incremented here rather than on failure, so that a relay which crashes
// mid-publish has still consumed an attempt and a message that reliably kills
// its relay eventually quarantines instead of being reclaimed forever. querygen
// assigns bound values, and `attempts = attempts + 1` is not one.
//
// The set binds last, as every set predicate in this module does: on the two
// dialects with no array type it expands to one marker per element, and an
// argument bound after the expansion would be numbered into the middle of it.
func claimMessages(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "ClaimOutboxMessages", Type: querygen.ExecType},
		Content: fmt.Sprintf(`UPDATE %s SET
	%s = sqlc.arg(%s),
	%s = %s + 1
WHERE %s;`,
			OutboxTable,
			ClaimedUntilColumn, ClaimedUntilColumn,
			AttemptsColumn, AttemptsColumn,
			g.SetCondition(querygen.IDColumn, IDsArg),
		),
	}
}

// fetchClaimed projects the leased rows, oldest first.
//
// It is written out for its ordering rather than its shape. querygen's batched
// read orders by the column it keyed on, so that a consumer walking the rows
// sees one key's rows together; what this one owes is the publish order, which
// is (created_at, id) — the same tuple the claim predicate calls "earlier", and
// the whole reason a batch cannot contain a pair it is about to reorder.
//
// The projection is still assembled from the column list above rather than
// spelled a second time.
func fetchClaimed(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "FetchClaimedOutboxMessages", Type: querygen.ManyType},
		Content: fmt.Sprintf("SELECT\n\t%s\nFROM %s\nWHERE %s\nORDER BY %s, %s;",
			strings.Join(ClaimedColumns, ",\n\t"),
			OutboxTable,
			g.SetCondition(querygen.IDColumn, IDsArg),
			querygen.CreatedAtColumn,
			querygen.IDColumn,
		),
	}
}

// markPublished retires the rows the broker accepted.
//
// The rows are kept rather than deleted, so a duplicate or a gap can be
// investigated later; the reap removes them once they age past retention.
//
// The two columns cleared are assigned NULL outright rather than bound. There
// is no value a caller could pass that should leave a published message holding
// a lease or carrying the reason an earlier attempt failed, so the statement
// owns both — which is querygen's own argument for a guard, applied to a SET.
func markPublished(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "MarkOutboxMessagesPublished", Type: querygen.ExecType},
		Content: fmt.Sprintf(`UPDATE %s SET
	%s = sqlc.arg(%s),
	%s = NULL,
	%s = NULL
WHERE %s;`,
			OutboxTable,
			PublishedAtColumn, PublishedAtColumn,
			ClaimedUntilColumn,
			LastErrorColumn,
			g.SetCondition(querygen.IDColumn, IDsArg),
		),
	}
}

// recordFailure renders the write applied to a message whose publish failed:
// the lease released, the reason recorded, the retry scheduled, and the
// terminal flag set once the attempts are spent.
//
// It is the one write here querygen renders whole, because it is the one that
// assigns bound values to a row addressed by its id — which is the shape that
// package is nearly all of.
func recordFailure(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery("RecordOutboxMessageFailure", OutboxTable, Columns, FailureColumns, Nullable)
}

// backlog is the health probe: how many messages are waiting, and when the
// oldest of them was created.
//
// Both come back from one round trip because they answer one question — is the
// relay keeping up — and neither is useful alone. A depth of 40,000 is fine if
// the oldest is four seconds old and an incident if it is four hours old. Two
// statements would also be two snapshots, and a depth read against a queue that
// has since drained is a number nobody can act on.
//
// Quarantined rows are excluded: they are never going to be published, so
// counting them would make a permanently broken message look like a permanently
// growing backlog.
//
// It is written out because it is an aggregate, which is the one thing a corpus
// of row statements has no shape for.
//
// The oldest instant is a subquery projecting the column rather than
// MIN(created_at), and the grouping is what makes that safe. No analyzer here
// resolves a Go type for an aggregate over a timestamp — an override could name
// one, but not that it is nullable — where a subquery projecting the column
// itself resolves to the column's own type on all three engines, and that type
// is NOT NULL.
//
// So an empty queue must not be a row. Grouping on the column the predicate has
// already pinned yields exactly one group while any message is waiting and no
// group at all when none is, which is the honest shape: there is no oldest row
// to report, and "no backlog" is an absent row rather than a row of zeroes. The
// Relay reads that absence back as a depth of zero and no age, which is the
// answer it gave before.
func backlog() *querygen.Query {
	const queued = "queued"

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "OutboxBacklog", Type: querygen.OneType},
		Content: fmt.Sprintf(`SELECT
	COUNT(*) AS depth,
	(
		SELECT %[1]s.%[2]s
		FROM %[3]s AS %[1]s
		WHERE %[1]s.%[4]s IS NULL
			AND %[1]s.%[5]s = FALSE
		ORDER BY %[1]s.%[2]s ASC
		LIMIT 1
	) AS oldest
FROM %[3]s
WHERE %[6]s IS NULL
	AND %[7]s = FALSE
GROUP BY %[7]s;`,
			queued,
			querygen.CreatedAtColumn,
			OutboxTable,
			PublishedAtColumn,
			QuarantinedColumn,
			querygen.Qualify(OutboxTable, PublishedAtColumn),
			querygen.Qualify(OutboxTable, QuarantinedColumn),
		),
	}
}

// reapPublished renders the delete that removes published rows past the
// retention window, capped so one pass touches a bounded number of them.
//
// It is querygen's bounded prune rather than an authored delete, and the
// horizon is a bound instant rather than the server's clock. Both halves of
// that follow from where published_at comes from: the Relay stamps it from the
// clock it was constructed with, and computes this cutoff from that same clock,
// so a comparison against CURRENT_TIMESTAMP would be two clocks — years apart
// under a test clock that only moves when a test moves it.
//
// The pass takes the oldest published rows first, so a backlog of retired rows
// drains in the order it accumulated.
func reapPublished(g *querygen.Generator) *querygen.Query {
	return g.PruneQuery("ReapPublishedOutboxMessages", OutboxTable, querygen.Prune{
		Key:   []string{querygen.IDColumn},
		Order: []querygen.Order{{Column: PublishedAtColumn}},
	},
		querygen.Match{Column: PublishedAtColumn, Against: querygen.NoValue, Exclude: true},
		querygen.Match{Column: PublishedAtColumn, Arg: BeforeArg, Against: querygen.AtMostArgument},
	)
}

// binding renders a column's argument reference, nullable where the table says
// the column is. It is querygen's own rule, restated for the one insert here
// that is written out rather than rendered.
func binding(column string) string {
	if slices.Contains(Nullable, column) {
		return fmt.Sprintf("sqlc.narg(%s)", column)
	}

	return fmt.Sprintf("sqlc.arg(%s)", column)
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
