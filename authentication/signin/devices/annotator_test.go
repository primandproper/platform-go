package devices_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices"
	devicesmock "github.com/primandproper/platform-go/v15/authentication/signin/devices/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testReader is the executor an annotator is built over.
//
// Nothing executes through it: the store beneath the annotator is a mock, and
// what these tests assert is that the executor the annotator was built with is
// the one it passes down.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the annotator's store is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the annotator's store is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the annotator's store is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the annotator's store is a mock; nothing runs on this")
}

func TestNewAnnotator(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := devices.NewAnnotator(nil, &testReader{})
		test.ErrorIs(t, err, devices.ErrNilStore)

		_, err = devices.NewAnnotator(&devicesmock.StoreMock{}, nil)
		test.ErrorIs(t, err, devices.ErrNilExecutor)
	})

	T.Run("answers each login with what was recorded for it", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		store := &devicesmock.StoreMock{
			ListForFamiliesFunc: func(
				_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string, familyIDs []string,
			) ([]*devices.Device, error) {
				test.EqOp(t, reader, q)
				test.EqOp(t, hookScope, scope)
				test.EqOp(t, "user_1", userID)
				test.Eq(t, []string{"family_full", "family_partial", "family_blank", "family_none"}, familyIDs)

				return []*devices.Device{
					{FamilyID: "family_full", IPAddress: laptop.IPAddress, UserAgent: laptop.UserAgent, DeviceName: laptop.DeviceName},
					{FamilyID: "family_partial", UserAgent: laptop.UserAgent},
					{FamilyID: "family_blank"},
				}, nil
			},
		}

		annotate, err := devices.NewAnnotator(store, reader)
		must.NoError(t, err)

		attributes, err := annotate(t.Context(), hookScope, "user_1",
			[]string{"family_full", "family_partial", "family_blank", "family_none"})
		must.NoError(t, err)

		test.Eq(t, map[string]map[string]string{
			"family_full": {
				devices.AttributeIPAddress:  laptop.IPAddress,
				devices.AttributeUserAgent:  laptop.UserAgent,
				devices.AttributeDeviceName: laptop.DeviceName,
			},
			// Only what is known is named.
			"family_partial": {devices.AttributeUserAgent: laptop.UserAgent},
		}, attributes)
	})

	// An answer with the logins and none of their devices would be a screen
	// that silently stopped saying where, so the store's error is the listing's.
	T.Run("fails the listing when the store cannot answer", func(t *testing.T) {
		t.Parallel()

		store := &devicesmock.StoreMock{
			ListForFamiliesFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string, []string,
			) ([]*devices.Device, error) {
				return nil, errStoreFailed
			},
		}

		annotate, err := devices.NewAnnotator(store, &testReader{})
		must.NoError(t, err)

		attributes, err := annotate(t.Context(), hookScope, "user_1", []string{"family_1"})
		test.ErrorIs(t, err, errStoreFailed)
		test.Nil(t, attributes)
	})
}
