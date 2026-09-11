package waitlists

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/waitlists/internal/waitlistsdb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's SignupStore: the queue, written on the request path and worked
// through by whoever is running the launch.
var _ SignupStore = (*SQLStore)(nil)

// Join adds somebody to a list, through the caller's transaction — so the signup
// and whatever the caller writes beside it commit together or not at all. See
// [Store].
//
// The list read, the suppression check and the insert all run on tx, which is
// what makes them one decision. Without that a signup could be written against a
// list that closed between the check and the write, or past a withdrawal that
// landed in the same instant — and the second of those is the obligation this
// package exists to keep. It is also what lets a launch be opened and seeded in
// one transaction: the list read finds a list this transaction has written and
// not yet committed.
//
// The suppression check is a read rather than a reliance on the unique index,
// for the reason settings' name check is: a constraint violation reaches a
// caller as a driver error naming an index, which they cannot tell apart from
// the database being unwell and cannot show to a person. The index is still what
// makes it true under a concurrent write.
func (s *SQLStore) Join(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID string,
	signup *Signup,
) (*Signup, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "joining waitlist %q", listID)
	}

	if signup == nil {
		return nil, op.Error(ErrNilSignup, "joining waitlist %q", listID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	if err := matchScope(scope, signup.Scope, "waitlist signup"); err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	if err := validateContact(signup.Contact); err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	if err := signup.Subject.Validate(); err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	// The status and the stamps are the store's. A caller that set them was
	// describing a signup that has already happened, and honoring it would put
	// somebody straight into a status the lifecycle guards exist to move them
	// through.
	joined := *signup
	joined.Scope = scope
	joined.ListID = listID
	joined.ContactDigest = s.Digest(signup.Contact)
	joined.Status = StatusWaiting
	joined.StatusChangedAt = nil
	joined.LastUpdatedAt = nil
	joined.ArchivedAt = nil

	if joined.ID == "" {
		joined.ID = identifiers.New()
	}

	op.Set(signupKey, joined.ID)

	list, err := s.readList(ctx, tx, scope, listID)
	if err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	if !list.OpenAt(s.clock.Now()) {
		return nil, op.Error(
			platformerrors.Wrapf(ErrListClosed, "waitlist %q closed at %s", listID, list.ClosesAt),
			"joining waitlist %q", listID)
	}

	if err = s.refuseTakenContact(ctx, tx, scope, listID, joined.ContactDigest); err != nil {
		return nil, op.Error(err, "joining waitlist %q", listID)
	}

	if err = s.q.InsertSignup(ctx, tx, insertSignupParams(&joined, scope)); err != nil {
		return nil, op.Error(err, "writing waitlist signup")
	}

	row, err := s.q.GetSignupCreatedAt(ctx, tx, waitlistsdb.GetSignupCreatedAtParams{ID: joined.ID})
	if err != nil {
		return nil, op.Error(err, "reading back the waitlist signup's creation time")
	}

	joined.CreatedAt = row.CreatedAt.UTC()

	s.countSignups(ctx, StatusWaiting, 1)

	return &joined, nil
}

// GetSignup reads one live signup by id, on the list it belongs to, through the
// caller's executor.
func (s *SQLStore) GetSignup(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading waitlist signup %q", signupID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading waitlist signup %q", signupID)
	}

	signup, err := s.readSignup(ctx, q, scope, listID, signupID)
	if err != nil {
		return nil, op.Error(err, "reading waitlist signup %q", signupID)
	}

	return signup, nil
}

// GetSignupByContact reads one live signup by the address it was made with,
// through the caller's executor.
//
// The statement behind it sees archived rows, because it is the same statement
// Join's suppression check runs — see refuseTakenContact. An archived signup is
// not a live one, so this reports ErrSignupNotFound for it; a withdrawn signup
// is live and comes back, which is what lets an unsubscribe page say "you are
// already off this list" rather than "we have never heard of you".
func (s *SQLStore) GetSignupByContact(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	listID, contact string,
) (*Signup, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading waitlist signup by contact")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading waitlist signup by contact")
	}

	if err := validateContact(contact); err != nil {
		return nil, op.Error(err, "reading waitlist signup by contact")
	}

	signup, err := s.readSignupByDigest(ctx, q, scope, listID, s.Digest(contact))
	if err != nil {
		return nil, op.Error(err, "reading waitlist signup by contact")
	}

	if signup.ArchivedAt != nil {
		return nil, op.Error(ErrSignupNotFound, "reading waitlist signup by contact")
	}

	return signup, nil
}

