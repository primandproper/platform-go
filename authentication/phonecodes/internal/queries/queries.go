package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// CodesTable is the texted-code table at its canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
const CodesTable = "phone_codes"

// TableNames is every table this package owns, which is one.
var TableNames = []string{CodesTable}

// The columns the statements below name, and the store binds by. Exported
// because both halves spell them, and a column spelled twice is a column that
// can be spelled differently.
const (
	// ScopeColumn is whose directory the code was issued in. Every statement
	// here but the sweep filters on it.
	ScopeColumn = "scope"
	// SubjectIDColumn is the consumer's own identifier for the person. It
	// carries no REFERENCES — see the migrations package.
	SubjectIDColumn = "subject_id"
	// PhoneNumberColumn is the E.164 number the code was texted to, and with the
	// scope the row's natural key.
	PhoneNumberColumn = "phone_number"
	// CodeHashColumn is the digest of the code. It is bound by the issue and
	// compared against by the spend, and projected by nothing — see
	// [RecordColumns].
	CodeHashColumn = "code_hash"
	// AttemptsColumn is how many wrong codes have been presented against this
	// one.
	AttemptsColumn = "attempts"
	// MaxAttemptsColumn is the count at which the code is dead.
	MaxAttemptsColumn = "max_attempts"
	// IssuedAtColumn is when the code was issued.
	IssuedAtColumn = "issued_at"
	// ExpiresAtColumn is the deadline the code stops being redeemable at.
	ExpiresAtColumn = "expires_at"
	// PurgeAfterColumn is when the row may be deleted, past expires_at by the
	// store's retention window. The sweep is keyed on it.
	PurgeAfterColumn = "purge_after"
	// RedeemedAtColumn is when the code was spent, and NULL until it is.
	RedeemedAtColumn = "redeemed_at"
	// RevokedAtColumn is when the code was withdrawn with the rest of its
	// subject's, and NULL until it is.
	RevokedAtColumn = "revoked_at"
)

// The arguments named apart from the column they bind against.
const (
	// NowArg is the instant the two guarded writes' liveness guard compares
	// against. It is bound from the store's clock rather than read off the
	// server's, because the store's clock stamped expires_at.
	NowArg = "now"

	// ExpectedAttemptsArg is the compare half of the attempt count's
	// compare-and-set, and the spend's guard that nobody counted a wrong code
	// since the row was read. It is not attempts because the counting write
	// assigns that column, and one argument name would set the column to the
	// value it was requiring it to already hold — legal SQL that guards nothing.
	ExpectedAttemptsArg = "expected_attempts"

	// PurgeBeforeArg is the horizon the sweep binds.
	PurgeBeforeArg = "purge_before"
)

// Columns is the whole row, in the order the DDL declares it.
var Columns = []string{
	querygen.IDColumn,
	ScopeColumn,
	SubjectIDColumn,
	PhoneNumberColumn,
	CodeHashColumn,
	AttemptsColumn,
	MaxAttemptsColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
}

// RecordColumns is what every read projects: the whole row less the digest.
//
// The digest's absence is the point. Nothing in this package compares it in Go
// — the spend compares it in its own predicate — so a projection that carried
// it would only put a stored credential's digest in whatever a caller did next
// with the row.
var RecordColumns = without(Columns, CodeHashColumn)

// IssueColumns is what an issue writes: every column. The two stamps are among
// them although an issue always binds them NULL, because the write is an
// upsert whose conflict branch assigns every column the insert supplies — and
// binding them NULL is what clears a spent or withdrawn row the new code
// replaces.
var IssueColumns = Columns

// nullable is the two stamps, NULL on every code nobody has spent or withdrawn.
var nullable = []string{RedeemedAtColumn, RevokedAtColumn}

// The query names the generated querier's methods are built from.
const (
	IssueCodeQuery        = "IssuePhoneCode"
	GetCodeQuery          = "GetPhoneCode"
	SpendCodeQuery        = "SpendPhoneCode"
	CountAttemptQuery     = "CountPhoneCodeAttempt"
	RevokeForSubjectQuery = "RevokePhoneCodesForSubject"
	ListForSubjectQuery   = "ListPhoneCodesForSubject"
	DeleteForSubjectQuery = "DeletePhoneCodesForSubject"
	SweepCodesQuery       = "SweepPhoneCodes"
)

