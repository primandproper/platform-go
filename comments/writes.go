package comments

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v14/comments/internal/commentsdb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// CreateComment writes one comment through the caller's transaction and reads
// back the creation time the database assigned.
//
// The read-back is a second round trip on a write path, and it is worth it:
// created_at is database-owned — see comments/internal/queries — so the insert
// does not carry it, and the alternative is a value whose CreatedAt says
// 0001-01-01 for a row written a moment ago. A service that serializes what it
// just created straight into a response would render that as a date rather than
// as an absence.
//
// Every check runs on tx, so a reply whose parent was written earlier in the
// same transaction resolves its parent instead of reporting it absent. The
// catalog's existence hook is the one that does not, because it takes no
// executor and has none of this to run on. See [Store.CreateComment].
func (s *SQLStore) CreateComment(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	comment *Comment,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "writing comment")
	}

	if comment == nil {
		return op.Error(ErrNilComment, "writing comment")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "writing comment")
	}

	if err := adoptScope(scope, comment); err != nil {
		return op.Error(err, "writing comment")
	}

	op.Set(authorKey, comment.Author)

	if err := validAuthorAndBody(comment); err != nil {
		return op.Error(err, "writing comment")
	}

	// The parent first, because a reply's target is its parent's and the catalog
	// check below is made against whatever this settles on.
	if err := s.adoptParent(ctx, tx, scope, comment); err != nil {
		return op.Error(err, "writing comment")
	}

	op.Set(targetTypeKey, comment.Target.Type.String()).
		Set(targetIDKey, comment.Target.ID).
		Set(parentIDKey, comment.ParentID)

	if err := s.checkTarget(ctx, scope, comment.Target); err != nil {
		return op.Error(err, "writing comment")
	}

	if comment.ID == "" {
		comment.ID = identifiers.New()
	}

	op.Set(commentIDKey, comment.ID)

	if err := s.q.CreateComment(ctx, tx, createCommentParams(scope, comment)); err != nil {
		return op.Error(err, "writing comment")
	}

	created, err := s.q.GetCommentCreatedAt(ctx, tx,
		commentsdb.GetCommentCreatedAtParams{ID: comment.ID, Scope: scope})
	if err != nil {
		return op.Error(err, "reading back the comment's creation time")
	}

	comment.CreatedAt = created.CreatedAt.UTC()

	// Nothing has edited or archived a comment that was written a moment ago,
	// and a caller who filled either in is a caller describing a row that does
	// not exist yet.
	comment.LastUpdatedAt = nil
	comment.ArchivedAt = nil

	return nil
}

// adoptScope settles which tenant a write is for, and writes the answer back
// onto the comment.
//
// The scope the call named is the one the statement binds, so a comment that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's comment and writing it into another, which is a
// stale value or a mix-up and is not a thing to guess at. A comment that names
// none adopts the argument — the same reading adoptParent takes of a reply that
// named no target. tenancy.Scope tells the zero value apart from Global(), so
// "unset" here is genuinely unset rather than the global scope spelled shortly.
func adoptScope(scope tenancy.Scope, comment *Comment) error {
	if comment.Scope != (tenancy.Scope{}) && comment.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"comment names %q, the write names %q", comment.Scope, scope)
	}

	comment.Scope = scope

	return nil
}

// adoptParent settles what a reply is a reply to, and what it is about.
//
// A root has no parent and nothing to settle. A reply has three things checked,
// and each of them is a row that would otherwise be written and then be
// unreachable: a parent that is not in this scope is a conversation the reply
// would never appear in; a parent that is itself a reply is a depth this
// package's reads cannot walk; and a target that disagrees with the parent's is a
// comment that shows up under something nobody said it about.
//
// The read goes to the executor the write is running on rather than to whatever
// GetComment would reach. A reply written immediately after its parent is the
// ordinary case in a discussion, and a read replica still holding the moment
// before would report the parent absent — as would the writer, on the
// transactional path, for a parent this caller has written and not yet
// committed.
func (s *SQLStore) adoptParent(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	comment *Comment,
) error {
	// A root is about whatever it says it is about, and checkTarget is where
	// that is vetted. There is nothing to settle here.
	if comment.Root() {
		return nil
	}

	row, err := s.q.GetComment(ctx, q,
		commentsdb.GetCommentParams{ID: comment.ParentID, Scope: scope})
	if err != nil {
		return notFound(err, platformerrors.Wrapf(ErrParentNotFound, "comment %q", comment.ParentID))
	}

	parent := commentFromRow(&row)

	if !parent.Root() {
		return platformerrors.Wrapf(ErrNestedReply, "comment %q replies to %q", comment.ParentID, parent.ParentID)
	}

	// A reply that named no target adopts its parent's, which is the ordinary
	// case for a client that has a comment id and a text box. One that named a
	// different target is refused rather than corrected: a caller who spelled a
	// target out has a target in mind, and quietly storing another one is how a
	// comment ends up under a thing nobody said it about.
	if comment.Target.Zero() {
		comment.Target = parent.Target

		return nil
	}

	if err = comment.Target.Validate(); err != nil {
		return err
	}

	if comment.Target != parent.Target {
		return platformerrors.Wrapf(ErrTargetMismatch,
			"reply names %q/%q, parent %q is on %q/%q",
			comment.Target.Type, comment.Target.ID,
			parent.ID, parent.Target.Type, parent.Target.ID)
	}

	return nil
}

