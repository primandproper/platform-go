package recoverycodes

import (
	"context"
	"encoding/base32"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/internal/recoverycodedb"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// serviceName names the loggers and spans this store emits.
const serviceName = "signin_recovery_codes"

// The keys this store records against a span and a log line.
//
// None of them is a code, and none of them can be turned into one — the digest
// is not among them either, because sixty bits behind a fast hash is a digest
// somebody can reverse, and a trace is a place operators paste into tickets.
const (
	userIDKey = "identity.user_id"
	scopeKey  = "identity.scope"

	// deletedKey is how many rows a replacement or an erasure removed. On the
	// span only: it is a fact about one request rather than a metric.
	deletedKey = "signin.recovery_codes_deleted"
)

// CodeLength is how many characters a recovery code carries, separators aside.
//
// Twelve characters of base32 is sixty bits. That is the length where both of
// the things a code has to survive are comfortably survived: guessing one
// through a sign-in door that counts every miss as a failed sign-in is hopeless,
// and reversing a leaked digest — which is fast on purpose, see the migrations
// package — costs a determined attacker far more than the TOTP secret sitting in
// the same database would.
//
// It is a constant rather than an option, because the number is the whole of a
// code's strength, and a knob here would be a knob whose only use is making it
// weaker.
const CodeLength = 12

// groupLength is how many characters Replace prints between separators, and
// separator is what it prints between them. Neither is part of the code: a code
// typed with or without them is the same code. See normalize.
const (
	groupLength = 4
	separator   = "-"
)

// rawCodeBytes is how much randomness is drawn for one code: the fewest whole
// bytes whose base32 rendering is at least CodeLength characters long. The
// rendering is cut to CodeLength, so every character of a code is five fresh
// bits and the few left over are discarded rather than biased.
const rawCodeBytes = (CodeLength*5 + 7) / 8

// codeEncoding renders a code: RFC 4648 base32, upper case, no padding. The
// alphabet is A to Z and 2 to 7, so a printed code never holds a 0, a 1 or an 8
// — which is what lets normalize read those three as the O, I and B a person
// squinting at paper meant, without ever turning a real character into another.
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// DefaultTablePrefix is the namespace the recovery code table carries when none
// is configured, which is none — rendering plain "signin_recovery_codes".
//
// The signin_recovery_codes segment is the schema's, not the caller's: a table
// always says which package created it. Setting a namespace of "ddb" renders
// ddb_signin_recovery_codes, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ Store = (*SQLStore)(nil)

// SQLStore keeps recovery codes in a SQL table, against the schema
// recoverycodes/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam. It does one
// thing more than that interface describes — Digest renders the column a stored
// code is found by — and it does not belong on it: a store that is not a table
// has no column.
type SQLStore struct {
	q         recoverycodedb.Querier
	clock     clock.Clock
	generator random.Generator
	hasher    hashing.Hasher
	o11y      observability.Observer
}

// NewSQLStore builds a SQLStore over a database client.
//
// The client is not what anything executes on. Every write takes the caller's
// database.Tx and every read the caller's executor; what this one supplies is
// the dialect the generated statements are rendered for, read once here. This
// store has no machinery of its own — no sweep, no worker — so it holds no
// handle to run one on.
//
// It does not create the table. Hand migrations.SQL to your own migration run.
func NewSQLStore(cfg *Config, db database.Client, opts ...Option) (*SQLStore, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if db == nil {
		return nil, ErrNilDatabaseClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "recovery code store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &SQLStore{
		clock:     o.clock,
		generator: o.generator,
		hasher:    o.hasher,
		o11y:      observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical spelling
	// is internal/queries' and the separator is database/ddl's, so a namespaced
	// deployment cannot end up with two renderings of one name.
	if s.q, err = recoverycodedb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the recovery code querier")
	}

	return s, nil
}

// querierDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for.
func querierDialect(d dialect.Dialect) (recoverycodedb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return recoverycodedb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return recoverycodedb.DialectMySQL, nil
	case dialect.SQLite:
		return recoverycodedb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated recovery code queries for dialect %q", d)
	}
}

