package devices

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// browser is an origin with every field read, as a deployment behind a known
// edge would read it.
var browser = Origin{IPAddress: "203.0.113.7", UserAgent: "Mozilla/5.0", DeviceName: "Ada's laptop"}

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

		_, err := NewSQLStore(&Config{TablePrefix: "app_"}, newTestClient(t))
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("addresses the namespaced table", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTable(t, client, client.Dialect(), "app")

		h := newHarnessOn(t, client, &Config{TablePrefix: "app"})
		h.mustRecord(t, testScope(), h.sighting("family_namespaced", testUser, browser))

		test.EqOp(t, 1, rowsIn(t, client, "app_signin_devices"))
		test.EqOp(t, 0, rowsIn(t, client, "signin_devices"))
	})
}

// TestSQLStore is the behavior every dialect has to share. The container suites
// run it against Postgres and MySQL over one shared table, so every subtest keys
// on a user and families of its own and asserts on which rows it can read, never
// on how many the table holds.
//
//nolint:tparallel // the suite is sequential, deliberately: the container suites run it against one shared clock and table.
func TestSQLStore(t *testing.T) {
	t.Parallel()

	runStoreSuite(t, newHarness(t))
}

func runStoreSuite(t *testing.T, h *harness) {
	t.Helper()

	t.Run("records a login's first sighting from its own clock", func(t *testing.T) {
		const user = "user_first_sighting"

		sighting := h.sighting("family_first_sighting", user, browser)
		h.mustRecord(t, testScope(), sighting)

		recorded := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, recorded)

		device := recorded[0]
		test.EqOp(t, testScope(), device.Scope)
		test.EqOp(t, "family_first_sighting", device.FamilyID)
		test.EqOp(t, user, device.UserID)
		test.EqOp(t, browser.IPAddress, device.IPAddress)
		test.EqOp(t, browser.UserAgent, device.UserAgent)
		test.EqOp(t, browser.DeviceName, device.DeviceName)
		test.True(t, device.FirstSeenAt.Equal(h.clock.Now()), test.Sprintf("first seen %v", device.FirstSeenAt))
		test.True(t, device.LastSeenAt.Equal(h.clock.Now()), test.Sprintf("last seen %v", device.LastSeenAt))
		test.True(t, device.ExpiresAt.Equal(sighting.ExpiresAt), test.Sprintf("expires %v", device.ExpiresAt))
	})

	// A refresh is the same login renewed, so it lands on the same row and moves
	// what the request said, when, and how long the login now lives — and leaves
	// when the login was first seen.
	t.Run("renews a login's row rather than adding one", func(t *testing.T) {
		const (
			user   = "user_renewal"
			family = "family_renewal"
		)

		h.mustRecord(t, testScope(), h.sighting(family, user, browser))
		firstSeen := h.listForUser(t, testScope(), user)[0].FirstSeenAt

		renewedFrom := Origin{IPAddress: "198.51.100.4", UserAgent: "Mozilla/5.0 (iPhone)", DeviceName: "Ada's phone"}
		renewedAt := firstSeen.Add(time.Hour)
		renewal := &Sighting{FamilyID: family, UserID: user, Origin: renewedFrom, ExpiresAt: renewedAt.Add(testTTL)}

		h.clock.advance(time.Hour)
		h.mustRecord(t, testScope(), renewal)

		recorded := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, recorded)

		device := recorded[0]
		test.EqOp(t, renewedFrom.IPAddress, device.IPAddress)
		test.EqOp(t, renewedFrom.UserAgent, device.UserAgent)
		test.EqOp(t, renewedFrom.DeviceName, device.DeviceName)
		test.True(t, device.FirstSeenAt.Equal(firstSeen), test.Sprintf("first seen moved to %v", device.FirstSeenAt))
		test.True(t, device.LastSeenAt.Equal(renewedAt), test.Sprintf("last seen %v", device.LastSeenAt))
		test.True(t, device.ExpiresAt.Equal(renewal.ExpiresAt), test.Sprintf("expires %v", device.ExpiresAt))
	})

	// A renewal from somewhere the extractor could not read is a login whose
	// last-known device is unknown. Keeping the previous address would show the
	// screen a device that may no longer hold it.
	t.Run("an empty origin replaces what was recorded before", func(t *testing.T) {
		const (
			user   = "user_empty_origin"
			family = "family_empty_origin"
		)

		h.mustRecord(t, testScope(), h.sighting(family, user, browser))
		h.mustRecord(t, testScope(), h.sighting(family, user, Origin{}))

		recorded := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, recorded)
		test.MapEmpty(t, recorded[0].Attributes())
	})

	t.Run("bounds every value before it is stored", func(t *testing.T) {
		const user = "user_bounded"

		// A run of three-byte characters long enough that the cut falls inside
		// one, which is the case that turns a valid value invalid.
		long := strings.Repeat("€", MaxFieldLength)
		invalid := "Mozilla\xff/5.0"

		h.mustRecord(t, testScope(), h.sighting("family_bounded", user, Origin{
			IPAddress:  "  203.0.113.7  ",
			UserAgent:  invalid,
			DeviceName: long,
		}))

		recorded := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, recorded)

		device := recorded[0]
		test.EqOp(t, "203.0.113.7", device.IPAddress)
		test.EqOp(t, "Mozilla/5.0", device.UserAgent)
		test.LessEq(t, MaxFieldLength, len(device.DeviceName))
		test.EqOp(t, bound(long), device.DeviceName)
	})

	// The same family identifier under two scopes is two logins in two
	// directories, and neither scope's reads reach the other's.
	t.Run("keeps each scope's rows to itself", func(t *testing.T) {
		const (
			user   = "user_scoped"
			family = "family_scoped"
		)

		other := tenancy.Of("tenant_b")

		h.mustRecord(t, testScope(), h.sighting(family, user, browser))
		h.mustRecord(t, other, h.sighting(family, user, Origin{IPAddress: "192.0.2.1"}))

		mine := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, mine)
		test.EqOp(t, browser.IPAddress, mine[0].IPAddress)

		theirs := h.listForUser(t, other, user)
		must.SliceLen(t, 1, theirs)
		test.EqOp(t, "192.0.2.1", theirs[0].IPAddress)

		deleted, err := h.deleteForUser(t, other, user)
		must.NoError(t, err)
		test.EqOp(t, int64(1), deleted)

		test.SliceLen(t, 1, h.listForUser(t, testScope(), user))
	})

	t.Run("answers the families asked about, and only that person's", func(t *testing.T) {
		const (
			user     = "user_families"
			somebody = "user_families_other"
		)

		h.mustRecord(t, testScope(), h.sighting("family_families_a", user, browser))
		h.mustRecord(t, testScope(), h.sighting("family_families_b", user, browser))
		h.mustRecord(t, testScope(), h.sighting("family_families_c", user, browser))
		h.mustRecord(t, testScope(), h.sighting("family_families_theirs", somebody, browser))

		recorded, err := h.store.ListForFamilies(t.Context(), h.client.Reader(), testScope(), user,
			[]string{"family_families_a", "family_families_c", "family_families_theirs", "family_families_unknown"})
		must.NoError(t, err)

		families := make([]string, 0, len(recorded))
		for _, device := range recorded {
			families = append(families, device.FamilyID)
			test.EqOp(t, user, device.UserID)
		}

		test.SliceContainsAll(t, []string{"family_families_a", "family_families_c"}, families)
	})

	t.Run("lists a person's logins oldest first", func(t *testing.T) {
		const user = "user_ordered"

		h.mustRecord(t, testScope(), h.sighting("family_ordered_z", user, browser))
		h.clock.advance(time.Minute)
		h.mustRecord(t, testScope(), h.sighting("family_ordered_a", user, browser))

		recorded := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 2, recorded)
		test.EqOp(t, "family_ordered_z", recorded[0].FamilyID)
		test.EqOp(t, "family_ordered_a", recorded[1].FamilyID)
	})

	t.Run("deletes every row one person has, and nobody else's", func(t *testing.T) {
		const (
			user     = "user_erased"
			somebody = "user_erased_other"
		)

		h.mustRecord(t, testScope(), h.sighting("family_erased_a", user, browser))
		h.mustRecord(t, testScope(), h.sighting("family_erased_b", user, browser))
		h.mustRecord(t, testScope(), h.sighting("family_erased_theirs", somebody, browser))

		deleted, err := h.deleteForUser(t, testScope(), user)
		must.NoError(t, err)
		test.EqOp(t, int64(2), deleted)

		test.SliceEmpty(t, h.listForUser(t, testScope(), user))
		test.SliceLen(t, 1, h.listForUser(t, testScope(), somebody))

		deleted, err = h.deleteForUser(t, testScope(), user)
		must.NoError(t, err)
		test.EqOp(t, int64(0), deleted)
	})

	// A sighting that rolls back with the sign-in it belongs to records nothing,
	// which is what writing on the caller's transaction buys.
	t.Run("records nothing for a transaction that rolls back", func(t *testing.T) {
		const user = "user_rolled_back"

		err := h.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			must.NoError(t, h.store.Record(t.Context(), tx, testScope(), h.sighting("family_rolled_back", user, browser)))

			return context.Canceled
		})
		must.ErrorIs(t, err, context.Canceled)

		test.SliceEmpty(t, h.listForUser(t, testScope(), user))
	})
}

