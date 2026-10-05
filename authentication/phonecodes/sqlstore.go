package phonecodes

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/phonecodes/internal/phonecodesdb"
	"github.com/primandproper/platform-go/v15/authentication/phonecodes/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// serviceName names the loggers, spans, and instruments this store emits.
const serviceName = "phone_codes"

// The keys this store records against a span and a log line. None of them is
// the code or its digest.
const (
	scopeKey   = serviceName + ".scope"
	idKey      = serviceName + ".id"
	subjectKey = serviceName + ".subject_id"
	countKey   = serviceName + ".count"

	// reasonKey records which test refused a redemption. It is on the span only:
	// the caller is told one sentinel for every refusal, and this is where the
	// difference survives.
	reasonKey = serviceName + ".reason"
)

// The reasons a redemption can be refused, as a span records them. They are
// spelled here rather than at the comparison because the set is what a
// dashboard groups by.
const (
	reasonUnknown   = "no code for the number"
	reasonRedeemed  = "already redeemed"
	reasonRevoked   = "revoked"
	reasonExpired   = "expired"
	reasonExhausted = "too many attempts"
	reasonWrongCode = "wrong code"
	reasonLostRace  = "changed since it was read"
)

// maxDrawRounds bounds how many times newCode asks the generator for bytes.
// Rejection sampling discards a byte one time in forty, so a crypto/rand source
// never comes close; a generator that returns only rejectable bytes is broken,
// and this turns it into an error rather than a hang.
const maxDrawRounds = 64

// DefaultTablePrefix is the namespace the code table carries when none is
// configured, which is none — rendering phone_codes. A namespace must not end
// in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema
// authentication/phonecodes/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice. It does one thing more than Store
// describes: Sweep, which a store backed by something that expires its own
// entries would not need.
type SQLStore struct {
	// db is not what the writes run on — those are handed the caller's
	// transaction. It is the executor Sweep runs on, which belongs to nobody's
	// request and so can join nobody's transaction.
	db          database.Client
	q           phonecodesdb.Querier
	clock       clock.Clock
	generator   random.Generator
	hasher      hashing.Hasher
	o11y        observability.Observer
	instruments *metrics.OperationSet

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	lifetime  time.Duration
	retention time.Duration

	codeLength  int
	maxAttempts int
}