// Digest renders what the hash column holds for a code, as a person might type
// it — separators and case are normalized away first.
//
// It is exported for the one caller the seam cannot serve: a test asserting that
// the raw value is nowhere in the row. It is not a verification — comparing its
// output to a column by hand is how the single-use guarantee gets reimplemented
// badly.
func (s *SQLStore) Digest(code string) string {
	return hashing.HexString(s.hasher, normalize(code))
}

// Replace mints a fresh set of codes for a user, deletes whatever set they held,
// and returns the new codes exactly once, printed in groups a person can copy.
//
// The delete and the inserts are one write in tx. A replacement that rolls back
// leaves the old set working; one that commits leaves nothing of it, spent or
// unspent. Show the codes after the commit, not from inside the callback.
//
// A code repeated inside the set — which randomness that is still random will
// not produce — fails the insert on the primary key rather than being dropped,
// so a person is never shown a code that was not stored.
func (s *SQLStore) Replace(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	count int,
) ([]string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return nil, err
	}

	if count <= 0 {
		return nil, ErrNonPositiveCount
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	deleted, err := s.q.DeleteRecoveryCodesForUser(ctx, tx, recoverycodedb.DeleteRecoveryCodesForUserParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return nil, op.Error(err, "withdrawing a user's recovery codes")
	}

	op.SpanOnly(deletedKey, deleted)

	now := s.clock.Now().UTC()
	codes := make([]string, 0, count)

	for range count {
		code, genErr := s.mint(ctx)
		if genErr != nil {
			return nil, op.Error(genErr, "generating a recovery code")
		}

		if err = s.q.InsertRecoveryCode(ctx, tx, recoverycodedb.InsertRecoveryCodeParams{
			Scope:    scope,
			UserID:   userID,
			Hash:     s.Digest(code),
			IssuedAt: now,
		}); err != nil {
			return nil, op.Error(err, "storing a recovery code")
		}

		codes = append(codes, code)
	}

	return codes, nil
}

// Verify reports whether a user holds a code, still unspent, and changes
// nothing. Anything else is signin.ErrInvalidCredentials.
//
// It runs on whatever executor it is handed, and signin hands it the reader.
// That is safe in the one direction that matters: a replica that has not yet
// seen a spend answers "unspent", and the spend that follows on the primary is
// what refuses it.
func (s *SQLStore) Verify(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, code string,
) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateLookup(scope, userID, code); err != nil {
		return err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	if !isCodeShaped(code) {
		return signin.ErrInvalidCredentials
	}

	row, err := s.q.RecoveryCodeUnspent(ctx, q, recoverycodedb.RecoveryCodeUnspentParams{
		Scope:  scope,
		UserID: userID,
		Hash:   s.Digest(code),
	})
	if err != nil {
		return op.Error(err, "checking a recovery code")
	}

	if !row.Exists {
		return signin.ErrInvalidCredentials
	}

	return nil
}

// Consume spends a code, atomically.
//
// It is one guarded UPDATE whose predicate repeats every row-state test the
// answer depends on: this owner, this digest, not yet spent. Two requests
// presenting one code at the same instant both reach it; the first one's update
// matches, the second one's finds used_at already set and reports no rows. The
// count is the answer, which is why Verify cannot be.
//
// There is no read-back. A refusal here is one answer whatever caused it — an
// unknown code, a spent one, somebody else's — and a span that told them apart
// would be recording which guesses were real codes.
func (s *SQLStore) Consume(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, code string,
) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateLookup(scope, userID, code); err != nil {
		return err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	if !isCodeShaped(code) {
		return signin.ErrInvalidCredentials
	}

	now := s.clock.Now().UTC()

	affected, err := s.q.SpendRecoveryCode(ctx, tx, recoverycodedb.SpendRecoveryCodeParams{
		UsedAt: &now,
		Scope:  scope,
		UserID: userID,
		Hash:   s.Digest(code),
	})
	if err != nil {
		return op.Error(err, "spending a recovery code")
	}

	if affected == 0 {
		return signin.ErrInvalidCredentials
	}

	return nil
}

