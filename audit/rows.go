package audit

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
)

// The typed seam between the generated package and this one.
//
// audit/internal/auditdb is sqlc-gen-unison's output: one params and one row
// struct per statement, the same on all three dialects. These functions are the
// whole of what this package does with them — a row becomes an Entry, an Entry
// becomes the params — and every one is a struct literal or a conversion on
// purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; the projection-and-scan-targets pairing
// these replaced reported the same mistake as a runtime scan error, or worse,
// as two same-typed columns silently transposed.
//
// The row structs are nominal per statement, so a listing's row cannot convert
// to the get's where the columns differ — which is why the page converter
// restates its fields while the two reads that project exactly what the get
// projects are converted outright. The conversion is the assertion: the day
// those projections stop agreeing in field name, type or order, this stops
// building.

// storedEntry is one row as it was read: the decoded Entry, plus the encoded
// field blobs exactly as the database returned them.
//
// The raw bytes are kept rather than re-derived because verification hashes
// them directly. Re-encoding the decoded maps would put a JSON round trip
// between what was written and what is checked, and that round trip is lossy —
// see canonicalImage for the case that breaks.
type storedEntry struct {
	rawChanges  []byte
	rawMetadata []byte
	entry       Entry
}

// entryFromRow turns the single-entry read's row into a storedEntry.
//
// It is the one conversion, and every other row shape reaches it — by
// conversion where the projection is identical, by restatement where it is not
// — so what a row means is written down once.
//
// The blobs are decoded here and the decode can fail, unlike the encode: a
// column can be edited by hand, and an entry whose change set does not parse is
// one no caller should be handed as though it did.
func entryFromRow(r *auditdb.GetAuditLogEntryRow) (*storedEntry, error) {
	stored := &storedEntry{
		entry: Entry{
			// Read back as UTC unconditionally. Postgres hands back a time in
			// the session's zone, and the digest is taken over microseconds
			// since the epoch — which is zone-independent — but every
			// comparison and every value handed to a caller should still read
			// as UTC rather than as whatever the server was configured with.
			RecordedAt:   r.RecordedAt.UTC(),
			ID:           r.ID,
			Actor:        Actor{ID: r.ActorID, Type: ActorType(r.ActorType), IP: r.ActorIP},
			Scope:        r.Scope,
			ResourceType: r.ResourceType,
			ResourceID:   r.ResourceID,
			EventType:    EventType(r.EventType),
			PrevHash:     r.PrevHash,
			Hash:         r.Hash,
			Seq:          r.Seq,
		},
		rawChanges:  r.ChangeSet,
		rawMetadata: r.Metadata,
	}

	if len(r.ChangeSet) > 0 {
		if err := json.Unmarshal(r.ChangeSet, &stored.entry.Changes); err != nil {
			return nil, platformerrors.Wrapf(err, "decoding changes for audit entry %q", r.ID)
		}
	}

	if len(r.Metadata) > 0 {
		if err := json.Unmarshal(r.Metadata, &stored.entry.Metadata); err != nil {
			return nil, platformerrors.Wrapf(err, "decoding metadata for audit entry %q", r.ID)
		}
	}

	return stored, nil
}

// entryFromChainRow turns one row of a verification's walk into a storedEntry.
func entryFromChainRow(r *auditdb.ListAuditChainEntriesRow) (*storedEntry, error) {
	row := auditdb.GetAuditLogEntryRow(*r)

	return entryFromRow(&row)
}

// entryFromSeqRow turns the read keyed on a position into a storedEntry.
func entryFromSeqRow(r *auditdb.GetAuditLogEntryBySeqRow) (*storedEntry, error) {
	row := auditdb.GetAuditLogEntryRow(*r)

	return entryFromRow(&row)
}

