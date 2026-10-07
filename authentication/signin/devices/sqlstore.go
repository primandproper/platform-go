package devices

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices/internal/devicesdb"
	"github.com/primandproper/platform-go/v15/authentication/signin/devices/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// serviceName names the loggers, spans and instruments this store emits.
const serviceName = "signin_devices"

// The keys this store records against a span and a log line.
//
// None of them is a recorded value. An address and a user agent are personal
// data, and a trace is a place operators paste into tickets — so the span says
// whose login and which one, and never where it came from.
const (
	userIDKey   = "identity.user_id"
	scopeKey    = "identity.scope"
	familyIDKey = "signin.family_id"

	// deletedKey is how many rows an erasure removed. On the span only: it is a
	// fact about one request rather than a metric.
	deletedKey = "signin.devices_deleted"
)

// DefaultTablePrefix is the namespace the device table carries when none is
// configured, which is none — rendering plain "signin_devices".
//
// The signin_devices segment is the schema's, not the caller's: a table always
// says which package created it. Setting a namespace of "app" renders
// app_signin_devices, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ Store = (*SQLStore)(nil)

// SQLStore keeps sign-in devices in a SQL table, against the schema
// devices/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam. It does one
// thing more than that interface describes — Sweep, the store's own machinery —
// and it does not belong on it: a store that is not a table has nothing to
// sweep.
type SQLStore struct {
	db    database.Client
	q     devicesdb.Querier
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter
}

// NewSQLStore builds a SQLStore over a database client.
//
// The client is not what the writes execute on. Record and DeleteForUser take
// the caller's database.Tx, and the reads take the caller's executor; what this
// one supplies is the dialect the generated statements are rendered for and the
// handle Sweep runs on, which serves the store's own machinery rather than a
// request.
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
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "sign-in device store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &SQLStore{
		db:    db,
		clock: o.clock,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical spelling
	// is internal/queries' and the separator is database/ddl's, so a namespaced
	// deployment cannot end up with two renderings of one name.
	if s.q, err = devicesdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the sign-in device querier")
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// querierDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for.
func querierDialect(d dialect.Dialect) (devicesdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return devicesdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return devicesdb.DialectMySQL, nil
	case dialect.SQLite:
		return devicesdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated sign-in device queries for dialect %q", d)
	}
}

// Record writes where a login was renewed from: a new row for a login's first
// mint, and the same row renewed by every one after it.
//
// Both stamps come from this store's clock. A renewal moves last_seen_at, the
// three origin values and expires_at, and leaves first_seen_at and the owner
// where the first mint put them. The origin values are bounded to MaxFieldLength
// here rather than by the hook, so a caller recording directly is bounded too.
//
// The row lands with tx, so a sign-in that rolls back records nothing.
func (s *SQLStore) Record(ctx context.Context, tx database.Tx, scope tenancy.Scope, sighting *Sighting) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return err
	}

	if sighting == nil {
		return ErrNilSighting
	}

	if sighting.FamilyID == "" {
		return ErrEmptyFamilyID
	}

	if sighting.UserID == "" {
		return ErrEmptyUserID
	}

	if sighting.ExpiresAt.IsZero() {
		return ErrZeroExpiry
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: sighting.UserID, familyIDKey: sighting.FamilyID})

	now := s.clock.Now().UTC()

	if err := s.q.UpsertSignInDevice(ctx, tx, devicesdb.UpsertSignInDeviceParams{
		Scope:       scope,
		FamilyID:    sighting.FamilyID,
		UserID:      sighting.UserID,
		IPAddress:   bound(sighting.Origin.IPAddress),
		UserAgent:   bound(sighting.Origin.UserAgent),
		DeviceName:  bound(sighting.Origin.DeviceName),
		FirstSeenAt: now,
		LastSeenAt:  now,
		ExpiresAt:   sighting.ExpiresAt.UTC(),
	}); err != nil {
		return op.Error(err, "recording a sign-in device")
	}

	return nil
}