// Remaining reports how many unspent codes a user holds.
func (s *SQLStore) Remaining(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (int, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return 0, err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	row, err := s.q.CountUnspentRecoveryCodes(ctx, q, recoverycodedb.CountUnspentRecoveryCodesParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return 0, op.Error(err, "counting a user's recovery codes")
	}

	return int(row.Count), nil
}

// ListForUser answers with every code a user holds, spent and unspent, as
// records. The statement projects no digest, so none leaves the table this way.
func (s *SQLStore) ListForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*RecoveryCode, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	rows, err := s.q.ListRecoveryCodesForUser(ctx, q, recoverycodedb.ListRecoveryCodesForUserParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return nil, op.Error(err, "listing a user's recovery codes")
	}

	codes := make([]*RecoveryCode, 0, len(rows))
	for i := range rows {
		codes = append(codes, &RecoveryCode{
			Scope:    rows[i].Scope,
			UserID:   rows[i].UserID,
			IssuedAt: rows[i].IssuedAt.UTC(),
			UsedAt:   utcPtr(rows[i].UsedAt),
		})
	}

	return codes, nil
}

// DeleteForUser removes every code a user holds, spent or not, and reports how
// many it removed.
//
// It runs in tx, so an erasure and the rest of the subject's footprint are one
// fact rather than two that could half happen.
func (s *SQLStore) DeleteForUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return 0, err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	deleted, err := s.q.DeleteRecoveryCodesForUser(ctx, tx, recoverycodedb.DeleteRecoveryCodesForUserParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return 0, op.Error(err, "deleting a user's recovery codes")
	}

	op.SpanOnly(deletedKey, deleted)

	return deleted, nil
}

// mint draws one code and prints it in groups.
func (s *SQLStore) mint(ctx context.Context) (string, error) {
	raw, err := s.generator.GenerateRawBytes(ctx, rawCodeBytes)
	if err != nil {
		return "", err
	}

	code := codeEncoding.EncodeToString(raw)[:CodeLength]

	var printed strings.Builder

	for i := 0; i < len(code); i += groupLength {
		if i > 0 {
			printed.WriteString(separator)
		}

		printed.WriteString(code[i:min(i+groupLength, len(code))])
	}

	return printed.String(), nil
}

// normalize is a code as it is stored: the separators a person might type
// dropped, whitespace with them, the case folded to the alphabet's, and the
// three digits the alphabet never prints read as the letters they are mistaken
// for.
//
// It is the one place a typed code and a printed one are made the same string,
// so it is applied on the way in to every digest — the mint's and the check's
// alike — and a code copied as "abcd efgh 1jkl" finds the row "ABCD-EFGH-IJKL"
// was stored under. None of it widens what matches: every rewrite maps a
// character no code contains onto one it might, so no two printed codes
// normalize to one string.
func normalize(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-', r == ' ', r == '\t', r == '\n', r == '\r':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r == '0':
			return 'O'
		case r == '1':
			return 'I'
		case r == '8':
			return 'B'
		default:
			return r
		}
	}, code)
}

// validateOwner is the argument check every method shares.
func validateOwner(scope tenancy.Scope, userID string) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if userID == "" {
		return ErrEmptyUserID
	}

	return nil
}

// validateLookup is the argument check a check or a spend makes before it
// hashes.
//
// A code that normalizes to nothing is refused rather than hashed and looked up,
// because the digest of the empty string is a perfectly good digest and would
// cost a round trip to find nothing.
func validateLookup(scope tenancy.Scope, userID, code string) error {
	if err := validateOwner(scope, userID); err != nil {
		return err
	}

	if normalize(code) == "" {
		return ErrEmptyCode
	}

	return nil
}

// isCodeShaped reports whether code could be one this store minted, which is a
// question of its length once normalized and nothing else.
//
// Verify and Consume ask it before they read, because signin tries a recovery
// code only after the TOTP check has refused, so every wrong six-digit code at a
// door that accepts both reaches this store. Refusing one without a query costs
// the refusal nothing it could disclose: the answer is the one a code of the
// right length that matches no row gets.
func isCodeShaped(code string) bool {
	return len(normalize(code)) == CodeLength
}

// utcPtr normalises an optional stamp to UTC, leaving an absent one absent.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}
