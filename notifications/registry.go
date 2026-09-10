package notifications

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications/internal/notificationsdb"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's Registry: what a push is addressed to.
var _ Registry = (*SQLStore)(nil)

// RegisterDevice records a device token through the caller's transaction and
// answers with the row that is there afterwards.
//
// The write converges on (platform, token) — see notifications/internal/queries
// — so a handset re-registering keeps the id and the creation time it already
// had, and the value the caller was holding names neither. That is why this
// reads back rather than answering with what it wrote: a caller that minted an
// id for a token already registered would otherwise be holding an id no row has,
// and would revoke nothing when the user signs out. The read-back runs on tx, so
// it is the row this transaction just converged on.
//
// The caller's Device is never written to. The id this may mint, the last-seen
// stamp it may take from the clock, and the identity the row turned out to have
// are all on the value returned; see [Registry].
func (s *SQLStore) RegisterDevice(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	device *Device,
) (*Device, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "registering device")
	}

	if device == nil {
		return nil, op.Error(ErrNilDevice, "registering device")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "registering device")
	}

	// A copy, for CreateNotification's reason: what this call settles belongs on
	// the row it returns rather than on a value the caller still holds.
	registered := *device

	if err := adoptScope(scope, &registered.Scope); err != nil {
		return nil, op.Error(err, "registering device")
	}

	op.Set(principalKey, registered.Principal).
		Set(platformKey, registered.Platform.String())

	if err := validDevice(&registered); err != nil {
		return nil, op.Error(err, "registering device")
	}

	if registered.ID == "" {
		registered.ID = identifiers.New()
	}

	if registered.LastSeenAt.IsZero() {
		registered.LastSeenAt = s.now()
	}

	if err := s.q.RegisterDevice(ctx, tx, registerDeviceParams(scope, &registered)); err != nil {
		return nil, op.Error(err, "registering device")
	}

	row, err := s.q.GetDeviceByToken(ctx, tx, notificationsdb.GetDeviceByTokenParams{
		Scope:    scope,
		Platform: registered.Platform.String(),
		Token:    registered.Token,
	})
	if err != nil {
		return nil, op.Error(notFound(err, ErrDeviceNotFound), "reading back the registered device")
	}

	stored := deviceFromRow(&row)

	op.Set(deviceIDKey, stored.ID)

	return stored, nil
}

// ListDevices pages the principal's registered devices.
func (s *SQLStore) ListDevices(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principal string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Device], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing devices")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing devices")
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "listing devices")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]notificationsdb.ListDevicesRow, error) {
			return s.q.ListDevices(ctx, q,
				listDevicesParams(scope, principal, filter))
		},
		func() ([]notificationsdb.ListDevicesDescendingRow, error) {
			return s.q.ListDevicesDescending(ctx, q,
				notificationsdb.ListDevicesDescendingParams(
					listDevicesParams(scope, principal, filter)))
		},
		func(r notificationsdb.ListDevicesDescendingRow) notificationsdb.ListDevicesRow {
			return notificationsdb.ListDevicesRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing devices")
	}

	rows := make([]pageRow[Device], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, devicePageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(d *Device) string { return d.ID }, filter), nil
}

// ListDevicesByPrincipals reads every device registered to any of the named
// principals, in one query.
func (s *SQLStore) ListDevicesByPrincipals(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principals []string,
) ([]*Device, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading devices by principal")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading devices by principal")
	}

	// An empty batch is an empty answer without a query: the statement the
	// corpus carries has no rendering of an empty set, and sending one anyway is
	// a round trip whose answer was known before it left — see
	// querygen.Generator.SetReadQuery, which documents the contract this keeps.
	if len(principals) == 0 {
		return []*Device{}, nil
	}

	rows, err := s.q.ListDevicesByPrincipals(ctx, q,
		notificationsdb.ListDevicesByPrincipalsParams{Scope: scope, Principals: principals})
	if err != nil {
		return nil, op.Error(err, "reading devices by principal")
	}

	devices := make([]*Device, 0, len(rows))
	for i := range rows {
		devices = append(devices, deviceFromSetRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(devices))

	return devices, nil
}