// pageRow is one row of the paged read: the entry, and the two counts the
// statement carried alongside it.
type pageRow struct {
	value    *Entry
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts(row pageRow) (filtered, total int64) {
	return row.filtered, row.total
}

// pageValue reads the value off a row, for filtering.Drain.
func pageValue(row pageRow) *Entry { return row.value }

// entryPageRow restates one row of the paged read as the get's row and the two
// counts riding beside it.
//
// The restatement is the cost of the projections differing, and what it buys is
// the compiler checking every field name: a column that changes type breaks
// this line rather than the meaning of the entry it produces.
func entryPageRow(r *auditdb.ListAuditLogEntriesRow) (pageRow, error) {
	stored, err := entryFromRow(&auditdb.GetAuditLogEntryRow{
		ID:           r.ID,
		Seq:          r.Seq,
		Scope:        r.Scope,
		RecordedAt:   r.RecordedAt,
		EventType:    r.EventType,
		ResourceType: r.ResourceType,
		ResourceID:   r.ResourceID,
		ActorID:      r.ActorID,
		ActorType:    r.ActorType,
		ActorIP:      r.ActorIP,
		ChangeSet:    r.ChangeSet,
		Metadata:     r.Metadata,
		PrevHash:     r.PrevHash,
		Hash:         r.Hash,
	})
	if err != nil {
		return pageRow{}, err
	}

	return pageRow{value: &stored.entry, filtered: r.FilteredCount, total: r.TotalCount}, nil
}

// insertParams is one entry's worth of bound values, with the field blobs
// already encoded — the same bytes the digest was taken over.
func insertParams(entry *Entry, changes, metadata []byte) auditdb.InsertAuditLogEntryParams {
	return auditdb.InsertAuditLogEntryParams{
		ID:           entry.ID,
		Seq:          entry.Seq,
		Scope:        entry.Scope,
		RecordedAt:   entry.RecordedAt,
		EventType:    string(entry.EventType),
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceID,
		ActorID:      entry.Actor.ID,
		ActorType:    string(entry.Actor.Type),
		ActorIP:      entry.Actor.IP,
		ChangeSet:    changes,
		Metadata:     metadata,
		PrevHash:     entry.PrevHash,
		Hash:         entry.Hash,
	}
}

// convertRows maps a page of generated rows through a conversion that can fail.
// The conversion takes a pointer because a generated row is a wide struct and
// a page of them is what this is called on.
func convertRows[Row, T any](rows []Row, convert func(*Row) (T, error)) ([]T, error) {
	out := make([]T, 0, len(rows))

	for i := range rows {
		converted, err := convert(&rows[i])
		if err != nil {
			return nil, err
		}

		out = append(out, converted)
	}

	return out, nil
}

// sortedRows runs whichever of the paged read's two statements the filter's
// sort direction names, and hands back the ascending statement's rows either
// way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text,
// not a bound value, on all three engines. audit/internal/queries renders the
// pair and filtering.QueryFilter.SortsDescending picks between them.
//
// The descending rows are converted rather than restated field by field, and
// that is deliberate: they are one projection rendered twice, with the walk
// reversed and nothing else changed, so the conversion is the assertion. The
// day the two projections stop being identical in field name, type or order,
// this stops building rather than filling the wrong fields.
func sortedRows[Ascending, Descending any](
	filter *filtering.QueryFilter,
	ascending func() ([]Ascending, error),
	descending func() ([]Descending, error),
	same func(Descending) Ascending,
) ([]Ascending, error) {
	if !filter.SortsDescending() {
		return ascending()
	}

	rows, err := descending()
	if err != nil {
		return nil, err
	}

	page := make([]Ascending, 0, len(rows))
	for i := range rows {
		page = append(page, same(rows[i]))
	}

	return page, nil
}

// utcPtr normalizes an optional time bound, leaving an absent one absent.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// optional renders a selector a caller may leave unset: the empty string is
// "do not narrow", and every other value is the one the column must hold.
//
// The scope is the exception and does not come through here. Its absence is not
// an empty string but an absent [Query.Scope], because the identifier the empty
// string names — tenancy.Global() — is a value a row can hold and a narrowing a
// caller may ask for.
func optional(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}