// NewSQLStore builds a SQLStore over a database client.
//
// The client supplies the dialect the generated statements are rendered for
// and the executor Sweep runs on. Every other method runs on the executor its
// caller hands over.
//
// Defaults are applied before the settings are checked, so an option left out
// is a default rather than a refusal, and an option given a value this store
// cannot honor is ErrInvalidSetting rather than a value quietly clamped.
//
// It does not create the table. Hand migrations.SQL to your own migration run.
func NewSQLStore(client database.Client, opts ...Option) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "phone code store dialect %q", d)
	}

	o := newOptions(opts)

	if err := o.validate(); err != nil {
		return nil, err
	}

	if err := migrations.ValidatePrefix(o.prefix); err != nil {
		return nil, err
	}

	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	s := &SQLStore{
		db:          client,
		clock:       o.clock,
		generator:   o.generator,
		hasher:      o.hasher,
		lifetime:    o.lifetime,
		retention:   o.retention,
		codeLength:  o.codeLength,
		maxAttempts: o.maxAttempts,
		o11y:        observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	if s.q, err = phonecodesdb.New(qd, ddl.Qualify(o.prefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the phone code querier")
	}

	if s.instruments, err = metrics.NewOperationSet(o.metricsProvider, serviceName); err != nil {
		return nil, err
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// validate refuses the settings this store cannot honor.
func (o *options) validate() error {
	switch {
	case o.codeLength < MinCodeLength || o.codeLength > MaxCodeLength:
		return platformerrors.Wrapf(ErrInvalidSetting, "code length %d is outside [%d, %d]",
			o.codeLength, MinCodeLength, MaxCodeLength)
	case o.lifetime <= 0:
		return platformerrors.Wrapf(ErrInvalidSetting, "lifetime %s is not positive", o.lifetime)
	case o.maxAttempts < 1 || o.maxAttempts > MaxAttemptsCeiling:
		return platformerrors.Wrapf(ErrInvalidSetting, "attempt limit %d is outside [1, %d]",
			o.maxAttempts, MaxAttemptsCeiling)
	case o.retention <= 0:
		return platformerrors.Wrapf(ErrInvalidSetting, "retention %s is not positive", o.retention)
	default:
		return nil
	}
}

// querierDialect maps this module's dialect names onto the generated package's.
func querierDialect(d dialect.Dialect) (phonecodesdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return phonecodesdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return phonecodesdb.DialectMySQL, nil
	case dialect.SQLite:
		return phonecodesdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated phone code queries for dialect %q", d)
	}
}

// failed counts a failed operation and hands the error back unchanged.
func (s *SQLStore) failed(ctx context.Context, err error) error {
	s.instruments.Failed(ctx)

	return err
}

// digest renders what the digest column holds for a code issued under id.
//
// The id is bound in so that one code issued to two numbers is two different
// digests: a reader holding a dump then has to work through the million
// candidates once per row rather than once for the table. It does not make a
// six-digit code expensive to recover from its digest, and nothing could — see
// the migrations package, which says what the digest is for.
func (s *SQLStore) digest(id, code string) string {
	return hashing.HexString(s.hasher, id+":"+code)
}

// Issue mints a code for a phone number, replacing whatever the number held.
// See Store.Issue.
//
// Two statements on the caller's transaction: the read of the code being
// replaced, for Issuance.Previous, and an upsert onto (scope, phone_number).
// The upsert, not the read, is what keeps one code per number — two issues
// racing both read the same previous code, and the second's write waits for
// the first's and replaces it. On MySQL, InnoDB may break that race by killing
// one of the two with a deadlock instead, which the caller starts over from;
// see Store.Issue.
func (s *SQLStore) Issue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	request *IssueRequest,
) (*Issuance, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	maxAttempts, err := s.validateIssue(tx, scope, request)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "issuing phone code"))
	}

	op.Set(subjectKey, request.SubjectID)

	previous, err := s.read(ctx, tx, scope, request.PhoneNumber)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, s.failed(ctx, op.Error(err, "reading the code being replaced"))
	}

	plaintext, err := s.newCode(ctx)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "generating phone code"))
	}

	now := s.clock.Now().UTC()

	code := &Code{
		ID:          identifiers.New(),
		Scope:       scope,
		SubjectID:   request.SubjectID,
		PhoneNumber: request.PhoneNumber,
		MaxAttempts: maxAttempts,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.lifetime),
		PurgeAfter:  now.Add(s.lifetime).Add(s.retention),
	}

	op.Set(idKey, code.ID)

	if err = s.q.IssuePhoneCode(ctx, tx, phonecodesdb.IssuePhoneCodeParams{
		ID:          code.ID,
		Scope:       code.Scope,
		SubjectID:   code.SubjectID,
		PhoneNumber: code.PhoneNumber,
		CodeHash:    s.digest(code.ID, plaintext),
		Attempts:    0,
		MaxAttempts: int64(code.MaxAttempts),
		IssuedAt:    code.IssuedAt,
		ExpiresAt:   code.ExpiresAt,
		PurgeAfter:  code.PurgeAfter,
		RedeemedAt:  nil,
		RevokedAt:   nil,
	}); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing phone code row"))
	}

	return &Issuance{Code: code, Previous: previous, Plaintext: plaintext}, nil
}

// validateIssue is the argument check an issue makes, answering with the
// attempt limit the code is issued with.
func (s *SQLStore) validateIssue(tx database.Tx, scope tenancy.Scope, request *IssueRequest) (int, error) {
	if tx == nil {
		return 0, ErrNilExecutor
	}

	if err := scope.Validate(); err != nil {
		return 0, err
	}

	if request == nil {
		return 0, ErrNilRequest
	}

	if err := validateSubject(request.SubjectID); err != nil {
		return 0, err
	}

	if !e164.MatchString(request.PhoneNumber) {
		return 0, ErrInvalidPhoneNumber
	}

	switch {
	case request.MaxAttempts < 0:
		return 0, ErrInvalidMaxAttempts
	case request.MaxAttempts > s.maxAttempts:
		return 0, platformerrors.Wrapf(ErrInvalidMaxAttempts, "%d is above the store's %d",
			request.MaxAttempts, s.maxAttempts)
	case request.MaxAttempts == 0:
		return s.maxAttempts, nil
	default:
		return request.MaxAttempts, nil
	}
}

// validateSubject refuses an empty subject and one wider than the column.
func validateSubject(subjectID string) error {
	if subjectID == "" {
		return ErrEmptySubjectID
	}

	if len(subjectID) > MaxSubjectLength {
		return platformerrors.Wrapf(ErrValueTooLong, "subject is %d bytes, at most %d", len(subjectID), MaxSubjectLength)
	}

	return nil
}

