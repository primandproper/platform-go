package phonecodes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// bytesGenerator hands back the same bytes every time, so a test can know the
// code it will be texted.
type bytesGenerator struct {
	raw []byte
}

var _ random.Generator = (*bytesGenerator)(nil)

func (g *bytesGenerator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return "", errors.New("unused")
}

func (g *bytesGenerator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return "", errors.New("unused")
}

func (g *bytesGenerator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return "", errors.New("unused")
}

func (g *bytesGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return g.raw, nil
}

func TestNewSQLStore_refusals(T *testing.T) {
	T.Parallel()

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("a dialect it has no queries for", func(t *testing.T) {
		t.Parallel()

		client := &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return "oracle" }}

		_, err := NewSQLStore(client)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("a prefix that would not render", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(newTestClient(t), WithTablePrefix("trailing_"))
		test.Error(t, err)
	})

	for name, opt := range map[string]Option{
		"a code too short":          WithCodeLength(MinCodeLength - 1),
		"a code too long":           WithCodeLength(MaxCodeLength + 1),
		"no lifetime":               WithLifetime(0),
		"an attempt limit of 0":     WithMaxAttempts(0),
		"an attempt limit too high": WithMaxAttempts(MaxAttemptsCeiling + 1),
		"a non-positive retention":  WithRetention(-time.Second),
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := NewSQLStore(newTestClient(t), opt)
			test.ErrorIs(t, err, ErrInvalidSetting)
		})
	}
}

func TestIssue_returnsThePlaintextOnceAndStoresADigest(t *testing.T) {
	t.Parallel()

	store, c := newTestStore(t)

	issuance := issue(t, store)

	must.EqOp(t, DefaultCodeLength, len(issuance.Plaintext))
	test.StrNotContains(t, issuance.Plaintext, " ")
	for _, digit := range issuance.Plaintext {
		test.True(t, digit >= '0' && digit <= '9', test.Sprintf("%q is not all digits", issuance.Plaintext))
	}

	var stored string
	must.NoError(t, store.db.Reader().QueryRowContext(t.Context(),
		"SELECT code_hash FROM phone_codes WHERE id = ?", issuance.Code.ID).Scan(&stored))

	test.NotEq(t, issuance.Plaintext, stored)
	test.StrNotContains(t, stored, issuance.Plaintext)
	test.EqOp(t, store.digest(issuance.Code.ID, issuance.Plaintext), stored)

	test.EqOp(t, testSubject, issuance.Code.SubjectID)
	test.EqOp(t, testPhone, issuance.Code.PhoneNumber)
	test.EqOp(t, DefaultMaxAttempts, issuance.Code.MaxAttempts)
	test.EqOp(t, 0, issuance.Code.Attempts)
	test.True(t, c.Now().Add(DefaultLifetime).Equal(issuance.Code.ExpiresAt))
	test.True(t, issuance.Code.ExpiresAt.Add(DefaultRetention).Equal(issuance.Code.PurgeAfter))
	test.Nil(t, issuance.Previous)
}

// The digest is bound to the row, so one code issued to two numbers is two
// different digests.
func TestIssue_bindsTheDigestToTheRow(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, WithGenerator(&bytesGenerator{raw: []byte{1, 2, 3, 4, 5, 6}}))

	first := issue(t, store)
	second, err := issueFor(t, store, testScope(), &IssueRequest{SubjectID: testSubject, PhoneNumber: "+15555550199"})
	must.NoError(t, err)

	must.EqOp(t, first.Plaintext, second.Plaintext)
	test.NotEq(t, store.digest(first.Code.ID, first.Plaintext), store.digest(second.Code.ID, second.Plaintext))
}

func TestIssue_honorsTheConfiguredSettings(t *testing.T) {
	t.Parallel()

	store, c := newTestStore(t,
		WithCodeLength(8),
		WithLifetime(time.Minute),
		WithMaxAttempts(3),
		WithRetention(time.Hour),
	)

	issuance := issue(t, store)

	test.EqOp(t, 8, len(issuance.Plaintext))
	test.EqOp(t, 3, issuance.Code.MaxAttempts)
	test.True(t, c.Now().Add(time.Minute).Equal(issuance.Code.ExpiresAt))
	test.True(t, c.Now().Add(time.Minute+time.Hour).Equal(issuance.Code.PurgeAfter))

	own, err := issueFor(t, store, testScope(), &IssueRequest{
		SubjectID: testSubject, PhoneNumber: "+15555550101", MaxAttempts: 2,
	})
	must.NoError(t, err)
	test.EqOp(t, 2, own.Code.MaxAttempts)
	test.EqOp(t, 2, readRow(t, store, testScope(), "+15555550101").MaxAttempts)

	_, err = issueFor(t, store, testScope(), &IssueRequest{
		SubjectID: testSubject, PhoneNumber: "+15555550102", MaxAttempts: 4,
	})
	test.ErrorIs(t, err, ErrInvalidMaxAttempts)
}

