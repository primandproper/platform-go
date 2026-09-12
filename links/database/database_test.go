package database

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, err := New(&Config{}, newTestClient(t))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("takes no locker", func(t *testing.T) {
		t.Parallel()

		// The claim links makes about needing no lock service, asserted where
		// it could stop being true: this constructor's argument list.
		store, err := New(&Config{}, newTestClient(t))
		must.NoError(t, err)

		var _ links.Store = store
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := New(nil, newTestClient(t))
		test.ErrorIs(t, err, ErrNilConfig)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := New(&Config{}, nil)
		test.ErrorIs(t, err, ErrNilClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("rejects a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		_, err := New(&Config{TablePrefix: "trailing_"}, newTestClient(t))
		test.Error(t, err)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		_, err := New(&Config{}, newTestClient(t), nil)
		test.NoError(t, err)
	})
}

func TestStore_Put(T *testing.T) {
	T.Parallel()

	T.Run("stores a record readable by its digest", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		record, err := store.Get(t.Context(), testID)
		must.NoError(t, err)

		test.EqOp(t, testAction, record.Action)
		test.EqOp(t, testSubject, record.Subject)
		test.EqOp(t, links.StateActive, record.State)
		test.EqOp(t, links.RecordVersion, record.Version)
		test.True(t, record.CreatedAt.Equal(mintedAt), test.Sprintf("read %v", record.CreatedAt))
		test.True(t, record.ExpiresAt.Equal(mintedAt.Add(time.Hour)))
		test.True(t, record.PurgeAfter.Equal(mintedAt.Add(2*time.Hour)))
		test.Nil(t, record.ResolvedAt)
	})

	T.Run("round-trips metadata", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		record := activeRecord()
		record.Metadata = map[string]string{"next": "/dashboard", "campaign": "june"}
		put(t, store, testID, record)

		read, err := store.Get(t.Context(), testID)
		must.NoError(t, err)

		test.EqOp(t, "/dashboard", read.Metadata["next"])
		test.EqOp(t, "june", read.Metadata["campaign"])
	})

	T.Run("a link with no metadata reads back with none", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		record, err := store.Get(t.Context(), testID)
		must.NoError(t, err)
		test.MapLen(t, 0, record.Metadata)
	})

	// Put is the mint write, and the NULL it leaves in resolved_at is the
	// guard every later resolution matches on. The record's field is now the
	// same type as the column's parameter, so passing it through would compile
	// — and would let a caller mint a link the store considers already spent.
	T.Run("writes no resolution stamp for a record that arrived carrying one", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		record := activeRecord()
		resolved := mintedAt.Add(time.Minute)
		record.ResolvedAt = &resolved

		put(t, store, testID, record)

		stored, err := store.Get(t.Context(), testID)
		must.NoError(t, err)
		test.Nil(t, stored.ResolvedAt)
	})

	T.Run("refuses a second row under one digest", func(t *testing.T) {
		t.Parallel()

		// The primary key is the digest, so a collision means the generator
		// repeated itself. Replacing the row instead would hand the second
		// caller a URL that redeems the first caller's link.
		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		test.Error(t, store.Put(t.Context(), testID, activeRecord()))
	})
}

func TestStore_Get(T *testing.T) {
	T.Parallel()

	T.Run("reports an absent link as not found", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := store.Get(t.Context(), testID)
		test.ErrorIs(t, err, links.ErrLinkNotFound)
	})

	T.Run("reports a row written by another version as stale", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		record := activeRecord()
		record.Version = links.RecordVersion + 1
		put(t, store, testID, record)

		_, err := store.Get(t.Context(), testID)
		test.ErrorIs(t, err, links.ErrStaleRecord)
		test.False(t, stderrors.Is(err, links.ErrLinkNotFound))
	})

	T.Run("does not consume the link", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		_, err := store.Get(t.Context(), testID)
		must.NoError(t, err)

		at := mintedAt.Add(time.Minute)

		_, err = store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.NoError(t, err)
	})
}

