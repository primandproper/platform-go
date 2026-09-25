package recoverycodes

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/internal/recoverycodedb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// printedCode is the shape Replace prints a code in: three groups of four from
// the RFC 4648 base32 alphabet.
var printedCode = regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}$`)

// errGeneratorBroken stands in for a random source that could not be read.
var errGeneratorBroken = platformerrors.New("the random source is broken")

// countingQuerier counts the lookups and spends a store issues, so a test can
// say a refusal was reached without asking the database.
type countingQuerier struct {
	recoverycodedb.Querier

	lookups, spends int
}

func (c *countingQuerier) RecoveryCodeUnspent(
	ctx context.Context,
	db recoverycodedb.DBTX,
	arg recoverycodedb.RecoveryCodeUnspentParams,
) (recoverycodedb.RecoveryCodeUnspentRow, error) {
	c.lookups++

	return c.Querier.RecoveryCodeUnspent(ctx, db, arg)
}

func (c *countingQuerier) SpendRecoveryCode(
	ctx context.Context,
	db recoverycodedb.DBTX,
	arg recoverycodedb.SpendRecoveryCodeParams,
) (int64, error) {
	c.spends++

	return c.Querier.SpendRecoveryCode(ctx, db, arg)
}

// count wraps the harness's store in a countingQuerier and returns it.
func (h *harness) count() *countingQuerier {
	c := &countingQuerier{Querier: h.store.q}
	h.store.q = c

	return c
}

// wrongLengths are a TOTP code, a code one character short and one character
// long: none of them a code this store could have minted.
var wrongLengths = []string{"123456", "AAAA-BBBB-CCC", "AAAA-BBBB-CCCC-D"}

// failingGenerator is a random source that cannot be read.
type failingGenerator struct{ *constantGenerator }

func (failingGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return nil, errGeneratorBroken
}

var _ random.Generator = failingGenerator{&constantGenerator{}}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil, newTestClient(t))
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(&Config{}, nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(&Config{TablePrefix: "ddb_"}, newTestClient(t))
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts the empty prefix and a namespace", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		test.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{TablePrefix: "ddb_"}).ValidateWithContext(t.Context()))
	})
}

func TestSQLStore_Replace(T *testing.T) {
	T.Parallel()

	T.Run("mints the set it was asked for, each code printed and each one live", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		seen := map[string]bool{}

		for _, code := range codes {
			test.True(t, printedCode.MatchString(code), test.Sprintf("code %q", code))
			test.False(t, seen[code], test.Sprintf("code %q minted twice", code))
			seen[code] = true

			test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, code))
		}

		test.EqOp(t, testCount, h.remaining(t, testScope(), testUser))
	})

	T.Run("withdraws the set it replaces, spent codes and all", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		old := h.mint(t, testUser)
		must.NoError(t, h.consume(t, testScope(), testUser, old[0]))

		fresh := h.mint(t, testUser)

		for _, code := range old {
			test.ErrorIs(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, code),
				signin.ErrInvalidCredentials)
		}

		test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, fresh[0]))

		// One set, not two: the spent row of the old set went with it, so "how
		// many has this person used" is a question about the set they hold.
		test.EqOp(t, testCount, rowsIn(t, h.client, "signin_recovery_codes"))
	})

	T.Run("a replacement that rolls back leaves the old set working", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		old := h.mint(t, testUser)

		errRolledBack := platformerrors.New("the caller changed its mind")

		err := h.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			if _, replaceErr := h.store.Replace(t.Context(), tx, testScope(), testUser, testCount); replaceErr != nil {
				return replaceErr
			}

			return errRolledBack
		})
		must.ErrorIs(t, err, errRolledBack)

		test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, old[0]))
		test.EqOp(t, testCount, h.remaining(t, testScope(), testUser))
	})

	T.Run("a code repeated inside a set fails the replacement rather than dropping one", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		old := h.mint(t, testUser)

		repeating := newHarnessOn(t, h.client, &Config{},
			WithGenerator(&constantGenerator{raw: []byte("12345678")}))

		_, err := repeating.replace(t, testScope(), testUser, 2)
		test.Error(t, err)

		// The whole replacement rolled back, so the person still holds the set
		// they were last shown rather than one fewer than a new one.
		test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, old[0]))
	})

	T.Run("reports a random source that could not be read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, WithGenerator(failingGenerator{&constantGenerator{}}))

		_, err := h.replace(t, testScope(), testUser, testCount)
		test.ErrorIs(t, err, errGeneratorBroken)
	})

	T.Run("refuses what names nobody or asks for nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.replace(t, testScope(), "", testCount)
		test.ErrorIs(t, err, ErrEmptyUserID)

		_, err = h.replace(t, testScope(), testUser, 0)
		test.ErrorIs(t, err, ErrNonPositiveCount)

		_, err = h.replace(t, testScope(), testUser, -1)
		test.ErrorIs(t, err, ErrNonPositiveCount)
	})

	T.Run("stores a digest and never the code", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		var stored []string

		rows, err := h.client.Reader().QueryContext(t.Context(), "SELECT hash FROM signin_recovery_codes")
		must.NoError(t, err)

		for rows.Next() {
			var hash string
			must.NoError(t, rows.Scan(&hash))
			stored = append(stored, hash)
		}

		must.NoError(t, rows.Err())
		must.NoError(t, rows.Close())

		joined := strings.Join(stored, "\n")

		for _, code := range codes {
			test.StrNotContains(t, joined, code)
			test.StrNotContains(t, joined, normalize(code))
			test.StrContains(t, joined, h.store.Digest(code))
		}
	})
}

func TestSQLStore_Verify(T *testing.T) {
	T.Parallel()

	T.Run("changes nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		for range 3 {
			test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, codes[0]))
		}

		test.EqOp(t, testCount, h.remaining(t, testScope(), testUser))
	})

	// The ways a code fails to be a code this person holds unspent, which are
	// one answer — the one a wrong TOTP code gets at the same door.
	T.Run("refuses every code that is not one this person holds unspent, alike", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.mint(t, testUser)
		theirs := h.mint(t, "user_02")
		must.NoError(t, h.consume(t, testScope(), testUser, mine[1]))

		for name, attempt := range map[string]struct {
			code  string
			scope tenancy.Scope
		}{
			"unknown":         {"AAAA-BBBB-CCCC", testScope()},
			"spent":           {mine[1], testScope()},
			"somebody else's": {theirs[0], testScope()},
			"another scope":   {mine[0], tenancy.Of("tenant_b")},
		} {
			err := h.store.Verify(t.Context(), h.client.Reader(), attempt.scope, testUser, attempt.code)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials, test.Sprintf("%s code", name))
		}
	})

	T.Run("accepts a code however a person copied it off paper", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		code := h.mint(t, testUser)[0]

		bare := strings.ReplaceAll(code, "-", "")

		for _, typed := range []string{
			strings.ToLower(code),
			bare,
			strings.ToLower(bare),
			strings.ReplaceAll(code, "-", " "),
			"  " + code + "\n",
		} {
			test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, typed),
				test.Sprintf("typed as %q", typed))
		}
	})

	T.Run("refuses a code that is nothing but separators before it hashes", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.ErrorIs(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, ""), ErrEmptyCode)
		test.ErrorIs(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, " - - "), ErrEmptyCode)
		test.ErrorIs(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), "", "AAAA"), ErrEmptyUserID)
	})
	// signin tries a recovery code after every refused TOTP code, so a
	// six-digit code reaching this store is the common case rather than an
	// attack, and it is answered without a read.
	T.Run("refuses a code of the wrong length without reading", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.mint(t, testUser)
		queries := h.count()

		for _, code := range wrongLengths {
			err := h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, code)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials, test.Sprintf("code %q", code))
		}

		test.EqOp(t, 0, queries.lookups)

		// The positive control: a code of the right length is looked up.
		test.ErrorIs(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), testUser, "AAAA-BBBB-CCCC"),
			signin.ErrInvalidCredentials)
		test.EqOp(t, 1, queries.lookups)
	})
}

func TestSQLStore_Consume(T *testing.T) {
	T.Parallel()

	T.Run("spends a code exactly once", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		must.NoError(t, h.consume(t, testScope(), testUser, codes[0]))
		test.EqOp(t, testCount-1, h.remaining(t, testScope(), testUser))

		test.ErrorIs(t, h.consume(t, testScope(), testUser, codes[0]), signin.ErrInvalidCredentials)
		test.EqOp(t, testCount-1, h.remaining(t, testScope(), testUser))

		// The rest are untouched: spending one is not spending the set.
		test.NoError(t, h.consume(t, testScope(), testUser, codes[1]))
	})

	T.Run("stamps when the code was spent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		h.clock.advance(time.Hour)
		spentAt := h.clock.Now()

		must.NoError(t, h.consume(t, testScope(), testUser, codes[0]))

		records, err := h.store.ListForUser(t.Context(), h.client.Reader(), testScope(), testUser)
		must.NoError(t, err)
		must.SliceLen(t, testCount, records)

		var spent []*RecoveryCode

		for _, record := range records {
			if record.UsedAt != nil {
				spent = append(spent, record)
			}
		}

		must.SliceLen(t, 1, spent)
		test.True(t, spent[0].UsedAt.Equal(spentAt), test.Sprintf("stamped %v, spent at %v", spent[0].UsedAt, spentAt))
	})

	T.Run("a spend that rolls back leaves the code unspent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		codes := h.mint(t, testUser)

		errRolledBack := platformerrors.New("the sign-in this code proved was refused")

		err := h.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			if consumeErr := h.store.Consume(t.Context(), tx, testScope(), testUser, codes[0]); consumeErr != nil {
				return consumeErr
			}

			return errRolledBack
		})
		must.ErrorIs(t, err, errRolledBack)

		test.NoError(t, h.consume(t, testScope(), testUser, codes[0]))
	})

	T.Run("refuses somebody else's code and another scope's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		theirs := h.mint(t, "user_02")

		test.ErrorIs(t, h.consume(t, testScope(), testUser, theirs[0]), signin.ErrInvalidCredentials)
		test.ErrorIs(t, h.consume(t, tenancy.Of("tenant_b"), "user_02", theirs[0]), signin.ErrInvalidCredentials)
		test.EqOp(t, testCount, h.remaining(t, testScope(), "user_02"))
	})

	T.Run("refuses an empty code as a caller that did not submit", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.ErrorIs(t, h.consume(t, testScope(), testUser, ""), ErrEmptyCode)
	})
	T.Run("refuses a code of the wrong length without writing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.mint(t, testUser)
		queries := h.count()

		for _, code := range wrongLengths {
			test.ErrorIs(t, h.consume(t, testScope(), testUser, code), signin.ErrInvalidCredentials,
				test.Sprintf("code %q", code))
		}

		test.EqOp(t, 0, queries.spends)
		test.EqOp(t, testCount, h.remaining(t, testScope(), testUser))
	})
}

func TestSQLStore_Remaining(T *testing.T) {
	T.Parallel()

	T.Run("somebody who never asked for a set holds none", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.EqOp(t, 0, h.remaining(t, testScope(), testUser))
	})

	T.Run("refuses a count of nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.store.Remaining(t.Context(), h.client.Reader(), testScope(), "")
		test.ErrorIs(t, err, ErrEmptyUserID)
	})
}

func TestSQLStore_ListForUser(T *testing.T) {
	T.Parallel()

	T.Run("answers with the whole set as records", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.mint(t, testUser)
		h.mint(t, "user_02")

		records, err := h.store.ListForUser(t.Context(), h.client.Reader(), testScope(), testUser)
		must.NoError(t, err)
		must.SliceLen(t, testCount, records)

		for _, record := range records {
			test.EqOp(t, testUser, record.UserID)
			test.EqOp(t, testScope(), record.Scope)
			test.True(t, record.IssuedAt.Equal(h.clock.Now()))
			test.Nil(t, record.UsedAt)
		}
	})

	T.Run("somebody who never asked for a set holds none", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		records, err := h.store.ListForUser(t.Context(), h.client.Reader(), testScope(), testUser)
		must.NoError(t, err)
		test.SliceEmpty(t, records)
	})
}

func TestSQLStore_DeleteForUser(T *testing.T) {
	T.Parallel()

	T.Run("removes one person's set and leaves their neighbour's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.mint(t, testUser)
		theirs := h.mint(t, "user_02")
		must.NoError(t, h.consume(t, testScope(), testUser, mine[0]))

		deleted, err := h.deleteForUser(t, testScope(), testUser)
		must.NoError(t, err)
		test.EqOp(t, int64(testCount), deleted)

		test.EqOp(t, 0, h.remaining(t, testScope(), testUser))
		test.NoError(t, h.store.Verify(t.Context(), h.client.Reader(), testScope(), "user_02", theirs[0]))
	})

	T.Run("somebody who holds nothing deletes nothing and is not an error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		deleted, err := h.deleteForUser(t, testScope(), testUser)
		must.NoError(t, err)
		test.EqOp(t, int64(0), deleted)
	})

	T.Run("refuses a deletion of nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.deleteForUser(t, testScope(), "")
		test.ErrorIs(t, err, ErrEmptyUserID)
	})
}

func TestSQLStore_namespacedTable(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	createTable(t, client, dialect.SQLite, "ddb")

	plain := newHarnessOn(t, client, &Config{})
	namespaced := newHarnessOn(t, client, &Config{TablePrefix: "ddb"})

	codes := namespaced.mint(t, testUser)

	test.EqOp(t, testCount, rowsIn(t, client, "ddb_signin_recovery_codes"))
	test.EqOp(t, 0, rowsIn(t, client, "signin_recovery_codes"))

	test.ErrorIs(t, plain.store.Verify(t.Context(), client.Reader(), testScope(), testUser, codes[0]),
		signin.ErrInvalidCredentials)
	test.NoError(t, namespaced.store.Verify(t.Context(), client.Reader(), testScope(), testUser, codes[0]))
}

func TestNormalize(T *testing.T) {
	T.Parallel()

	T.Run("drops separators and folds case", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ABCDEFGHIJKL", normalize(" abcd-EFGH ijkl\n"))
	})

	// The three digits the alphabet never prints are read as the letters a
	// person squinting at paper mistook them for. Nothing a code contains is
	// rewritten, so no two printed codes normalize to one string.
	T.Run("reads the digits a code never holds as the letters they are mistaken for", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "OIB", normalize("018"))

		for _, r := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567" {
			test.EqOp(t, string(r), normalize(string(r)))
		}
	})
}
