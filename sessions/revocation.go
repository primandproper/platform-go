package sessions

import (
	"context"
	stderrors "errors"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// validate reports whether this Holder names somebody.
//
// Both halves are required and neither substitutes for the other. An unset
// scope is tenancy.ErrNoScope — the call never said whose data it wanted — and
// an empty principal is ErrPrincipalRequired, because the empty principal is
// the anonymous session's and enumerating or revoking every anonymous session
// in a scope is not a question anybody asks on purpose.
func (h Holder) validate() error {
	if err := h.Scope.Validate(); err != nil {
		return err
	}

	if h.Principal == "" {
		return ErrPrincipalRequired
	}

	return nil
}

// List enumerates the live sessions a holder holds, newest first.
//
// The expiry filter is the same Policy the by-identifier read applies, run over
// the records the backend returned rather than pushed into its query. That is
// deliberate and it is the same decision the schema records: a session's
// deadlines are computed from its own two anchors against the store's clock, so
// a predicate on the row's stored deadline would be a second clock deciding
// which sessions a person is shown — and the two disagreeing means a session
// that is missing from the list and still answers requests.
//
// Nothing is written. An enumeration does not touch idle deadlines, or opening
// a security page would keep every session listed on it alive; and it does not
// delete the expired rows it filters out, which is the sweeper's job and not
// something a page render should be doing a variable amount of.
func (s *BackendStore[T]) List(ctx context.Context, holder Holder, currentID string) ([]*Session[T], error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationList, s.clock.Now())

	op.Set(scopeKey, holder.Scope.String()).Set(principalKey, holder.Principal)

	if err := holder.validate(); err != nil {
		return nil, op.Error(err, "listing sessions")
	}

	held, err := s.backend.ListHeld(ctx, holder)
	if err != nil {
		s.countBackendFailure(ctx, err)

		return nil, op.Error(err, "listing sessions")
	}

	now := s.now()

	sessions := make([]*Session[T], 0, len(held))
	for _, entry := range held {
		if entry == nil || entry.Record == nil {
			continue
		}

		if entry.Record.Version != recordVersion {
			s.staleRecordCounter.Add(ctx, 1)

			continue
		}

		if s.policy.Expiry(entry.Record.CreatedAt, entry.Record.LastSeenAt, now) != ExpiryNone {
			continue
		}

		session := s.session(entry.ID, entry.Record)
		session.IsCurrent = currentID != "" && entry.ID == currentID

		sessions = append(sessions, session)
	}

	op.Set(listedKey, len(sessions))

	return sessions, nil
}

// Revoke ends one of a holder's sessions.
//
// A count of zero is ErrNotFound rather than a distinct refusal, so a caller
// naming a session that is not theirs learns nothing about whether it exists.
// The alternative — a permission error — is a lookup oracle over every session
// identifier anybody cares to try, on the one endpoint whose whole purpose is
// to be reachable by an authenticated stranger.
func (s *BackendStore[T]) Revoke(ctx context.Context, holder Holder, id string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationRevoke, s.clock.Now())

	if id == "" {
		return op.Error(ErrIDRequired, "revoking session")
	}

	revoked, err := s.revoke(ctx, op, holder, revocationOne, func() (int, error) {
		return s.backend.DeleteHeld(ctx, holder, id)
	})
	if err != nil {
		return err
	}

	if revoked == 0 {
		return platformerrors.Wrap(ErrNotFound, "revoking session")
	}

	return nil
}

// RevokeAll ends every session a holder holds, the caller's own included.
func (s *BackendStore[T]) RevokeAll(ctx context.Context, holder Holder) (int, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationRevoke, s.clock.Now())

	return s.revoke(ctx, op, holder, revocationAll, func() (int, error) {
		return s.backend.DeleteAllHeld(ctx, holder, "")
	})
}

// RevokeAllExcept ends every session a holder holds but one.
//
// An empty keepID spares nothing, which makes this exactly RevokeAll. That is
// the honest reading rather than an error: the identifier a caller passes here
// is their own current session, and a caller that has none is signing out of
// everywhere.
func (s *BackendStore[T]) RevokeAllExcept(ctx context.Context, holder Holder, keepID string) (int, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationRevoke, s.clock.Now())

	return s.revoke(ctx, op, holder, revocationAllExcept, func() (int, error) {
		return s.backend.DeleteAllHeld(ctx, holder, keepID)
	})
}

// revoke runs one of the three revocations, with the holder check, the
// observability and the counter that are the same for all of them.
//
// The delete itself is a closure rather than a parameter set because the three
// differ only in which backend statement they are: what has to be identical is
// everything around it — that the holder is validated before anything is
// removed, that a backend failure is counted once, and that the number reported
// to the caller is the number counted.
//
// The reason is what separates them on sessions_revoked, which is the one place
// the difference is worth seeing: a deployment where "sign out everywhere"
// climbs is a deployment where something is scaring people.
func (s *BackendStore[T]) revoke(
	ctx context.Context,
	op observability.Operation,
	holder Holder,
	reason string,
	remove func() (int, error),
) (int, error) {
	op.Set(scopeKey, holder.Scope.String()).Set(principalKey, holder.Principal)

	if err := holder.validate(); err != nil {
		return 0, op.Error(err, "revoking sessions")
	}

	revoked, err := remove()
	if err != nil {
		s.countBackendFailure(ctx, err)

		return 0, op.Error(err, "revoking sessions")
	}

	if revoked > 0 {
		s.revokedCounter.Add(ctx, int64(revoked),
			metric.WithAttributes(attribute.String(reasonKey, reason)))
	}

	op.Set(revokedKey, revoked)

	return revoked, nil
}

// countBackendFailure counts a backend error unless it is the one that is not a
// failure.
//
// ErrNoPrincipalIndex says the backend never had the index, which is a wiring
// decision rather than a store that is unwell — counting it would put a
// constant on the dashboard that watches backend health, at whatever rate a
// consumer's security page is opened.
func (s *BackendStore[T]) countBackendFailure(ctx context.Context, err error) {
	if stderrors.Is(err, ErrNoPrincipalIndex) {
		return
	}

	s.backendErrorsCounter.Add(ctx, 1)
}