func TestIssue_refusals(T *testing.T) {
	T.Parallel()

	store, _ := newTestStore(T)

	for name, tc := range map[string]struct {
		want    error
		request *IssueRequest
		scope   tenancy.Scope
	}{
		"nil request":               {scope: testScope(), want: ErrNilRequest},
		"no subject":                {scope: testScope(), request: &IssueRequest{PhoneNumber: testPhone}, want: ErrEmptySubjectID},
		"a subject too long":        {scope: testScope(), request: &IssueRequest{SubjectID: strings.Repeat("s", MaxSubjectLength+1), PhoneNumber: testPhone}, want: ErrValueTooLong},
		"no number":                 {scope: testScope(), request: &IssueRequest{SubjectID: testSubject}, want: ErrInvalidPhoneNumber},
		"a number not in E.164":     {scope: testScope(), request: &IssueRequest{SubjectID: testSubject, PhoneNumber: "+1 555 555 0100"}, want: ErrInvalidPhoneNumber},
		"a number with no plus":     {scope: testScope(), request: &IssueRequest{SubjectID: testSubject, PhoneNumber: "15555550100"}, want: ErrInvalidPhoneNumber},
		"a number too long":         {scope: testScope(), request: &IssueRequest{SubjectID: testSubject, PhoneNumber: "+1234567890123456"}, want: ErrInvalidPhoneNumber},
		"a negative limit":          {scope: testScope(), request: &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone, MaxAttempts: -1}, want: ErrInvalidMaxAttempts},
		"a limit above the store's": {scope: testScope(), request: &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone, MaxAttempts: DefaultMaxAttempts + 1}, want: ErrInvalidMaxAttempts},
		"an unset scope":            {request: &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone}, want: tenancy.ErrNoScope},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := issueFor(t, store, tc.scope, tc.request)
			test.ErrorIs(t, err, tc.want)
		})
	}

	T.Run("a nil transaction", func(t *testing.T) {
		t.Parallel()

		_, err := store.Issue(t.Context(), nil, testScope(), &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone})
		test.ErrorIs(t, err, ErrNilExecutor)
	})
}

func TestRedeem_spendsOnceAndFailsTheSecondTime(t *testing.T) {
	t.Parallel()

	store, c := newTestStore(t)

	issuance := issue(t, store)

	spent, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	must.NoError(t, err)
	test.EqOp(t, issuance.Code.ID, spent.ID)
	test.EqOp(t, testSubject, spent.SubjectID)
	test.EqOp(t, testPhone, spent.PhoneNumber)
	must.NotNil(t, spent.RedeemedAt)
	test.True(t, c.Now().Equal(*spent.RedeemedAt))

	_, err = redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	test.ErrorIs(t, err, ErrCodeInvalid)
}

// The attempt limit, which is the difference between a code and a link: five
// wrong codes and the right one is dead.
func TestRedeem_wrongCodesExhaustTheRightOne(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	issuance := issue(t, store)

	for range DefaultMaxAttempts {
		_, err := redeem(t, store, testScope(), testPhone, wrong(issuance.Plaintext))
		must.ErrorIs(t, err, ErrCodeInvalid)
	}

	test.EqOp(t, DefaultMaxAttempts, readRow(t, store, testScope(), testPhone).Attempts)

	_, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	test.ErrorIs(t, err, ErrCodeInvalid)

	// A dead code is not counted further: nothing could match it.
	test.EqOp(t, DefaultMaxAttempts, readRow(t, store, testScope(), testPhone).Attempts)
}

func TestRedeem_theRightCodeUnderTheLimitWorks(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	issuance := issue(t, store)

	for range DefaultMaxAttempts - 1 {
		_, err := redeem(t, store, testScope(), testPhone, wrong(issuance.Plaintext))
		must.ErrorIs(t, err, ErrCodeInvalid)
	}

	spent, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	must.NoError(t, err)
	test.EqOp(t, DefaultMaxAttempts-1, spent.Attempts)
}

