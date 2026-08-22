package database

import (
	"context"
	"database/sql"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// scanRows drives a result set through scan, closing it afterwards. A close
// failure is surfaced only when nothing worse already went wrong, so the real
// cause is never masked by the cleanup.
//
// The wrap inside the defer is unreachable in practice and stays for the shape
// rather than the behavior: database/sql closes a result set itself once Next
// reports exhaustion, so by the time this Close runs the rows are closed and it
// returns nil. A driver-level close failure therefore arrives through rows.Err
// below, which is the path TestScanRows_CloseFailure actually exercises. Do not
// read that test as coverage of this branch.
func scanRows(rows *sql.Rows, scan func() error) (err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = platformerrors.Wrap(closeErr, "closing authorization rows")
		}
	}()

	for rows.Next() {
		if err = scan(); err != nil {
			return err
		}
	}

	return rows.Err()
}

// scanStrings runs a single-column query and collects the results.
func scanStrings(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	var out []string
	if err = scanRows(rows, func() error {
		var value string
		if scanErr := rows.Scan(&value); scanErr != nil {
			return scanErr
		}
		out = append(out, value)

		return nil
	}); err != nil {
		return nil, err
	}

	return out, nil
}
