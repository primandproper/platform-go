package shredding

import (
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("derives the table name from the prefix", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "shredding_subject_keys", newTables("").keys)
		test.EqOp(t, "ddb_shredding_subject_keys", newTables("ddb").keys)
		test.EqOp(t, "ddb", newTables("ddb").prefix())
	})
}

func TestBuildSelectRecord(T *testing.T) {
	T.Parallel()

	T.Run("binds the subject pair", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildSelectRecord(d, testSubject)

			test.StrContains(t, query, recordColumns, test.Sprintf("dialect %q", d))
			test.StrContains(t, query, "FROM shredding_subject_keys", test.Sprintf("dialect %q", d))
			test.Eq(t, []any{testSubject.Type, testSubject.ID}, args, test.Sprintf("dialect %q", d))
		}
	})
}

func TestBuildInsertRecord(T *testing.T) {
	T.Parallel()

	T.Run("renders the dialect's skip-a-duplicate clause", func(t *testing.T) {
		t.Parallel()

		record := &Record{Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime}

		pg, _ := newTables("").buildInsertRecord(dialect.Postgres, record)
		test.StrContains(t, pg, "ON CONFLICT (subject_type, subject_id) DO NOTHING")

		my, _ := newTables("").buildInsertRecord(dialect.MySQL, record)
		test.StrContains(t, my, "INSERT IGNORE INTO")
		test.StrNotContains(t, my, "ON CONFLICT")

		lite, _ := newTables("").buildInsertRecord(dialect.SQLite, record)
		test.StrContains(t, lite, "INSERT OR IGNORE INTO")
		test.StrNotContains(t, lite, "ON CONFLICT")
	})

	T.Run("binds a nil shredded_at", func(t *testing.T) {
		t.Parallel()

		record := &Record{Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime}

		for _, d := range allDialects {
			_, args := newTables("").buildInsertRecord(d, record)

			must.SliceLen(t, 5, args, must.Sprintf("dialect %q", d))
			test.Nil(t, args[4], test.Sprintf("dialect %q", d))
		}
	})
}

func TestBuildInsertTombstone(T *testing.T) {
	T.Parallel()

	T.Run("binds no key material and a destruction time", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			_, args := newTables("").buildInsertTombstone(d, testSubject, baseTime)

			must.SliceLen(t, 5, args, must.Sprintf("dialect %q", d))
			test.Nil(t, args[2], test.Sprintf("dialect %q", d))
			test.Eq(t, any(baseTime), args[4], test.Sprintf("dialect %q", d))
		}
	})
}

func TestBuildShred(T *testing.T) {
	T.Parallel()

	T.Run("nulls the key material under a guard on the tombstone", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildShred(d, testSubject, baseTime)

			test.StrContains(t, query, "SET wrapped_key = NULL", test.Sprintf("dialect %q", d))

			// The guard is what makes a second shred a no-op instead of
			// rewriting the timestamp of a destruction that already happened.
			test.StrContains(t, query, "shredded_at IS NULL", test.Sprintf("dialect %q", d))
			test.Eq(t, []any{baseTime, testSubject.Type, testSubject.ID}, args, test.Sprintf("dialect %q", d))
		}
	})
}