// checkTarget is the catalog gate, and the existence check the catalog optionally
// carries.
//
// The two answers are different and are kept different. A type nobody registered
// is a bug in the caller — a misspelling, or a target kind this deployment does
// not have — and a target the consumer's own check cannot find is a stale client
// or a thing that was deleted while somebody was typing. Only the second is
// counted, because only the second is a number: the first is a build-time
// mistake arriving at runtime and there is nothing to watch.
func (s *SQLStore) checkTarget(ctx context.Context, scope tenancy.Scope, target Target) error {
	if err := target.Validate(); err != nil {
		return err
	}

	definition, known := s.targets[target.Type]
	if !known {
		return platformerrors.Wrapf(ErrUnknownTargetType, "comment target type %q", target.Type)
	}

	if definition.Exists == nil {
		return nil
	}

	// An error is not "absent". A hook that could not reach its table fails the
	// write rather than deciding the target is gone: those two answers lead to
	// opposite actions, and only one of them is recoverable by trying again.
	exists, err := definition.Exists(ctx, scope, target.ID)
	if err != nil {
		return platformerrors.Wrapf(err, "checking that %s %q exists", target.Type, target.ID)
	}

	if !exists {
		s.countAbsentTarget(ctx, target.Type)

		return platformerrors.Wrapf(ErrTargetNotFound, "%s %q", target.Type, target.ID)
	}

	return nil
}

// UpdateComment revises what the author said, through the caller's transaction,
// so the revision and whatever the caller records about it commit together or
// not at all. See [Store.UpdateComment].
func (s *SQLStore) UpdateComment(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	comment *Comment,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "editing comment")
	}

	if comment == nil {
		return op.Error(ErrNilComment, "editing comment")
	}

	op.Set(commentIDKey, comment.ID)

	if err := scope.Validate(); err != nil {
		return op.Error(err, "editing comment %q", comment.ID)
	}

	if err := adoptScope(scope, comment); err != nil {
		return op.Error(err, "editing comment %q", comment.ID)
	}

	// The body alone, because the body is all the statement assigns. Checking the
	// target here would be checking a value this write cannot store, and refusing
	// a comment whose target type has since been withdrawn would mean its author
	// could no longer fix a typo in it.
	if strings.TrimSpace(comment.Body) == "" {
		return op.Error(ErrEmptyBody, "editing comment %q", comment.ID)
	}

	count, err := s.q.UpdateComment(ctx, tx, updateCommentParams(scope, comment))

	return op.Error(
		guardCount(count, err, ErrCommentNotFound, "editing the comment"),
		"editing comment %q", comment.ID)
}

// ArchiveComment removes one comment from the discussion, through the caller's
// transaction, so the removal and whatever the caller records about it commit
// together or not at all. See [Store.ArchiveComment].
//
// Zero rows is ErrCommentNotFound rather than a quiet success, and the reading is
// exact: the statement excludes archived rows, so a comment that has already
// been archived is not in the discussion, which is what this method addresses.
func (s *SQLStore) ArchiveComment(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	commentID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(commentIDKey, commentID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving comment %q", commentID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving comment %q", commentID)
	}

	count, err := s.q.ArchiveComment(ctx, tx,
		commentsdb.ArchiveCommentParams{ID: commentID, Scope: scope})

	return op.Error(
		guardCount(count, err, ErrCommentNotFound, "archiving the comment"),
		"archiving comment %q", commentID)
}

// DeleteCommentsForTarget destroys every comment about one thing and reports how
// many that was.
//
// Zero is not an error. The sweep runs against whatever the target actually
// collected, and a thing nobody commented on is a thing with nothing here to
// remove — reporting that as a failure would fail a delete that succeeded.
func (s *SQLStore) DeleteCommentsForTarget(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	target Target,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(targetTypeKey, target.Type.String()),
		observability.WithValue(targetIDKey, target.ID),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "sweeping a target's comments")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "sweeping a target's comments")
	}

	// The shape check, not the catalog check. A sweep is what a consumer runs
	// when a target is going away, and a target type on its way out of the
	// catalog is exactly the one whose rows most need reaching.
	if err := target.Validate(); err != nil {
		return 0, op.Error(err, "sweeping a target's comments")
	}

	deleted, err := s.q.DeleteCommentsForTarget(ctx, tx, commentsdb.DeleteCommentsForTargetParams{
		Scope:      scope,
		TargetType: target.Type.String(),
		TargetID:   target.ID,
	})
	if err != nil {
		return 0, op.Error(err, "sweeping a target's comments")
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// DeleteCommentsByAuthor destroys everything one person wrote within the scope
// and reports how many that was.
//
// Zero is not an error. An erasure runs against whatever the subject actually
// left behind, and a person who never commented is a person with nothing here to
// erase — reporting that as a failure would fail an erasure that succeeded.
func (s *SQLStore) DeleteCommentsByAuthor(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	author string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(authorKey, author),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "erasing comments")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "erasing comments")
	}

	if author == "" {
		return 0, op.Error(ErrEmptyAuthor, "erasing comments")
	}

	deleted, err := s.q.DeleteCommentsByAuthor(ctx, tx,
		commentsdb.DeleteCommentsByAuthorParams{Scope: scope, Author: author})
	if err != nil {
		return 0, op.Error(err, "erasing comments")
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// validAuthorAndBody is what the store requires of a comment's two free-text
// halves before it writes one.
//
// Each check refuses a row that would be unreachable rather than merely odd: a
// comment written by nobody is one no author's list can find and no erasure can
// reach, and one with no body records that somebody pressed a button.
func validAuthorAndBody(c *Comment) error {
	if strings.TrimSpace(c.Author) == "" {
		return ErrEmptyAuthor
	}

	if strings.TrimSpace(c.Body) == "" {
		return ErrEmptyBody
	}

	return nil
}