// ListSignups pages one list's live signups, through the caller's executor.
func (s *SQLStore) ListSignups(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	listID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Signup], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing waitlist signups")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing waitlist signups")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]waitlistsdb.ListSignupsRow, error) {
			return s.q.ListSignups(ctx, q, listSignupsParams(scope, listID, filter))
		},
		func() ([]waitlistsdb.ListSignupsDescendingRow, error) {
			return s.q.ListSignupsDescending(ctx, q,
				waitlistsdb.ListSignupsDescendingParams(listSignupsParams(scope, listID, filter)))
		},
		func(r waitlistsdb.ListSignupsDescendingRow) waitlistsdb.ListSignupsRow {
			return waitlistsdb.ListSignupsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing waitlist signups")
	}

	rows := make([]pageRow[Signup], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, signupPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(sg *Signup) string { return sg.ID }, filter), nil
}

// ListSignupsForSubject pages the signups belonging to one principal across
// every list in the scope, archived ones too where the filter asks for them,
// through the caller's executor.
func (s *SQLStore) ListSignupsForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Signup], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, string(subject.Type)),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing a subject's waitlist signups")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing a subject's waitlist signups")
	}

	if err := requireSubject(subject); err != nil {
		return nil, op.Error(err, "listing a subject's waitlist signups")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]waitlistsdb.ListSignupsForSubjectRow, error) {
			return s.q.ListSignupsForSubject(ctx, q,
				listSignupsForSubjectParams(scope, subject, filter))
		},
		func() ([]waitlistsdb.ListSignupsForSubjectDescendingRow, error) {
			return s.q.ListSignupsForSubjectDescending(ctx, q,
				waitlistsdb.ListSignupsForSubjectDescendingParams(
					listSignupsForSubjectParams(scope, subject, filter)))
		},
		func(r waitlistsdb.ListSignupsForSubjectDescendingRow) waitlistsdb.ListSignupsForSubjectRow {
			return waitlistsdb.ListSignupsForSubjectRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing a subject's waitlist signups")
	}

	rows := make([]pageRow[Signup], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, subjectSignupPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(sg *Signup) string { return sg.ID }, filter), nil
}

// UpdateSignupNotes rewrites the operator's note against a signup, through the
// caller's transaction. See [Store].
func (s *SQLStore) UpdateSignupNotes(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID, notes string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "updating waitlist signup %q", signupID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating waitlist signup %q", signupID)
	}

	if err := requireID(signupID); err != nil {
		return op.Error(err, "updating waitlist signup %q", signupID)
	}

	count, err := s.q.UpdateSignupNotes(ctx, tx, waitlistsdb.UpdateSignupNotesParams{
		Notes:      notes,
		ID:         signupID,
		Scope:      scope,
		WaitlistID: listID,
	})

	return op.Error(guardCount(count, err, ErrSignupNotFound, "updating waitlist signup"),
		"updating waitlist signup %q", signupID)
}

// Invite moves a waiting signup to invited, through the caller's transaction —
// so the invitation and the record of who sent it land together or not at all.
// See [Store].
func (s *SQLStore) Invite(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
		observability.WithValue(statusKey, string(StatusInvited)),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "moving waitlist signup %q to %s", signupID, StatusInvited)
	}

	return op.Error(s.transition(ctx, tx, scope, listID, signupID, StatusWaiting, StatusInvited),
		"moving waitlist signup %q to %s", signupID, StatusInvited)
}

// Convert moves an invited signup to converted, through the caller's transaction
// — which is the ordinary one, since what somebody converted into is a row of
// the caller's written in the same transaction. See [Store].
func (s *SQLStore) Convert(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
		observability.WithValue(statusKey, string(StatusConverted)),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "moving waitlist signup %q to %s", signupID, StatusConverted)
	}

	return op.Error(s.transition(ctx, tx, scope, listID, signupID, StatusInvited, StatusConverted),
		"moving waitlist signup %q to %s", signupID, StatusConverted)
}

