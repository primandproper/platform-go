package comments

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments/internal/commentsdb"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// GetComment reads one of the scope's live comments, on the caller's executor —
// so a caller inside a transaction reads the comment that transaction has
// written and not yet committed.
func (s *SQLStore) GetComment(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	commentID string,
) (*Comment, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(commentIDKey, commentID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading comment %q", commentID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading comment %q", commentID)
	}

	row, err := s.q.GetComment(ctx, q,
		commentsdb.GetCommentParams{ID: commentID, Scope: scope})
	if err != nil {
		return nil, op.Error(notFound(err, ErrCommentNotFound), "reading comment %q", commentID)
	}

	return commentFromRow(&row), nil
}

// ListRootComments pages the top level of one target's discussion.
func (s *SQLStore) ListRootComments(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	target Target,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Comment], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(targetTypeKey, target.Type.String()),
		observability.WithValue(targetIDKey, target.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing a target's root comments")
	}

	page, err := s.listLevel(ctx, op, q, scope, target, RootParentID, filter)
	if err != nil {
		return nil, op.Error(err, "listing a target's root comments")
	}

	return page, nil
}

// ListReplies pages one root comment's replies.
func (s *SQLStore) ListReplies(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	target Target,
	parentID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Comment], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(targetTypeKey, target.Type.String()),
		observability.WithValue(targetIDKey, target.ID),
		observability.WithValue(parentIDKey, parentID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing a comment's replies")
	}

	// Refused rather than answered with the roots, which is what the empty
	// parent selects in the column. A client that lost the parent id would
	// otherwise be handed the top of the discussion as though it were one
	// comment's replies, and nothing about those rows would say so.
	if parentID == RootParentID {
		return nil, op.Error(ErrEmptyParent, "listing a comment's replies")
	}

	page, err := s.listLevel(ctx, op, q, scope, target, parentID, filter)
	if err != nil {
		return nil, op.Error(err, "listing a comment's replies")
	}

	return page, nil
}

// listLevel is one level of one target's discussion, and it is where the two
// public reads meet.
//
// They are two methods because they are two questions, and one statement because
// they are the same question with a different parent: the roots are the comments
// whose parent is the empty string. Rendering them as two statements would have
// been two plans and two projections free to drift; rendering them as one method
// with a parent argument the caller leaves empty would have made "the roots" the
// thing that happens when a caller forgets something.
func (s *SQLStore) listLevel(
	ctx context.Context,
	op observability.Operation,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	target Target,
	parentID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Comment], error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}

	if err := target.Validate(); err != nil {
		return nil, err
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]commentsdb.ListCommentsRow, error) {
			return s.q.ListComments(ctx, q,
				listCommentsParams(scope, target, parentID, filter))
		},
		func() ([]commentsdb.ListCommentsDescendingRow, error) {
			return s.q.ListCommentsDescending(ctx, q,
				commentsdb.ListCommentsDescendingParams(listCommentsParams(scope, target, parentID, filter)))
		},
		func(r commentsdb.ListCommentsDescendingRow) commentsdb.ListCommentsRow {
			return commentsdb.ListCommentsRow(r)
		})
	if err != nil {
		return nil, err
	}

	return listPage(op, rows, filter), nil
}

// ListCommentsByTargetType pages every comment about one kind of thing.
func (s *SQLStore) ListCommentsByTargetType(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	targetType TargetType,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Comment], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(targetTypeKey, targetType.String()),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing comments by target type")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing comments by target type")
	}

	// The shape check, not the catalog check. This is the read an operator runs
	// to see what withdrawing a target type would strand, so a type the catalog
	// no longer holds is exactly the one it has to answer for.
	if targetType == "" {
		return nil, op.Error(ErrEmptyTargetType, "listing comments by target type")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]commentsdb.ListCommentsByTargetTypeRow, error) {
			return s.q.ListCommentsByTargetType(ctx, q,
				listByTargetTypeParams(scope, targetType, filter))
		},
		func() ([]commentsdb.ListCommentsByTargetTypeDescendingRow, error) {
			return s.q.ListCommentsByTargetTypeDescending(ctx, q,
				commentsdb.ListCommentsByTargetTypeDescendingParams(
					listByTargetTypeParams(scope, targetType, filter)))
		},
		func(r commentsdb.ListCommentsByTargetTypeDescendingRow) commentsdb.ListCommentsByTargetTypeRow {
			return commentsdb.ListCommentsByTargetTypeRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing comments by target type")
	}

	return listPage(op, convert(rows, func(r commentsdb.ListCommentsByTargetTypeRow) commentsdb.ListCommentsRow {
		return commentsdb.ListCommentsRow(r)
	}), filter), nil
}

// ListCommentsByAuthor pages what one person wrote within the scope.
func (s *SQLStore) ListCommentsByAuthor(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	author string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Comment], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(authorKey, author),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing comments by author")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing comments by author")
	}

	if author == "" {
		return nil, op.Error(ErrEmptyAuthor, "listing comments by author")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]commentsdb.ListCommentsByAuthorRow, error) {
			return s.q.ListCommentsByAuthor(ctx, q,
				listByAuthorParams(scope, author, filter))
		},
		func() ([]commentsdb.ListCommentsByAuthorDescendingRow, error) {
			return s.q.ListCommentsByAuthorDescending(ctx, q,
				commentsdb.ListCommentsByAuthorDescendingParams(
					listByAuthorParams(scope, author, filter)))
		},
		func(r commentsdb.ListCommentsByAuthorDescendingRow) commentsdb.ListCommentsByAuthorRow {
			return commentsdb.ListCommentsByAuthorRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing comments by author")
	}

	return listPage(op, convert(rows, func(r commentsdb.ListCommentsByAuthorRow) commentsdb.ListCommentsRow {
		return commentsdb.ListCommentsRow(r)
	}), filter), nil
}

// listPage is the one place a page of comments becomes a result.
//
// The cursor is the id, because every list statement orders by it. A cursor
// naming a position in an order the query does not use is a page that skips rows
// and repeats others, with nothing reporting an error — so the three lists share
// this rather than each naming the field they page by.
func listPage(
	op observability.Operation,
	listRows []commentsdb.ListCommentsRow,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Comment] {
	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, commentPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(c *Comment) string { return c.ID }, filter)
}