func TestRedeem_honorsAPerCodeLimit(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	issuance, err := issueFor(t, store, testScope(), &IssueRequest{
		SubjectID: testSubject, PhoneNumber: testPhone, MaxAttempts: 1,
	})
	must.NoError(t, err)

	_, err = redeem(t, store, testScope(), testPhone, wrong(issuance.Plaintext))
	must.ErrorIs(t, err, ErrCodeInvalid)

	_, err = redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	test.ErrorIs(t, err, ErrCodeInvalid)
}

// A caller that returns Redeem's error straight out of its callback — the
// natural code — still has every wrong guess counted, because a refusal is not
// an error: up to the limit that way, and the right code is dead.
func TestRedeem_theNaturalReturnErrCommitsTheCount(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	issuance := issue(t, store)

	naive := func(code string) (*Code, bool) {
		t.Helper()

		var (
			spent    *Code
			redeemed bool
		)

		err := withTx(t, store, func(tx database.Tx) error {
			var err error

			spent, redeemed, err = store.Redeem(t.Context(), tx, testScope(), testPhone, code)

			return err
		})
		must.NoError(t, err)

		return spent, redeemed
	}

	for attempt := range DefaultMaxAttempts {
		spent, redeemed := naive(wrong(issuance.Plaintext))
		must.False(t, redeemed)
		must.Nil(t, spent)
		test.EqOp(t, attempt+1, readRow(t, store, testScope(), testPhone).Attempts)
	}

	spent, redeemed := naive(issuance.Plaintext)
	test.False(t, redeemed)
	test.Nil(t, spent)
	test.Nil(t, readRow(t, store, testScope(), testPhone).RedeemedAt)
}

// One live code per number: the second issue replaces the first.
func TestIssue_supersedesTheNumbersPriorCode(t *testing.T) {
	t.Parallel()

	store, c := newTestStore(t)

	first := issue(t, store)

	c.advance(time.Minute)

	second := issue(t, store)
	must.NotNil(t, second.Previous)
	test.EqOp(t, first.Code.ID, second.Previous.ID)
	test.True(t, first.Code.IssuedAt.Equal(second.Previous.IssuedAt))
	test.NotEq(t, first.Code.ID, second.Code.ID)

	if first.Plaintext != second.Plaintext {
		_, err := redeem(t, store, testScope(), testPhone, first.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
	}

	_, err := redeem(t, store, testScope(), testPhone, second.Plaintext)
	test.NoError(t, err)
}

// A replacement starts over: a spent, exhausted or withdrawn code's row is a
// fresh code again once the number is sent a new one.
func TestIssue_aReplacementStartsOver(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	first := issue(t, store)

	for range DefaultMaxAttempts {
		_, err := redeem(t, store, testScope(), testPhone, wrong(first.Plaintext))
		must.ErrorIs(t, err, ErrCodeInvalid)
	}

	second := issue(t, store)
	must.NotNil(t, second.Previous)
	test.EqOp(t, DefaultMaxAttempts, second.Previous.Attempts)

	fresh := readRow(t, store, testScope(), testPhone)
	test.EqOp(t, 0, fresh.Attempts)
	test.Nil(t, fresh.RedeemedAt)
	test.Nil(t, fresh.RevokedAt)

	_, err := redeem(t, store, testScope(), testPhone, second.Plaintext)
	test.NoError(t, err)
}

// A redemption that read the row before a replacement landed matches nothing:
// the spend is keyed on the id it read, and the replacement minted a new one.
func TestRedeem_aReplacementBetweenReadAndSpendMatchesNothing(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, WithGenerator(&bytesGenerator{raw: []byte{1, 2, 3, 4, 5, 6}}))

	first := issue(t, store)
	held := readRow(t, store, testScope(), testPhone)

	_ = issue(t, store)

	err := withTx(t, store, func(tx database.Tx) error {
		spent, spendErr := store.q.SpendPhoneCode(t.Context(), tx, spendParams(store, held, first.Plaintext))
		must.NoError(t, spendErr)
		test.EqOp(t, int64(0), spent)

		return nil
	})
	must.NoError(t, err)
}