func TestStore_Resolve(T *testing.T) {
	T.Parallel()

	T.Run("transitions an active link", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		record, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		must.NoError(t, err)

		test.EqOp(t, links.StateRedeemed, record.State)
		resolvedAt(t, record, at)

		stored, err := store.Get(t.Context(), testID)
		must.NoError(t, err)
		test.EqOp(t, links.StateRedeemed, stored.State)
		resolvedAt(t, stored, at)
		test.True(t, stored.PurgeAfter.Equal(at.Add(time.Hour)))
	})

	T.Run("refuses a second transition and names the first", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		must.NoError(t, err)

		record, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkAlreadyRedeemed)
		must.NotNil(t, record)
		test.EqOp(t, testAction, record.Action)
	})

	T.Run("tells a redemption that the link was revoked", func(t *testing.T) {
		t.Parallel()

		// The two are different sentences for whoever is holding the token,
		// which is why the guard's zero count is answered by a re-read rather
		// than by one canned error.
		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		_, err := store.Resolve(t.Context(), testID, links.StateRevoked, at, at.Add(time.Hour))
		must.NoError(t, err)

		_, err = store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkRevoked)
	})

	T.Run("refuses a link past its expiry without writing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(2 * time.Hour)

		record, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkExpired)
		must.NotNil(t, record)

		// The refusal rolled the transaction back, so the row is untouched and
		// a later Inspect still says why rather than saying "already used".
		stored, err := store.Get(t.Context(), testID)
		must.NoError(t, err)
		test.EqOp(t, links.StateActive, stored.State)
		test.Nil(t, stored.ResolvedAt)
	})

	T.Run("refuses at the instant the link expires", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		// The boundary belongs to the dead side: ExpiresAt is when it stops
		// working, not the last moment it works.
		at := mintedAt.Add(time.Hour)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkExpired)
	})

	T.Run("reports an absent link", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, mintedAt, mintedAt.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkNotFound)
	})

	T.Run("reports a row written by another version as stale", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		record := activeRecord()
		record.Version = links.RecordVersion + 1
		put(t, store, testID, record)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, mintedAt, mintedAt.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrStaleRecord)
	})

	T.Run("a state the schema does not name refuses rather than being honored", func(t *testing.T) {
		t.Parallel()

		// A row whose state column holds something this binary does not know
		// is a link whose meaning was written by something else. Refusing is
		// the only safe reading of it.
		store, _ := newTestStore(t)

		record := activeRecord()
		record.State = links.State(99)
		put(t, store, testID, record)

		_, err := store.Get(t.Context(), testID)
		must.NoError(t, err)

		_, err = store.Resolve(t.Context(), testID, links.StateRedeemed, mintedAt, mintedAt.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkNotFound)
	})
}

// TestStore_RevokeForSubject covers the capability links.Store does not carry.
//
// The statement is one UPDATE keyed on the subject and guarded on the
// resolution stamp, so every case here is about which rows it reaches: the
// subject's live ones and nothing else, whatever tenant they were minted for
// and whatever action they were minted under.
func TestStore_RevokeForSubject(T *testing.T) {
	T.Parallel()

	T.Run("withdraws every live link for the subject and counts them", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		for _, id := range []links.ID{"live_one", "live_two", "live_three"} {
			put(t, store, id, activeRecord())
		}

		at := mintedAt.Add(time.Minute)

		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(3), revoked)

		for _, id := range []links.ID{"live_one", "live_two", "live_three"} {
			stored, getErr := store.Get(t.Context(), id)
			must.NoError(t, getErr, must.Sprintf("id %q", id))

			test.EqOp(t, links.StateRevoked, stored.State, test.Sprintf("id %q", id))
			resolvedAt(t, stored, at, must.Sprintf("id %q", id))
			test.True(t, stored.PurgeAfter.Equal(at.Add(time.Hour)), test.Sprintf("id %q", id))
		}
	})

	T.Run("leaves another subject's links alone", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		theirs := activeRecord()
		theirs.Subject = "user_456"
		put(t, store, "theirs", theirs)

		at := mintedAt.Add(time.Minute)

		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		stored, err := store.Get(t.Context(), "theirs")
		must.NoError(t, err)
		test.EqOp(t, links.StateActive, stored.State)
		test.Nil(t, stored.ResolvedAt)
	})

	// There is no scope argument and no scope column, so a person's links are
	// withdrawn wherever they were minted. An application that records a tenant
	// against a link records it in metadata, and metadata is not a predicate —
	// which is the point: revoking somebody's links should cross their tenants
	// rather than stop inside one.
	T.Run("crosses whatever tenants the subject belongs to", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		for _, tenant := range []string{"tenant_a", "tenant_b", "tenant_c"} {
			record := activeRecord()
			record.Metadata = map[string]string{"tenant": tenant}
			put(t, store, links.ID(tenant), record)
		}

		at := mintedAt.Add(time.Minute)

		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(3), revoked)
	})

	// The guard is the resolution's own, so a row somebody already spent is a
	// row this write does not reach — and the sentence a second click gets stays
	// "already redeemed" rather than becoming "revoked".
	T.Run("does not reach a link that was already resolved", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		must.NoError(t, err)

		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		stored, err := store.Get(t.Context(), testID)
		must.NoError(t, err)
		test.EqOp(t, links.StateRedeemed, stored.State)
	})

	// Documented rather than incidental. Nothing in links decides liveness in
	// SQL, so this statement matches on the resolution stamp alone: a link that
	// expired without ever being resolved is moved too, and afterwards answers
	// "revoked" rather than "expired".
	T.Run("withdraws a link that expired without being resolved", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		// Past the record's own hour, which nothing in this statement consults.
		at := mintedAt.Add(3 * time.Hour)

		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkRevoked)
	})

	// A revocation naming nobody would match the rows of a subject the caller
	// never meant, so it is refused rather than answered with a count.
	T.Run("refuses an empty subject", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		revoked, err := store.RevokeForSubject(t.Context(), "", at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrEmptySubject)
		test.EqOp(t, int64(0), revoked)

		stored, getErr := store.Get(t.Context(), testID)
		must.NoError(t, getErr)
		test.EqOp(t, links.StateActive, stored.State)
	})

	// The statement is not keyed by a primary key, so it is the one place a
	// prefix mistake would reach another application's rows rather than simply
	// finding nothing.
	T.Run("a namespaced store does not reach the plain table", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTable(t, client, dialect.SQLite, "ddb")

		plain, err := New(&Config{}, client)
		must.NoError(t, err)

		namespaced, err := New(&Config{TablePrefix: "ddb"}, client)
		must.NoError(t, err)

		put(t, plain, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		revoked, err := namespaced.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		stored, err := plain.Get(t.Context(), testID)
		must.NoError(t, err)
		test.EqOp(t, links.StateActive, stored.State)
	})
}

