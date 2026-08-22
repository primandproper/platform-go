package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// storedEntry is one row as it was read: the decoded Entry, plus the encoded
// field blobs exactly as the database returned them.
//
// The raw bytes are kept rather than re-derived because verification hashes
// them directly. Re-encoding the decoded maps would put a JSON round trip
// between what was written and what is checked, and that round trip is lossy —
// see canonicalImage for the case that breaks.
type storedEntry struct {
	entry       Entry
	rawChanges  []byte
	rawMetadata []byte
}

// scanEntries reads a projection of entryColumns into storedEntry values.
func scanEntries(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]storedEntry, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, platformerrors.Wrap(err, "querying audit entries")
	}

	var out []storedEntry

	if err = scanRows(rows, func() error {
		stored, scanErr := scanEntry(rows)
		if scanErr != nil {
			return scanErr
		}
		out = append(out, *stored)

		return nil
	}); err != nil {
		return nil, platformerrors.Wrap(err, "scanning audit entries")
	}

	return out, nil
}

// scanEntry reads one row of entryColumns.
func scanEntry(s database.Scanner) (*storedEntry, error) {
	// Every column but the two blobs is NOT NULL in the shipped DDL, so they
	// scan straight into strings. The blobs are nullable and scan to nil, which
	// is what an entry with no changes or no metadata was written as.
	var (
		stored     storedEntry
		eventType  string
		actorType  string
		recordedAt time.Time
		changes    []byte
		metadata   []byte
	)

	if err := s.Scan(
		&stored.entry.ID,
		&stored.entry.Seq,
		&stored.entry.Scope,
		&recordedAt,
		&eventType,
		&stored.entry.ResourceType,
		&stored.entry.ResourceID,
		&stored.entry.Actor.ID,
		&actorType,
		&stored.entry.Actor.IP,
		&changes,
		&metadata,
		&stored.entry.PrevHash,
		&stored.entry.Hash,
	); err != nil {
		return nil, err
	}

	// Read back as UTC unconditionally. Postgres hands back a time in the
	// session's zone, and the digest is taken over microseconds since the epoch
	// — which is zone-independent — but every comparison and every value handed
	// to a caller should still read as UTC rather than as whatever the server
	// was configured with.
	stored.entry.RecordedAt = recordedAt.UTC()
	stored.entry.EventType = EventType(eventType)
	stored.entry.Actor.Type = ActorType(actorType)
	stored.rawChanges = changes
	stored.rawMetadata = metadata

	if len(changes) > 0 {
		if err := json.Unmarshal(changes, &stored.entry.Changes); err != nil {
			return nil, platformerrors.Wrapf(err, "decoding changes for audit entry %q", stored.entry.ID)
		}
	}

	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &stored.entry.Metadata); err != nil {
			return nil, platformerrors.Wrapf(err, "decoding metadata for audit entry %q", stored.entry.ID)
		}
	}

	return &stored, nil
}

// scanRows drives a result set through fn, closing it and reporting the first
// of a scan error, an iteration error, or a close error.
func scanRows(rows database.ResultIterator, fn func() error) (err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	for rows.Next() {
		if err = fn(); err != nil {
			return err
		}
	}

	return rows.Err()
}