func TestSQLStore_Record_Refusals(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, tc := range map[string]struct {
		sighting *Sighting
		want     error
		scope    tenancy.Scope
	}{
		"an unset scope": {scope: tenancy.Scope{}, sighting: h.sighting("family", testUser, browser), want: tenancy.ErrNoScope},
		"no sighting":    {scope: testScope(), sighting: nil, want: ErrNilSighting},
		"no login":       {scope: testScope(), sighting: h.sighting("", testUser, browser), want: ErrEmptyFamilyID},
		"nobody":         {scope: testScope(), sighting: h.sighting("family", "", browser), want: ErrEmptyUserID},
		"no deadline":    {scope: testScope(), sighting: &Sighting{FamilyID: "family", UserID: testUser}, want: ErrZeroExpiry},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.ErrorIs(t, h.record(t, tc.scope, tc.sighting), tc.want)
		})
	}
}

func TestSQLStore_Reads_Refusals(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	T.Run("refuse an unset scope", func(t *testing.T) {
		t.Parallel()

		_, err := h.store.ListForUser(t.Context(), h.client.Reader(), tenancy.Scope{}, testUser)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = h.store.ListForFamilies(t.Context(), h.client.Reader(), tenancy.Scope{}, testUser, []string{"family"})
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = h.deleteForUser(t, tenancy.Scope{}, testUser)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("refuse nobody", func(t *testing.T) {
		t.Parallel()

		_, err := h.store.ListForUser(t.Context(), h.client.Reader(), testScope(), "")
		test.ErrorIs(t, err, ErrEmptyUserID)

		_, err = h.store.ListForFamilies(t.Context(), h.client.Reader(), testScope(), "", []string{"family"})
		test.ErrorIs(t, err, ErrEmptyUserID)

		_, err = h.deleteForUser(t, testScope(), "")
		test.ErrorIs(t, err, ErrEmptyUserID)
	})

	// A set of no keys has an answer known before it is asked. The executor is
	// nil to prove nothing was sent: a query on it would panic.
	T.Run("answer no families without a query", func(t *testing.T) {
		t.Parallel()

		recorded, err := h.store.ListForFamilies(t.Context(), nil, testScope(), testUser, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, recorded)
		test.NotNil(t, recorded)
	})
}

func TestSQLStore_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes the rows of logins that can no longer be alive, in every scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		h.mustRecord(t, testScope(), &Sighting{FamilyID: "family_soon", UserID: testUser, ExpiresAt: h.clock.Now().Add(time.Hour)})
		h.mustRecord(t, tenancy.Of("tenant_b"), &Sighting{FamilyID: "family_soon", UserID: testUser, ExpiresAt: h.clock.Now().Add(time.Hour)})
		h.mustRecord(t, testScope(), &Sighting{FamilyID: "family_later", UserID: testUser, ExpiresAt: h.clock.Now().Add(testTTL)})

		swept, err := h.store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		// The horizon is inclusive: a login whose deadline is now cannot be
		// refreshed now.
		h.clock.advance(time.Hour)

		swept, err = h.store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(2), swept)

		remaining := h.listForUser(t, testScope(), testUser)
		must.SliceLen(t, 1, remaining)
		test.EqOp(t, "family_later", remaining[0].FamilyID)
		test.SliceEmpty(t, h.listForUser(t, tenancy.Of("tenant_b"), testUser))
	})

	// The background loop's only effect is the sweep it runs, so the way to see
	// it ran is the row it collected.
	T.Run("the background loop sweeps on every tick", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		h := newHarness(t, WithSweeper(ctx, time.Minute))
		h.mustRecord(t, testScope(), h.sighting("family_ticked", testUser, browser))
		h.clock.advance(testTTL)

		// The tick is delivered synchronously, but the sweep it triggers runs on
		// the loop's goroutine; a second tick blocks until the first is drained,
		// which is what makes the first one's work observable.
		h.clock.tick()
		h.clock.tick()

		test.EqOp(t, 0, rowsIn(t, h.client, "signin_devices"))
	})

	// A sweep that fails is logged and nothing else: nothing is waiting on the
	// goroutine, and a table that grows for another interval is not a sign-in
	// that misbehaves.
	T.Run("a failing background sweep is logged rather than returned", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		logger := newRecordingLogger()

		h := newHarness(t, WithSweeper(ctx, time.Minute), WithLogger(logger))
		must.NoError(t, h.client.Close())

		h.clock.tick()
		h.clock.tick()

		test.Positive(t, logger.count(backgroundSweepFailure))
	})

	T.Run("starts nothing when it was not asked to", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, newTestClient(t), WithSweeper(nil, time.Minute), //nolint:staticcheck // the nil context is the case under test
			WithLogger(loggingnoop.NewLogger()), WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}