// transition is the guarded lifecycle move both Invite and Convert are, on the
// transaction the caller is holding.
//
// The guard is in the statement rather than in a read before it, which is what
// makes the move happen once: two requests inviting the same signup both find it
// waiting, and only one of their updates reports a row. Deciding on the read
// leaves a window as wide as whatever the caller does next, which for an
// invitation is an email.
//
// The refusal it reports is not certain about which of two things went wrong —
// the row is gone, or it is in another status — because a statement that matched
// nothing cannot tell them apart. So the guard's own answer is ErrWrongStatus
// and the ambiguity is resolved by one extra read, made only on the losing path,
// where a round trip costs nothing anybody is waiting on.
func (s *SQLStore) transition(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID string,
	from, to Status,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if err := requireID(signupID); err != nil {
		return err
	}

	count, err := s.q.TransitionSignup(ctx, tx,
		transitionSignupParams(scope, listID, signupID, from, to, s.clock.Now()))
	if err != nil {
		return platformerrors.Wrap(err, "moving waitlist signup")
	}

	if count == 0 {
		return s.explainLostTransition(ctx, tx, scope, listID, signupID, from)
	}

	s.countSignups(ctx, to, 1)

	return nil
}

// Withdraw takes somebody off the list at their own request, through the
// caller's transaction. See [Store].
func (s *SQLStore) Withdraw(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "withdrawing waitlist signup %q", signupID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "withdrawing waitlist signup %q", signupID)
	}

	if err := requireID(signupID); err != nil {
		return op.Error(err, "withdrawing waitlist signup %q", signupID)
	}

	count, err := s.q.WithdrawSignup(ctx, tx, withdrawSignupParams(scope, listID, signupID, s.clock.Now()))
	if err != nil {
		return op.Error(platformerrors.Wrap(err, "withdrawing waitlist signup"),
			"withdrawing waitlist signup %q", signupID)
	}

	if count == 0 {
		return op.Error(s.explainLostTransition(ctx, tx, scope, listID, signupID, StatusWithdrawn),
			"withdrawing waitlist signup %q", signupID)
	}

	s.countSignups(ctx, StatusWithdrawn, 1)

	return nil
}

// WithdrawSignupsForSubject withdraws every signup one principal holds in the
// scope, archived signups included, and reports how many that was.
//
// It is one statement rather than a page of reads and a withdrawal per row, and
// the difference is what makes it an erasure. A read outside the caller's
// transaction sees the table as it was committed, so a signup landing between
// the read and the writes would survive; and the single-row withdrawal cannot
// reach an archived signup, which still holds the address it was made with. The
// statement keys on the subject and nothing else, so both rows are its.
//
// Zero is not an error. An erasure runs against whatever the subject actually
// left behind, and a person who never joined a list is a person with nothing
// here to erase — reporting that as a failure would fail an erasure that
// succeeded.
func (s *SQLStore) WithdrawSignupsForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject Subject,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, string(subject.Type)),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "erasing a subject's waitlist signups")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "erasing a subject's waitlist signups")
	}

	if err := requireSubject(subject); err != nil {
		return 0, op.Error(err, "erasing a subject's waitlist signups")
	}

	withdrawn, err := s.q.WithdrawSignupsForSubject(ctx, tx,
		withdrawSignupsForSubjectParams(scope, subject, s.clock.Now()))
	if err != nil {
		return 0, op.Error(platformerrors.Wrap(err, "withdrawing waitlist signups"),
			"erasing a subject's waitlist signups")
	}

	op.Set(countKey, withdrawn)

	if withdrawn > 0 {
		s.countSignups(ctx, StatusWithdrawn, withdrawn)
	}

	return withdrawn, nil
}

// ArchiveSignup retires a signup administratively, through the caller's
// transaction. See [Store].
func (s *SQLStore) ArchiveSignup(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(listKey, listID),
		observability.WithValue(signupKey, signupID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving waitlist signup %q", signupID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving waitlist signup %q", signupID)
	}

	if err := requireID(signupID); err != nil {
		return op.Error(err, "archiving waitlist signup %q", signupID)
	}

	count, err := s.q.ArchiveSignup(ctx, tx, waitlistsdb.ArchiveSignupParams{
		ID:         signupID,
		Scope:      scope,
		WaitlistID: listID,
	})

	return op.Error(guardCount(count, err, ErrSignupNotFound, "archiving waitlist signup"),
		"archiving waitlist signup %q", signupID)
}

