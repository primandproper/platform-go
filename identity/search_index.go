package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
)

// ScanUsersForReindex implements [SearchIndexWriter].
func (s *SQLStore) ScanUsersForReindex(
	ctx context.Context,
	q database.SQLQueryExecutor,
	cursor string,
	limit uint8,
) ([]string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := requireExecutor(q); err != nil {
		return nil, op.Error(err, "scanning identity users for reindex")
	}

	rows, err := s.q.ScanUserIDsForReindex(ctx, q, identitydb.ScanUserIDsForReindexParams{
		ReindexCursor: cursor,
		ResultLimit:   int64(reindexLimit(limit)),
	})
	if err != nil {
		return nil, op.Error(err, "scanning identity users for reindex")
	}

	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}

	return ids, nil
}

// MarkUsersAsIndexed implements [SearchIndexWriter].
func (s *SQLStore) MarkUsersAsIndexed(
	ctx context.Context,
	tx database.Tx,
	ids []string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return 0, op.Error(err, "stamping indexed identity users")
	}

	// An empty flush is the ordinary state of a buffer nothing wrote to before
	// its interval elapsed, and is answered without a statement: UPDATE ... IN
	// () is not legal on every dialect, and a round trip that can only stamp
	// nothing is a round trip to make anyway.
	if len(ids) == 0 {
		return 0, nil
	}

	stamped, err := s.q.MarkUsersAsIndexed(ctx, tx, identitydb.MarkUsersAsIndexedParams{IDs: ids})
	if err != nil {
		return 0, op.Error(err, "stamping indexed identity users")
	}

	op.Set(indexStampedKey, stamped)

	return stamped, nil
}

// reindexLimit bounds a scan page.
//
// Zero asks for the default rather than for nothing, which is the reading every
// other paged read here takes of an unset size, and the ceiling is the one
// filtering already applies — a backstop walking the directory is the caller
// least served by a page it has to make several round trips to assemble, and
// the most able to bring the whole database down asking for one.
func reindexLimit(limit uint8) uint8 {
	if limit == 0 {
		return defaultReindexPageSize
	}

	return limit
}
