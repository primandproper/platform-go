package assembled_test

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/audit"
	auditcfg "github.com/primandproper/platform-go/v15/audit/config"
	auditmigrations "github.com/primandproper/platform-go/v15/audit/migrations"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/outbox"
	outboxcfg "github.com/primandproper/platform-go/v15/outbox/config"
	outboxmigrations "github.com/primandproper/platform-go/v15/outbox/migrations"
	"github.com/primandproper/platform-go/v15/service"
	"github.com/primandproper/platform-go/v15/waitlists"
	waitlistscfg "github.com/primandproper/platform-go/v15/waitlists/config"
	waitlistsmigrations "github.com/primandproper/platform-go/v15/waitlists/migrations"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhookscfg "github.com/primandproper/platform-go/v15/webhooks/config"
	webhooksmigrations "github.com/primandproper/platform-go/v15/webhooks/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/identifiers"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/logging/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingBoot is what a recording run configures beside the database and the
// waitlist store whose writes it watches.
type recordingBoot struct {
	// publishes is whether the run configures Webhooks and Outbox beside Audit,
	// which is the whole of what decides whether a service records.
	publishes bool
}

// recordingRun is a service booted for a recording assertion: its injector,
// and what its logger was told.
type recordingRun struct {
	i        do.Injector
	client   database.Client
	warnings *warnings
}

// TestRecording_IsTheDefault is the claim service.Register makes about
// recording, asserted end to end over SQLite: configure an audit log, webhooks
// and an outbox, and every platform write records an entry and an event without
// the application registering a single hook.
func TestRecording_IsTheDefault(T *testing.T) {
	T.Parallel()

	T.Run("a waitlist join records an entry and emits an event with no hooks registered", func(t *testing.T) {
		t.Parallel()

		run := bootRecording(t, recordingBoot{publishes: true})
		scope := tenancy.Of(identifiers.New())

		// A subscriber to the join, so the event the join emits is observable
		// as the delivery it fans out to rather than as an outbox row.
		hooked := do.MustInvoke[webhooks.Store](run.i)
		endpointID := identifiers.New()
		must.NoError(t, run.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := hooked.SaveEndpoint(t.Context(), tx, scope, &webhooks.Endpoint{
				ID:            endpointID,
				URL:           "https://93.184.216.34/" + endpointID,
				ContentType:   webhooks.DefaultContentType,
				Secret:        webhooks.Secret{Current: []byte("signing-key-" + endpointID)},
				Subscriptions: webhooks.SubscribeTo(waitlists.EventSignupJoined),
			})

			return err
		}))

		signup := joinWaitlist(t, run, scope)

		entries := auditEntries(t, run, scope, waitlists.ResourceTypeSignup)
		must.SliceLen(t, 1, entries)
		test.EqOp(t, signup.ID, entries[0].ResourceID)
		test.EqOp(t, audit.ActorUnattributed, entries[0].Actor.ID)

		// The endpoint's own registration is recorded too, by the webhooks
		// store's hooks, which write through an emitter that does not dispatch
		// through that store.
		test.SliceLen(t, 1, auditEntries(t, run, scope, webhooks.ResourceTypeEndpoint))

		depth, _, err := hooked.Backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), depth, test.Sprint("the join fanned out to the one subscriber"))

		test.SliceEmpty(t, run.warnings.said())
	})

	T.Run("audit without webhooks records nothing, and says so at startup", func(t *testing.T) {
		t.Parallel()

		run := bootRecording(t, recordingBoot{publishes: false})
		scope := tenancy.Of(identifiers.New())

		joinWaitlist(t, run, scope)

		test.SliceEmpty(t, auditEntries(t, run, scope, waitlists.ResourceTypeSignup))

		said := run.warnings.said()
		must.SliceLen(t, 1, said)
		test.StrContains(t, said[0], "record no audit entry")
	})

	T.Run("a service that would record and has no principal extractor refuses to start, naming it", func(t *testing.T) {
		t.Parallel()

		cfg, _ := recordingConfig(t, recordingBoot{publishes: true})

		i := do.New()
		do.ProvideValue(i, t.Context())
		service.Register(i, cfg)
		do.ProvideValue(i, recordingCatalog())

		_, err := service.New(i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[callers.PrincipalExtractor]())
	})
}