// explainLostTransition says why a guarded write matched nothing.
//
// It is one read on the losing path, and it exists because the count alone
// cannot tell "the signup is gone" from "the signup is somewhere else in its
// lifecycle", and those lead a caller to two different places: a broken link,
// or a person who has already been invited. The expected status is the one the
// write required — the one it required the row to hold, or, for a withdrawal, the one
// it required the row not to hold, which is why the withdrawn case is answered
// first.
//
// The read goes through the transaction the write ran on, which is the only
// executor that can see a row this transaction wrote — and on a one-connection
// SQLite client, the only one that would not be waiting on itself for the
// connection to read with.
func (s *SQLStore) explainLostTransition(
	ctx context.Context,
	q waitlistsdb.DBTX,
	scope tenancy.Scope,
	listID, signupID string,
	expected Status,
) error {
	signup, err := s.readSignup(ctx, q, scope, listID, signupID)
	if err != nil {
		return err
	}

	if expected == StatusWithdrawn && signup.Withdrawn() {
		return ErrAlreadyWithdrawn
	}

	return platformerrors.Wrapf(ErrWrongStatus, "waitlist signup %q is %s", signupID, signup.Status)
}

// readSignup is the read by id, through whatever executor the caller is
// holding. It is the read GetSignup makes on the executor it was handed and the
// one a lost transition makes on the transaction it lost on.
func (s *SQLStore) readSignup(
	ctx context.Context,
	q waitlistsdb.DBTX,
	scope tenancy.Scope,
	listID, signupID string,
) (*Signup, error) {
	if err := requireID(signupID); err != nil {
		return nil, err
	}

	row, err := s.q.GetSignup(ctx, q, waitlistsdb.GetSignupParams{
		ID:         signupID,
		Scope:      scope,
		WaitlistID: listID,
	})
	if err != nil {
		return nil, notFound(err, ErrSignupNotFound)
	}

	return signupFromRow(&row), nil
}

// refuseTakenContact reports why a contact cannot join this list, having found a
// row for its digest.
//
// The read sees archived rows, because the uniqueness it stands in for does —
// see waitlists/internal/queries. Which of the two refusals it is decides what
// the caller shows somebody: ErrContactWithdrawn is a person who asked to be
// left alone and must be, and ErrAlreadySignedUp is a person who is already in
// the queue.
func (s *SQLStore) refuseTakenContact(
	ctx context.Context,
	q waitlistsdb.DBTX,
	scope tenancy.Scope,
	listID, digest string,
) error {
	signup, err := s.readSignupByDigest(ctx, q, scope, listID, digest)

	switch {
	case errors.Is(err, ErrSignupNotFound):
		return nil
	case err != nil:
		return err
	case signup.Withdrawn():
		return ErrContactWithdrawn
	default:
		return ErrAlreadySignedUp
	}
}

// readSignupByDigest is the read keyed on a contact's digest, through whatever
// executor the caller is holding. It sees archived rows; the callers decide what
// one means.
func (s *SQLStore) readSignupByDigest(
	ctx context.Context,
	q waitlistsdb.DBTX,
	scope tenancy.Scope,
	listID, digest string,
) (*Signup, error) {
	row, err := s.q.GetSignupByContactDigest(ctx, q, waitlistsdb.GetSignupByContactDigestParams{
		Scope:         scope,
		WaitlistID:    listID,
		ContactDigest: digest,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSignupNotFound
		}

		return nil, platformerrors.Wrap(err, "reading waitlist signup by contact digest")
	}

	return signupFromDigestRow(&row), nil
}

// requireSubject is what the two subject-keyed methods require of theirs: a
// whole subject, naming somebody.
//
// The anonymous subject is refused rather than answered. Both columns default
// to the empty string, so a statement bound to nobody would reach every signup
// nobody claimed — which for a read is the widest possible answer to a question
// about one person, and for the erasure is a withdrawal of everybody on every
// list who did not have an account.
func requireSubject(subject Subject) error {
	if subject.Anonymous() {
		return ErrEmptySubjectType
	}

	return subject.Validate()
}