func TestRedeem_collapsesEveryRefusal(T *testing.T) {
	T.Parallel()

	T.Run("a number that was never sent a code", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := redeem(t, store, testScope(), testPhone, "123456")
		test.ErrorIs(t, err, ErrCodeInvalid)
	})

	T.Run("a code past its deadline", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store)
		c.advance(DefaultLifetime + time.Second)

		_, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
	})

	T.Run("a code at exactly its deadline", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store)
		c.advance(DefaultLifetime)

		_, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
	})

	T.Run("a code withdrawn with its subject's", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		_, err := revokeForSubject(t, store, testScope(), testSubject)
		must.NoError(t, err)

		_, err = redeem(t, store, testScope(), testPhone, issuance.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
	})

	T.Run("a code presented in another directory", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		_, err := redeem(t, store, tenancy.Of("tenant_other"), testPhone, issuance.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)

		_, err = redeem(t, store, testScope(), testPhone, issuance.Plaintext)
		test.NoError(t, err)
	})

	T.Run("the right code for another number", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		_, err := redeem(t, store, testScope(), "+15555550199", issuance.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
	})
}

func TestRedeem_refusals(T *testing.T) {
	T.Parallel()

	store, _ := newTestStore(T)

	T.Run("an empty code is not a counted guess", func(t *testing.T) {
		t.Parallel()

		_, err := redeem(t, store, testScope(), testPhone, "")
		test.ErrorIs(t, err, ErrEmptyCode)
	})

	T.Run("a malformed number", func(t *testing.T) {
		t.Parallel()

		_, err := redeem(t, store, testScope(), "5555550100", "123456")
		test.ErrorIs(t, err, ErrInvalidPhoneNumber)
	})

	T.Run("an unset scope", func(t *testing.T) {
		t.Parallel()

		_, err := redeem(t, store, tenancy.Scope{}, testPhone, "123456")
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("a nil transaction", func(t *testing.T) {
		t.Parallel()

		spent, redeemed, err := store.Redeem(t.Context(), nil, testScope(), testPhone, "123456")
		test.ErrorIs(t, err, ErrNilExecutor)
		test.False(t, redeemed)
		test.Nil(t, spent)
	})
}

func TestDeadReason(T *testing.T) {
	T.Parallel()

	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-time.Minute)

	live := func() *Code {
		return &Code{ExpiresAt: now.Add(time.Minute), MaxAttempts: 5}
	}

	for name, tc := range map[string]struct {
		mutate func(*Code)
		want   string
	}{
		"live":      {mutate: func(*Code) {}, want: ""},
		"redeemed":  {mutate: func(c *Code) { c.RedeemedAt = &stamp }, want: reasonRedeemed},
		"revoked":   {mutate: func(c *Code) { c.RevokedAt = &stamp }, want: reasonRevoked},
		"expired":   {mutate: func(c *Code) { c.ExpiresAt = now }, want: reasonExpired},
		"exhausted": {mutate: func(c *Code) { c.Attempts = 5 }, want: reasonExhausted},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			code := live()
			tc.mutate(code)

			test.EqOp(t, tc.want, deadReason(code, now))
		})
	}
}

func TestNewCode(T *testing.T) {
	T.Parallel()

	T.Run("discards the bytes that would bias the low digits", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t, WithCodeLength(4), WithGenerator(&bytesGenerator{raw: []byte{250, 13, 255, 7, 99, 24}}))

		code, err := store.newCode(t.Context())
		must.NoError(t, err)
		test.EqOp(t, "3794", code)
	})

	T.Run("a source that yields nothing usable is an error, not a hang", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t, WithGenerator(&bytesGenerator{raw: []byte{255, 254, 253, 252}}))

		_, err := store.newCode(t.Context())
		test.Error(t, err)
	})
}

