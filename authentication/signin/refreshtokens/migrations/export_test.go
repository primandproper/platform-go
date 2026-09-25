package migrations

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
)

// StatementsThrough renders the sequence from version 1 up to and including
// version, which is a database as an earlier release left it. It is a test's
// alone: a consumer never builds a table at a version this package has moved
// past, and exporting it would be offering them the means to.
func StatementsThrough(d dialect.Dialect, prefix string, version uint64) ([]string, error) {
	var out []string

	for i := range sequence {
		if sequence[i].Version > version {
			break
		}

		stmts, err := sequence[i].Schema.Statements(d, prefix)
		if err != nil {
			return nil, err
		}

		out = append(out, stmts...)
	}

	return out, nil
}