// without is columns less the ones named, order preserved.
//
// querygen derives a statement's id predicate from the column list it is
// handed, so dropping the id is how a statement says it keys on something else.
func without(columns []string, dropped ...string) []string {
	kept := make([]string, 0, len(columns))

	for _, column := range columns {
		if !slices.Contains(dropped, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

// Render returns the canonical sqlc input for d: the eight statements this
// store executes, in one file's worth of text.
//
// Every one is a querygen shape. The attempt count is the statement that looked
// as though it would need an authored `attempts = attempts + 1`, and it does
// not: the store has read the row before it counts, so the count is a
// compare-and-set of the value read — the same Match.Arg shape a grant's
// refresh uses — and a lost race is a zero the store reads rather than an
// expression the generator would have to learn.
//
// # A note on timestamps
//
// Every instant this corpus binds is a UTC time.Time. SQLite has no date type,
// so the liveness guard and the sweep are string comparisons there, made
// chronological by both sides being the same fixed-width UTC rendering. That
// rendering is whole seconds, so a code on SQLite dies up to a second early
// rather than living a second late — the direction that fails closed.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		issue(g),
		read(g),
		spend(g),
		countAttempt(g),
		revokeForSubject(g),
		listForSubject(g),
		deleteForSubject(g),
		sweep(g),
	})
}

// issue is an upsert onto (scope, phone_number), the unique index's key.
//
// It is what makes "one live code per number" structural rather than a read and
// a revoke a concurrent issue could interleave with: the second of two issues
// for one number waits on the first's row and then replaces it, id included, so
// only the later code works. The conflict branch resets attempts, both stamps
// and the deadlines, because every inserted column is also an assigned one.
func issue(g *querygen.Generator) *querygen.Query {
	return g.UpsertQuery(IssueCodeQuery, CodesTable, Columns, IssueColumns, IssueColumns, nullable,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: PhoneNumberColumn},
	)
}

// read is the row one number holds, whatever state it is in.
//
// It is what an issue reads first, to hand the caller the code it is about to
// replace, and what a redemption reads first, for the id the digest is bound to
// and the attempt count the writes compare against. It carries no liveness
// predicate, because a redemption refused for a spent or expired row still
// wants to say which on its span.
func read(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetCodeQuery, CodesTable, without(Columns, querygen.IDColumn),
		querygen.Read{Projection: RecordColumns},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: PhoneNumberColumn},
	)
}

// spend is the write that redeems a code, and every row-state test the answer
// depends on is in its predicate: the row the redemption read (by id, so a
// replacement issued since matches nothing), the digest, neither stamp, the
// deadline, and the attempt count it read — so a wrong code counted by a
// concurrent request since, including the one that exhausted the code, makes
// this match nothing.
//
// The count is the answer, which is why it is :execrows.
func spend(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(SpendCodeQuery, CodesTable, Columns, []string{RedeemedAtColumn}, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: CodeHashColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
		querygen.Match{Column: AttemptsColumn, Arg: ExpectedAttemptsArg},
	)
}

// countAttempt counts one wrong code: a compare-and-set of the attempt count
// the redemption read, guarded by the same liveness tests as the spend.
//
// Two wrong codes racing both read the same count; the first moves it and the
// second matches nothing. The second is refused all the same, uncounted — and
// that loses nothing, because exactly one guess took effect per value the count
// passed through, so no more guesses are ever evaluated than the limit allows.
func countAttempt(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(CountAttemptQuery, CodesTable, Columns, []string{AttemptsColumn}, nil,
		querygen.Match{Column: ScopeColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
		querygen.Match{Column: AttemptsColumn, Arg: ExpectedAttemptsArg},
	)
}

// revokeForSubject withdraws every code one person holds that has not been
// spent or withdrawn already.
func revokeForSubject(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeForSubjectQuery, CodesTable, without(Columns, querygen.IDColumn),
		[]string{RevokedAtColumn}, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
		unredeemed(),
		unrevoked(),
	)
}

// listForSubject is every row one person holds in a scope, in any state, which
// is what authentication/phonecodes/privacy's collector is built on.
//
// It is unpaged, and the bound is structural: a row per number, and a person
// has as many numbers as somebody typed in for them.
func listForSubject(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery(ListForSubjectQuery, CodesTable, RecordColumns, nil,
		[]querygen.Order{{Column: IssuedAtColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
	)
}

// deleteForSubject is the erasure: every row one person holds in a scope.
func deleteForSubject(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteForSubjectQuery, CodesTable, without(Columns, querygen.IDColumn),
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
	)
}

// sweep removes every row past its purge deadline, across every scope.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepCodesQuery, CodesTable, without(Columns, querygen.IDColumn),
		querygen.Match{Column: PurgeAfterColumn, Against: querygen.AtMostArgument, Arg: PurgeBeforeArg},
	)
}

// unredeemed is the guard that makes a single-use code single-use.
func unredeemed() querygen.Match {
	return querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue}
}

// unrevoked is the guard that keeps a withdrawn code from being spent, and
// makes the withdrawal idempotent.
func unrevoked() querygen.Match {
	return querygen.Match{Column: RevokedAtColumn, Against: querygen.NoValue}
}

// stillLive is the deadline guard: the row's deadline is strictly after the
// instant the store named.
func stillLive() querygen.Match {
	return querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: NowArg, Exclude: true}
}

// FileName is the file one dialect's rendered queries are committed to.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}