func TestStore_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes only what is past its purge deadline", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		put(t, store, testID, activeRecord())

		long := activeRecord()
		long.PurgeAfter = mintedAt.Add(48 * time.Hour)
		put(t, store, "0000000000000000", long)

		c.advance(3 * time.Hour)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)

		_, err = store.Get(t.Context(), testID)
		test.ErrorIs(t, err, links.ErrLinkNotFound)

		_, err = store.Get(t.Context(), "0000000000000000")
		test.NoError(t, err)
	})

	T.Run("collects a resolved link at its purge deadline rather than at its resolution", func(t *testing.T) {
		t.Parallel()

		// The whole retention policy: a spent link keeps answering "already
		// used" for exactly as long as the Minter said it should.
		store, c := newTestStore(t)

		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)

		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		must.NoError(t, err)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		_, err = store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, err, links.ErrLinkAlreadyRedeemed)

		c.advance(2 * time.Hour)

		swept, err = store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)
	})

	T.Run("sweeps nothing when nothing is collectable", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		put(t, store, testID, activeRecord())

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})
}

func TestStore_sweepEvery(T *testing.T) {
	T.Parallel()

	T.Run("sweeps on every tick until its context is done", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute))

		put(t, store, testID, activeRecord())
		c.advance(3 * time.Hour)

		c.tick()

		// The tick is delivered synchronously, but the sweep it triggers runs
		// on the loop's goroutine; a second tick blocks until the first is
		// drained, which is what makes the first one's work observable.
		c.tick()

		_, err := store.Get(t.Context(), testID)
		test.ErrorIs(t, err, links.ErrLinkNotFound)
	})

	// The loop's only effect on a failure is the line it logs, so a loop that
	// stopped logging would fail silently.
	T.Run("logs a failing sweep and keeps going", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		logger := newRecordingLogger()

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute), WithLogger(logger))
		must.NoError(t, store.db.Close())

		c.tick()
		c.tick()

		test.Positive(t, logger.count(backgroundSweepFailure))
	})

	T.Run("starts nothing when it was not asked to", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // the nil context is the case under test
		store, err := New(&Config{}, newTestClient(t), WithSweeper(nil, time.Minute))
		must.NoError(t, err)
		must.NotNil(t, store)

		store, err = New(&Config{}, newTestClient(t), WithSweeper(t.Context(), 0))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

func TestStore_TablePrefix(T *testing.T) {
	T.Parallel()

	T.Run("a namespaced store addresses its own table", func(t *testing.T) {
		t.Parallel()

		// A prefix is not decoration: it renders a second table, and both the
		// DDL and every statement have to agree about which one they mean.
		client := newTestClient(t)
		createTable(t, client, dialect.SQLite, "ddb")

		plain, err := New(&Config{}, client)
		must.NoError(t, err)

		namespaced, err := New(&Config{TablePrefix: "ddb"}, client)
		must.NoError(t, err)

		put(t, namespaced, testID, activeRecord())

		test.EqOp(t, 1, rowsIn(t, client, "ddb_action_links"))
		test.EqOp(t, 0, rowsIn(t, client, "action_links"))

		_, err = plain.Get(t.Context(), testID)
		test.ErrorIs(t, err, links.ErrLinkNotFound)

		_, err = namespaced.Get(t.Context(), testID)
		test.NoError(t, err)
	})
}