// ListForFamilies answers with what was recorded for one person's logins, among
// familyIDs.
//
// The person is part of the predicate, not only the families: a family that is
// somebody else's is absent from the answer rather than read. An empty familyIDs
// is answered without a query — a set of no keys has an answer known before it
// is asked.
func (s *SQLStore) ListForFamilies(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
	familyIDs []string,
) ([]*Device, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return nil, err
	}

	if len(familyIDs) == 0 {
		return []*Device{}, nil
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	rows, err := s.q.ListSignInDevicesForFamilies(ctx, q, devicesdb.ListSignInDevicesForFamiliesParams{
		Scope:     scope,
		UserID:    userID,
		FamilyIDs: familyIDs,
	})
	if err != nil {
		return nil, op.Error(err, "reading the devices behind a person's sign-ins")
	}

	devices := make([]*Device, 0, len(rows))
	for i := range rows {
		devices = append(devices, &Device{
			Scope:       rows[i].Scope,
			FamilyID:    rows[i].FamilyID,
			UserID:      rows[i].UserID,
			IPAddress:   rows[i].IPAddress,
			UserAgent:   rows[i].UserAgent,
			DeviceName:  rows[i].DeviceName,
			FirstSeenAt: rows[i].FirstSeenAt.UTC(),
			LastSeenAt:  rows[i].LastSeenAt.UTC(),
			ExpiresAt:   rows[i].ExpiresAt.UTC(),
		})
	}

	return devices, nil
}

// ListForUser answers with every row one person has, oldest login first.
func (s *SQLStore) ListForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*Device, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	rows, err := s.q.ListSignInDevicesForUser(ctx, q, devicesdb.ListSignInDevicesForUserParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return nil, op.Error(err, "listing a person's sign-in devices")
	}

	devices := make([]*Device, 0, len(rows))
	for i := range rows {
		devices = append(devices, &Device{
			Scope:       rows[i].Scope,
			FamilyID:    rows[i].FamilyID,
			UserID:      rows[i].UserID,
			IPAddress:   rows[i].IPAddress,
			UserAgent:   rows[i].UserAgent,
			DeviceName:  rows[i].DeviceName,
			FirstSeenAt: rows[i].FirstSeenAt.UTC(),
			LastSeenAt:  rows[i].LastSeenAt.UTC(),
			ExpiresAt:   rows[i].ExpiresAt.UTC(),
		})
	}

	return devices, nil
}

// DeleteForUser removes every row one person has, and reports how many it
// removed.
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

	deleted, err := s.q.DeleteSignInDevicesForUser(ctx, tx, devicesdb.DeleteSignInDevicesForUserParams{
		Scope:  scope,
		UserID: userID,
	})
	if err != nil {
		return 0, op.Error(err, "deleting a person's sign-in devices")
	}

	op.SpanOnly(deletedKey, deleted)

	return deleted, nil
}

// DeleteForFamilies removes the rows of one person's logins among familyIDs,
// and reports how many it removed.
//
// It is what ending a login does to its row: without it the row of a login
// somebody signed out of would stay until the sweep reached its deadline, and a
// subject access request in between would export an address from a login that
// was already over. The person is part of the predicate for ListForFamilies'
// reason.
//
// It runs in tx, so the rows go with the revocation that ended their logins. It
// issues one statement per family; a revocation names one person's live logins,
// so the set is as small as the screen that lists them.
func (s *SQLStore) DeleteForFamilies(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	familyIDs []string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := validateOwner(scope, userID); err != nil {
		return 0, err
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), userIDKey: userID})

	var deleted int64

	for _, familyID := range familyIDs {
		removed, err := s.q.DeleteSignInDeviceForFamily(ctx, tx, devicesdb.DeleteSignInDeviceForFamilyParams{
			Scope:    scope,
			UserID:   userID,
			FamilyID: familyID,
		})
		if err != nil {
			return 0, op.Error(err, "deleting an ended sign-in's device")
		}

		deleted += removed
	}

	op.SpanOnly(deletedKey, deleted)

	return deleted, nil
}

// validateOwner is the argument check every method keyed on a person shares.
func validateOwner(scope tenancy.Scope, userID string) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if userID == "" {
		return ErrEmptyUserID
	}

	return nil
}