func TestRevokeForSubject(T *testing.T) {
	T.Parallel()

	T.Run("withdraws every unspent code a person holds, and nobody else's", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		mine := issue(t, store)
		alsoMine, err := issueFor(t, store, testScope(), &IssueRequest{SubjectID: testSubject, PhoneNumber: "+15555550101"})
		must.NoError(t, err)
		theirs, err := issueFor(t, store, testScope(), &IssueRequest{SubjectID: "contact_02", PhoneNumber: "+15555550102"})
		must.NoError(t, err)

		revoked, err := revokeForSubject(t, store, testScope(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		_, err = redeem(t, store, testScope(), testPhone, mine.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
		_, err = redeem(t, store, testScope(), "+15555550101", alsoMine.Plaintext)
		test.ErrorIs(t, err, ErrCodeInvalid)
		_, err = redeem(t, store, testScope(), "+15555550102", theirs.Plaintext)
		test.NoError(t, err)
	})

	T.Run("is idempotent and leaves a spent code alone", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		_, err := redeem(t, store, testScope(), testPhone, issuance.Plaintext)
		must.NoError(t, err)

		revoked, err := revokeForSubject(t, store, testScope(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
		test.Nil(t, readRow(t, store, testScope(), testPhone).RevokedAt)
	})

	T.Run("refusals", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := revokeForSubject(t, store, testScope(), "")
		test.ErrorIs(t, err, ErrEmptySubjectID)

		_, err = revokeForSubject(t, store, tenancy.Scope{}, testSubject)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.RevokeForSubject(t.Context(), nil, testScope(), testSubject)
		test.ErrorIs(t, err, ErrNilExecutor)
	})
}

func TestListAndDeleteForSubject(T *testing.T) {
	T.Parallel()

	T.Run("lists every code a person holds in the scope, oldest first, and deletes them", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		first := issue(t, store)
		c.advance(time.Minute)
		second, err := issueFor(t, store, testScope(), &IssueRequest{SubjectID: testSubject, PhoneNumber: "+15555550101"})
		must.NoError(t, err)
		_, err = issueFor(t, store, tenancy.Of("tenant_other"), &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone})
		must.NoError(t, err)

		codes, err := store.ListForSubject(t.Context(), store.db.Reader(), testScope(), testSubject)
		must.NoError(t, err)
		must.SliceLen(t, 2, codes)
		test.EqOp(t, first.Code.ID, codes[0].ID)
		test.EqOp(t, second.Code.ID, codes[1].ID)

		var deleted int64
		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			var deleteErr error
			deleted, deleteErr = store.DeleteForSubject(t.Context(), tx, testScope(), testSubject)

			return deleteErr
		}))
		test.EqOp(t, int64(2), deleted)

		codes, err = store.ListForSubject(t.Context(), store.db.Reader(), testScope(), testSubject)
		must.NoError(t, err)
		test.SliceEmpty(t, codes)

		other, err := store.ListForSubject(t.Context(), store.db.Reader(), tenancy.Of("tenant_other"), testSubject)
		must.NoError(t, err)
		test.SliceLen(t, 1, other)
	})

	T.Run("refusals", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := store.ListForSubject(t.Context(), nil, testScope(), testSubject)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListForSubject(t.Context(), store.db.Reader(), testScope(), "")
		test.ErrorIs(t, err, ErrEmptySubjectID)

		_, err = store.ListForSubject(t.Context(), store.db.Reader(), tenancy.Scope{}, testSubject)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.DeleteForSubject(t.Context(), nil, testScope(), testSubject)
		test.ErrorIs(t, err, ErrNilExecutor)
	})
}

func TestSweep_collectsOnThePurgeDeadline(t *testing.T) {
	t.Parallel()

	store, c := newTestStore(t)

	issue(t, store)
	_, err := issueFor(t, store, tenancy.Of("tenant_other"), &IssueRequest{SubjectID: testSubject, PhoneNumber: testPhone})
	must.NoError(t, err)

	// Past the deadline and inside the retention window: dead, and kept.
	c.advance(DefaultLifetime + time.Minute)

	swept, err := store.Sweep(t.Context())
	must.NoError(t, err)
	test.EqOp(t, int64(0), swept)

	// Past the purge deadline: collected, across every scope.
	c.advance(DefaultRetention)

	swept, err = store.Sweep(t.Context())
	must.NoError(t, err)
	test.EqOp(t, int64(2), swept)
	test.EqOp(t, 0, rowsIn(t, store.db, "phone_codes"))
}

func TestSweep_logsItsOwnFailureInTheBackgroundLoop(t *testing.T) {
	t.Parallel()

	logger := newRecordingLogger()
	client := newTestClient(t)

	_, err := client.Writer().ExecContext(t.Context(), "DROP TABLE phone_codes")
	must.NoError(t, err)

	c := newFakeClock()

	store, err := NewSQLStore(client, WithClock(c), WithLogger(logger), WithSweeper(t.Context(), time.Minute))
	must.NoError(t, err)
	must.NotNil(t, store)

	c.tick()

	for range 100 {
		if logger.count(backgroundSweepFailure) > 0 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("the background loop never logged %q", backgroundSweepFailure)
}

func TestSQLStore_addressesTheNamespacedTable(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	createTable(t, client, dialect.SQLite, "app")

	store, err := NewSQLStore(client, WithClock(newFakeClock()), WithTablePrefix("app"))
	must.NoError(t, err)

	issuance := issue(t, store)

	test.EqOp(t, 1, rowsIn(t, client, "app_phone_codes"))
	test.EqOp(t, 0, rowsIn(t, client, "phone_codes"))

	_, err = redeem(t, store, testScope(), testPhone, issuance.Plaintext)
	test.NoError(t, err)
}