// newCode draws a code of the configured length, uniformly over the digits.
//
// Each digit is a byte taken modulo ten, and bytes of 250 and above are
// discarded rather than folded: 256 is not a multiple of ten, so folding them
// would make 0 through 5 slightly likelier than 6 through 9, which is a
// guesser's head start.
func (s *SQLStore) newCode(ctx context.Context) (string, error) {
	digits := make([]byte, 0, s.codeLength)

	for range maxDrawRounds {
		raw, err := s.generator.GenerateRawBytes(ctx, s.codeLength)
		if err != nil {
			return "", err
		}

		for _, b := range raw {
			if b >= 250 {
				continue
			}

			digits = append(digits, '0'+b%10)
			if len(digits) == s.codeLength {
				return string(digits), nil
			}
		}
	}

	return "", platformerrors.New("the random source produced no usable bytes")
}

// Redeem spends the code a phone number holds, if code is it. See Store.Redeem,
// which says why a refusal is a result rather than an error.
//
// It reads the row first, on tx, because two things it needs are only on the
// row: the id the digest is bound to, and the attempt count both writes compare
// against. Then one of two guarded writes:
//
//   - The spend, whose predicate repeats every test the answer rests on — the
//     row read, the digest, neither stamp, the deadline, and the attempt count
//     read. One is the answer.
//   - When the spend matched nothing, the count: a compare-and-set of the
//     attempt count read, under the same liveness guards. Two wrong codes racing
//     both read one count; the first moves it and the second is refused
//     uncounted, which loses nothing, because exactly one guess took effect for
//     each value the count passed through.
//
// A row the read already shows dead — spent, withdrawn, expired, or at its
// limit — is refused without a write, since neither write could match it.
func (s *SQLStore) Redeem(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	phoneNumber, code string,
) (*Code, bool, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if err := validateRedemption(tx, scope, phoneNumber, code); err != nil {
		return nil, false, s.failed(ctx, op.Error(err, "redeeming phone code"))
	}

	held, err := s.read(ctx, tx, scope, phoneNumber)
	if errors.Is(err, sql.ErrNoRows) {
		op.SpanOnly(reasonKey, reasonUnknown)

		return nil, false, nil
	}

	if err != nil {
		return nil, false, s.failed(ctx, op.Error(err, "reading phone code row"))
	}

	op.Set(idKey, held.ID).Set(subjectKey, held.SubjectID)

	now := s.clock.Now().UTC()

	if reason := deadReason(held, now); reason != "" {
		op.SpanOnly(reasonKey, reason)

		return nil, false, nil
	}

	spent, err := s.q.SpendPhoneCode(ctx, tx, phonecodesdb.SpendPhoneCodeParams{
		RedeemedAt:       &now,
		ID:               held.ID,
		Scope:            scope,
		CodeHash:         s.digest(held.ID, code),
		Now:              now,
		ExpectedAttempts: int64(held.Attempts),
	})
	if err != nil {
		return nil, false, s.failed(ctx, op.Error(err, "spending phone code row"))
	}

	if spent == 1 {
		held.RedeemedAt = &now

		return held, true, nil
	}

	counted, err := s.q.CountPhoneCodeAttempt(ctx, tx, phonecodesdb.CountPhoneCodeAttemptParams{
		Attempts:         int64(held.Attempts) + 1,
		ID:               held.ID,
		Scope:            scope,
		Now:              now,
		ExpectedAttempts: int64(held.Attempts),
	})
	if err != nil {
		return nil, false, s.failed(ctx, op.Error(err, "counting a wrong phone code"))
	}

	if counted == 0 {
		op.SpanOnly(reasonKey, reasonLostRace)
	} else {
		op.SpanOnly(reasonKey, reasonWrongCode)
	}

	return nil, false, nil
}

// validateRedemption is the argument check a redemption makes before it reads.
// A malformed number cannot hold a code, so it is refused as one rather than
// looked up.
func validateRedemption(tx database.Tx, scope tenancy.Scope, phoneNumber, code string) error {
	if tx == nil {
		return ErrNilExecutor
	}

	if err := scope.Validate(); err != nil {
		return err
	}

	if !e164.MatchString(phoneNumber) {
		return ErrInvalidPhoneNumber
	}

	if code == "" {
		return ErrEmptyCode
	}

	return nil
}