// recordingConfig is a service.Config over a fresh SQLite database with an
// audit log and a waitlist store, and webhooks and an outbox when boot says so.
func recordingConfig(t *testing.T, boot recordingBoot) (cfg *service.Config, prefix string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "recording.db")
	prefix = fmt.Sprintf("rec_%d", prefixCounter.Add(1))

	cfg = &service.Config{
		Name: "recording",
		Database: &databasecfg.Config{
			Provider:        databasecfg.ProviderSQLite,
			ReadConnection:  databasecfg.ConnectionDetails{Database: path},
			WriteConnection: databasecfg.ConnectionDetails{Database: path},
			MaxOpenConns:    1,
		},
		Audit:     &auditcfg.Config{Dialect: dialect.SQLite, TablePrefix: prefix},
		Waitlists: &waitlistscfg.Config{TablePrefix: prefix},
	}

	if boot.publishes {
		cfg.Webhooks = &webhookscfg.Config{TablePrefix: prefix}
		cfg.Outbox = &outboxcfg.Config{
			Queue: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
			Relay: outbox.RelayConfig{TablePrefix: prefix},
		}
	}

	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	return cfg, prefix
}

// bootRecording builds a service from recordingConfig the way a consumer's
// main does, adding only what an application owes: the catalog its webhooks
// publish, and the extractor that says who is writing.
func bootRecording(t *testing.T, boot recordingBoot) *recordingRun {
	t.Helper()

	cfg, prefix := recordingConfig(t, boot)

	said := &warnings{Logger: noop.NewLogger()}

	i := do.New()
	do.ProvideValue(i, t.Context())
	// The pillars, registered whole, are what New logs through, so the
	// startup notice lands here.
	do.ProvideValue(i, &observability.Pillars{Logger: said})

	service.Register(i, cfg)

	do.ProvideValue(i, recordingCatalog())
	do.ProvideValue[callers.PrincipalExtractor](i, func(context.Context) (callers.Principal, bool) {
		return nil, false
	})

	_, err := service.New(i)
	must.NoError(t, err)

	client := do.MustInvoke[database.Client](i)

	schemas := []func(dialect.Dialect, string) ([]string, error){
		auditmigrations.Statements,
		waitlistsmigrations.Statements,
	}
	if boot.publishes {
		schemas = append(schemas, webhooksmigrations.Statements, outboxmigrations.Statements)
	}

	for _, render := range schemas {
		stmts, renderErr := render(dialect.SQLite, prefix)
		must.NoError(t, renderErr)

		for _, stmt := range stmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}
	}

	return &recordingRun{i: i, client: client, warnings: said}
}

// recordingCatalog is the application's catalog: the waitlist events and the
// webhook events, so both stores' events are offered to subscribers.
func recordingCatalog() webhooks.Catalog {
	catalog := webhooks.Catalog{}
	maps.Copy(catalog, waitlists.EventCatalog())
	maps.Copy(catalog, webhooks.EventCatalog())

	return catalog
}

// joinWaitlist opens a list in scope and joins it, through the store the
// composition root built, and returns the signup.
func joinWaitlist(t *testing.T, run *recordingRun, scope tenancy.Scope) *waitlists.Signup {
	t.Helper()

	store := do.MustInvoke[waitlists.Store](run.i)

	var signup *waitlists.Signup

	must.NoError(t, run.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		list, err := store.CreateList(t.Context(), tx, scope, &waitlists.List{
			Name:     "Launch",
			ClosesAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			return err
		}

		signup, err = store.Join(t.Context(), tx, scope, list.ID, &waitlists.Signup{Contact: "ada@example.com"})

		return err
	}))

	return signup
}

// auditEntries reads scope's entries about one kind of resource.
func auditEntries(t *testing.T, run *recordingRun, scope tenancy.Scope, resourceType string) []*audit.Entry {
	t.Helper()

	reader := do.MustInvoke[audit.Reader](run.i)

	page, err := reader.List(t.Context(), run.client.Reader(), scope, &audit.Query{ResourceType: resourceType}, nil)
	must.NoError(t, err)

	return page.Data
}

// warnings is a logger that keeps what it is warned of.
type warnings struct {
	logging.Logger

	messages []string

	mu sync.Mutex
}

func (w *warnings) Warn(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.messages = append(w.messages, msg)
}

func (w *warnings) WithName(string) logging.Logger           { return w }
func (w *warnings) WithValue(string, any) logging.Logger     { return w }
func (w *warnings) WithValues(map[string]any) logging.Logger { return w }
func (w *warnings) Clone() logging.Logger                    { return w }

func (w *warnings) said() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.messages)
}
