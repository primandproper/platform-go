package recordingcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testScope = tenancy.Of("account-1")

// filed is an audit.Recorder that keeps the scope each batch was filed under.
func filed(t *testing.T) (*auditmock.RecorderMock, *[]tenancy.Scope) {
	t.Helper()

	var scopes []tenancy.Scope

	return &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, scope tenancy.Scope, _ ...*audit.Entry) error {
			scopes = append(scopes, scope)

			return nil
		},
	}, &scopes
}

func testEmitter(t *testing.T) *webhooks.Emitter {
	t.Helper()

	emitter, err := webhooks.NewEmitter(&webhooksmock.EnqueuerMock{}, &webhooksmock.DispatcherMock{}, "events")
	must.NoError(t, err)

	return emitter
}

func nobody(context.Context) (callers.Principal, bool) { return nil, false }

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("files by the write", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, FileByWrite, cfg.FileBy)
	})

	T.Run("leaves an explicit rule alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{FileBy: FileBySubject}
		cfg.EnsureDefaults()

		test.EqOp(t, FileBySubject, cfg.FileBy)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts both rules", func(t *testing.T) {
		t.Parallel()

		for _, rule := range []FileBy{FileByWrite, FileBySubject} {
			test.NoError(t, (&Config{FileBy: rule}).ValidateWithContext(t.Context()))
		}
	})

	T.Run("refuses a rule it does not know", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{FileBy: "tenant"}).ValidateWithContext(t.Context()))
	})
}

func TestNewRecorder(T *testing.T) {
	T.Parallel()

	entry := &recording.Entry{ResourceType: "thing", ResourceID: "thing-1", SubjectID: "person-1", EventType: audit.EventCreated}

	T.Run("files by the write by default", func(t *testing.T) {
		t.Parallel()

		entries, scopes := filed(t)

		recorder, err := NewRecorder(t.Context(), &Config{}, entries, testEmitter(t), nobody)
		must.NoError(t, err)

		must.NoError(t, recorder.Record(t.Context(), database.NewTxForTesting(nil), testScope, nil, entry))
		test.Eq(t, []tenancy.Scope{testScope}, *scopes)
	})

	T.Run("files by the subject when the config says so", func(t *testing.T) {
		t.Parallel()

		entries, scopes := filed(t)

		recorder, err := NewRecorder(t.Context(), &Config{FileBy: FileBySubject}, entries, testEmitter(t), nobody)
		must.NoError(t, err)

		must.NoError(t, recorder.Record(t.Context(), database.NewTxForTesting(nil), testScope, nil, entry))
		test.Eq(t, []tenancy.Scope{tenancy.Of("person-1")}, *scopes)
	})

	T.Run("files an entry naming no subject by the write, even when filing by subject", func(t *testing.T) {
		t.Parallel()

		entries, scopes := filed(t)

		recorder, err := NewRecorder(t.Context(), &Config{FileBy: FileBySubject}, entries, testEmitter(t), nobody)
		must.NoError(t, err)

		anonymous := *entry
		anonymous.SubjectID = ""

		must.NoError(t, recorder.Record(t.Context(), database.NewTxForTesting(nil), testScope, nil, &anonymous))
		test.Eq(t, []tenancy.Scope{testScope}, *scopes)
	})

	T.Run("a recorder option overrides the config's rule", func(t *testing.T) {
		t.Parallel()

		entries, scopes := filed(t)
		elsewhere := tenancy.Of("elsewhere")

		recorder, err := NewRecorder(t.Context(), &Config{FileBy: FileBySubject}, entries, testEmitter(t), nobody,
			WithRecorderOptions(recording.WithScopeResolver(func(context.Context, tenancy.Scope, *recording.Entry) tenancy.Scope {
				return elsewhere
			})),
		)
		must.NoError(t, err)

		must.NoError(t, recorder.Record(t.Context(), database.NewTxForTesting(nil), testScope, nil, entry))
		test.Eq(t, []tenancy.Scope{elsewhere}, *scopes)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(t.Context(), nil, &auditmock.RecorderMock{}, testEmitter(t), nobody)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(t.Context(), &Config{FileBy: "tenant"}, &auditmock.RecorderMock{}, testEmitter(t), nobody)
		test.Error(t, err)
	})

	T.Run("each of the three is required", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(t.Context(), &Config{}, nil, testEmitter(t), nobody)
		test.ErrorIs(t, err, recording.ErrNilAuditRecorder)

		_, err = NewRecorder(t.Context(), &Config{}, &auditmock.RecorderMock{}, nil, nobody)
		test.ErrorIs(t, err, recording.ErrNilEmitter)

		_, err = NewRecorder(t.Context(), &Config{}, &auditmock.RecorderMock{}, testEmitter(t), nil)
		test.ErrorIs(t, err, recording.ErrNilPrincipalExtractor)
	})
}
