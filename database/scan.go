package database

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ScanAll runs a query and collects one value per row through scan.
//
// It exists because the loop around a *sql.Rows is four separate obligations
// and every store in this module had written all four by hand, eighteen times:
// close the rows whatever happens, surface a close failure only when nothing
// worse already went wrong, check rows.Err() after the loop rather than trusting
// Next's false, and return the scan error rather than the close error when both
// occur. Missing the third silently truncates a result set when the connection
// drops mid-read — the loop simply ends, and the caller gets a short list with
// no error to say so. Missing the second masks the real cause behind the
// cleanup's.
//
// subject names the rows in the close failure's message: "outbox id",
// "dataprivacy request". It is a noun phrase, not a sentence — " rows" is
// appended.
//
// The named error return is load-bearing. The deferred close writes to it, which
// is how a close failure on an otherwise-successful read still reaches the
// caller; a plain `return results, nil` would discard it.
func ScanAll[T any](
	ctx context.Context,
	q SQLQueryExecutor,
	subject, query string,
	args []any,
	scan func(Scanner) (T, error),
) (results []T, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = platformerrors.Wrapf(closeErr, "closing %s rows", subject)
		}
	}()

	for rows.Next() {
		result, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// ScanStrings is ScanAll for the single-column reads that collect identifiers,
// which is what most of them are.
func ScanStrings(ctx context.Context, q SQLQueryExecutor, subject, query string, args []any) ([]string, error) {
	return ScanAll(ctx, q, subject, query, args, func(scanner Scanner) (string, error) {
		var value string

		err := scanner.Scan(&value)

		return value, err
	})
}