// RevokeDevice removes one of the principal's registrations through the
// caller's transaction, so the handset stops being addressable with whatever
// else a sign-out writes, and answers with the registration it removed.
//
// The read comes first, which is the one ordering a deletion allows: after the
// statement there is no row anywhere to describe what went, and a consumer
// recording which handset stopped being addressable has nowhere else to read it
// from. That is a read-then-write, and it is safe because the write that follows
// is guarded — it keys on the same scope, principal and id, and zero rows is
// ErrDeviceNotFound. A row that moved between the two is therefore a refusal
// rather than a value handed back for a deletion that did not happen. Both
// statements run on tx, so the row read is the row this transaction deletes.
func (s *SQLStore) RevokeDevice(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	principal, deviceID string,
) (*Device, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalKey, principal),
		observability.WithValue(deviceIDKey, deviceID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "revoking device %q", deviceID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "revoking device %q", deviceID)
	}

	if principal == "" {
		return nil, op.Error(ErrEmptyPrincipal, "revoking device %q", deviceID)
	}

	row, err := s.q.GetDevice(ctx, tx, notificationsdb.GetDeviceParams{
		ID:        deviceID,
		Scope:     scope,
		Principal: principal,
	})
	if err != nil {
		return nil, op.Error(notFound(err, ErrDeviceNotFound), "revoking device %q", deviceID)
	}

	count, err := s.q.RevokeDevice(ctx, tx, notificationsdb.RevokeDeviceParams{
		ID:        deviceID,
		Scope:     scope,
		Principal: principal,
	})
	if guardErr := guardCount(count, err, ErrDeviceNotFound, "revoking the device"); guardErr != nil {
		return nil, op.Error(guardErr, "revoking device %q", deviceID)
	}

	return deviceFromGetRow(&row), nil
}

// InvalidateDeviceToken removes a token the provider has permanently rejected.
//
// It is unscoped, it takes no executor, and it is idempotent — see
// [Registry.InvalidateDeviceToken], which carries the reasoning for all three.
// It is the one write here that runs on the client this store was built with,
// because it is the one write with no consumer request behind it: the caller is
// a send path acting on a provider's verdict, mid network round trip, with no
// transaction of anybody's to join. The platform is normalized and checked
// rather than passed through, because the string arrives from a sender rather
// than from this package: an unrecognized one would delete nothing and report
// success, which is exactly the silence this hook exists to end.
func (s *SQLStore) InvalidateDeviceToken(ctx context.Context, platform, token string) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(platformKey, platform))
	defer op.End()

	p, ok := ParsePlatform(platform)
	if !ok {
		return op.Error(ErrUnknownPlatform, "invalidating device token")
	}

	if token == "" {
		return op.Error(ErrEmptyToken, "invalidating device token")
	}

	count, err := s.q.DeleteDeviceToken(ctx, s.client.Writer(),
		notificationsdb.DeleteDeviceTokenParams{Platform: p.String(), Token: token})
	if err != nil {
		return op.Error(err, "invalidating device token")
	}

	s.invalidatedTokensCounter.Add(ctx, count)
	op.SpanOnly(countKey, count)

	return nil
}

// validDevice is what the registry requires of a registration before it stores
// one.
//
// The platform is checked against the set this package serves rather than stored
// as given, and that is the check worth having: a token filed under a platform
// no sender routes to is a row nothing will ever push to and nothing will ever
// prune, because the feedback that prunes rows comes from the provider that
// rejected them.
//
// The scope is not among these. It is checked on the argument before the device
// is consulted at all — see adoptScope — because it is the write's rather than
// the row's.
func validDevice(d *Device) error {
	if d.Principal == "" {
		return ErrEmptyPrincipal
	}

	if !d.Platform.Valid() {
		return ErrUnknownPlatform
	}

	if d.Token == "" {
		return ErrEmptyToken
	}

	return nil
}
