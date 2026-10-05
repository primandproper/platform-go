package database

import (
	"context"

	"github.com/primandproper/platform-go/v15/sessions"
	"github.com/primandproper/platform-go/v15/sessions/database/internal/sessionsdb"
)

// ListHeld returns every record the holder holds, newest first.
//
// It applies no expiry, deliberately: the deadline is decided one layer up from
// the record's own anchors against the store's clock, and a predicate on the
// stored expires_at here would be a second clock deciding which sessions a
// person is shown. A row this read returned and the store then filtered out is
// a row Get would have refused too, which is the property that matters.
//
// It is unpaged, because the set is a person's sessions and a "sign out
// everywhere" that acted on one page of them would not be one.
func (b *Backend[T]) ListHeld(ctx context.Context, holder sessions.Holder) ([]*sessions.Identified[T], error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	found, err := b.q.ListSessionsForPrincipal(ctx, b.db.Writer(), sessionsdb.ListSessionsForPrincipalParams{
		Scope:     holder.Scope,
		Principal: holder.Principal,
	})
	if err != nil {
		return nil, op.Error(err, "listing a principal's session rows")
	}

	held := make([]*sessions.Identified[T], 0, len(found))

	// Indexed rather than ranged by value: a row of this width is a copy per
	// iteration for no reason, and every field below is read rather than kept.
	for index := range found {
		row := &found[index]

		record := &sessions.Record[T]{
			CreatedAt:  row.CreatedAt.UTC(),
			LastSeenAt: row.LastSeenAt.UTC(),
			Holder:     holder,
			Metadata: sessions.Metadata{
				DeviceName:  row.DeviceName,
				IPAddress:   row.IPAddress,
				UserAgent:   row.UserAgent,
				LoginMethod: row.LoginMethod,
			},
			Version: int(row.Version),
		}

		if row.Data != nil {
			var value T
			if err = b.codec.Unmarshal(ctx, row.Data, &value); err != nil {
				// Dropped rather than surfaced, matching Load: an undecodable
				// payload is treated as an absent session, and failing the
				// whole enumeration over one of them would take away the page
				// the user needs in order to revoke it.
				op.Acknowledge(err, "decoding a listed session's payload")

				continue
			}

			record.Data = &value
		}

		held = append(held, &sessions.Identified[T]{Record: record, ID: row.ID})
	}

	return held, nil
}

// DeleteHeld removes one of the holder's sessions, reporting how many rows went.
//
// The holder is in the WHERE clause rather than checked first. A revocation
// that reads the row, decides it belongs to the caller, and then deletes it by
// identifier is a revocation whose two halves can be got out of step; here the
// server decides "this session, and it is theirs" at the instant the row goes,
// and the count is what it decided.
func (b *Backend[T]) DeleteHeld(ctx context.Context, holder sessions.Holder, id string) (int, error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	affected, err := b.q.DeleteSessionForPrincipal(ctx, b.db.Writer(), sessionsdb.DeleteSessionForPrincipalParams{
		ID:        id,
		Scope:     holder.Scope,
		Principal: holder.Principal,
	})
	if err != nil {
		return 0, op.Error(err, "revoking a session row")
	}

	return int(affected), nil
}

// DeleteAllHeld removes every session the holder holds, sparing keepID when it
// is not empty, and reports how many went.
//
// One statement, not a listed set of identifiers deleted afterwards. A session
// established between the list and the deletes is a session the sign-out missed
// — which is the interval an attacker who still holds a valid session uses to
// re-establish one.
func (b *Backend[T]) DeleteAllHeld(ctx context.Context, holder sessions.Holder, keepID string) (int, error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	params := sessionsdb.DeleteSessionsForPrincipalParams{
		Scope:     holder.Scope,
		Principal: holder.Principal,
	}

	// Left unset for the revocation that spares nothing, where the statement's
	// COALESCE then excludes an identifier no row holds. Binding the empty
	// string would mean the same thing only for as long as no identifier is
	// ever empty, which is a property of the mint rather than of this call.
	if keepID != "" {
		params.KeptSessionID = &keepID
	}

	affected, err := b.q.DeleteSessionsForPrincipal(ctx, b.db.Writer(), params)
	if err != nil {
		return 0, op.Error(err, "revoking a principal's session rows")
	}

	return int(affected), nil
}