// deadReason names which test a row already fails, or "" for a live one. The
// arms are the spend's guards, in the order its predicate lists them.
func deadReason(code *Code, now time.Time) string {
	switch {
	case code.RedeemedAt != nil:
		return reasonRedeemed
	case code.RevokedAt != nil:
		return reasonRevoked
	case !now.Before(code.ExpiresAt):
		return reasonExpired
	case code.Attempts >= code.MaxAttempts:
		return reasonExhausted
	default:
		return ""
	}
}

// read is the row one number holds, or sql.ErrNoRows.
func (s *SQLStore) read(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	phoneNumber string,
) (*Code, error) {
	row, err := s.q.GetPhoneCode(ctx, q, phonecodesdb.GetPhoneCodeParams{Scope: scope, PhoneNumber: phoneNumber})
	if err != nil {
		return nil, err
	}

	converted := phonecodesdb.ListPhoneCodesForSubjectRow(row)

	return codeFromRow(&converted), nil
}

// RevokeForSubject withdraws every unspent code one person holds. See
// Store.RevokeForSubject.
func (s *SQLStore) RevokeForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subjectID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subjectID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if err := validateSubjectCall(tx, scope, subjectID); err != nil {
		return 0, s.failed(ctx, op.Error(err, "withdrawing phone codes"))
	}

	at := s.clock.Now().UTC()

	revoked, err := s.q.RevokePhoneCodesForSubject(ctx, tx, phonecodesdb.RevokePhoneCodesForSubjectParams{
		RevokedAt: &at,
		Scope:     scope,
		SubjectID: subjectID,
	})
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "withdrawing phone codes"))
	}

	op.SpanOnly(countKey, revoked)

	return revoked, nil
}

// ListForSubject reads every code one person holds. See Store.ListForSubject.
func (s *SQLStore) ListForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subjectID string,
) ([]*Code, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subjectID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing phone codes"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing phone codes"))
	}

	if err := validateSubject(subjectID); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing phone codes"))
	}

	rows, err := s.q.ListPhoneCodesForSubject(ctx, q, phonecodesdb.ListPhoneCodesForSubjectParams{
		Scope:     scope,
		SubjectID: subjectID,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing phone codes"))
	}

	codes := make([]*Code, 0, len(rows))
	for i := range rows {
		codes = append(codes, codeFromRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(codes))

	return codes, nil
}

// DeleteForSubject destroys every code one person holds. See
// Store.DeleteForSubject.
func (s *SQLStore) DeleteForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subjectID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subjectID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if err := validateSubjectCall(tx, scope, subjectID); err != nil {
		return 0, s.failed(ctx, op.Error(err, "deleting phone codes"))
	}

	deleted, err := s.q.DeletePhoneCodesForSubject(ctx, tx, phonecodesdb.DeletePhoneCodesForSubjectParams{
		Scope:     scope,
		SubjectID: subjectID,
	})
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "deleting phone codes"))
	}

	op.SpanOnly(countKey, deleted)

	return deleted, nil
}

// validateSubjectCall is the argument check the two subject-keyed writes share.
func validateSubjectCall(tx database.Tx, scope tenancy.Scope, subjectID string) error {
	if tx == nil {
		return ErrNilExecutor
	}

	if err := scope.Validate(); err != nil {
		return err
	}

	return validateSubject(subjectID)
}

// codeFromRow renders a read row as the value the seam carries. The digest is
// not among the columns any read projects, so there is nothing here to drop.
//
// The stamps come back in whatever zone the driver decided, and are normalized
// to UTC, which is the only clock this module reads.
func codeFromRow(row *phonecodesdb.ListPhoneCodesForSubjectRow) *Code {
	return &Code{
		ID:          row.ID,
		Scope:       row.Scope,
		SubjectID:   row.SubjectID,
		PhoneNumber: row.PhoneNumber,
		Attempts:    int(row.Attempts),
		MaxAttempts: int(row.MaxAttempts),
		IssuedAt:    row.IssuedAt.UTC(),
		ExpiresAt:   row.ExpiresAt.UTC(),
		PurgeAfter:  row.PurgeAfter.UTC(),
		RedeemedAt:  utcPtr(row.RedeemedAt),
		RevokedAt:   utcPtr(row.RevokedAt),
	}
}

// utcPtr normalizes an optional stamp to UTC, leaving an absent one absent.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}
