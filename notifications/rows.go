package notifications

import (
	"time"

	"github.com/primandproper/platform-go/v14/notifications/internal/notificationsdb"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// notifications/internal/notificationsdb is sqlc-gen-unison's output: one params
// and one row struct per statement, the same on all three dialects. These
// functions are the whole of what this package does with them — a row becomes
// the domain type, a domain value becomes the params — and every one is a struct
// literal on purpose. A renamed or retyped column changes the generated struct,
// and every conversion here stops compiling; a scan-by-position pairing would
// report the same mistake as a runtime scan error, or worse, as two same-typed
// columns silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree — which is why the page converters
// restate the fields rather than casting.

// utcPtr normalizes an optional timestamp to UTC, preserving absence. It is the
// one home for the rule, and every conversion below goes through it.
//
// Every timestamp this package writes is UTC, so every one it returns is too —
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the string parsed as, so a caller comparing two of those, or
// rendering one into JSON, would get an answer that depends on where the row was
// read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero.
type pageRow[T any] struct {
	value    *T
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts[T any](row pageRow[T]) (filtered, total int64) {
	return row.filtered, row.total
}

// pageValue reads the value off a row, for filtering.Drain.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text, not
// a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the three paged reads. A read that
// reached for the ascending statement while holding a descending filter would
// answer in the order the client did not ask for, and nothing about the rows
// that came back would say so.
//
// The descending rows are converted rather than restated field by field, which
// is the one place in this file that casts. The preamble's rule is about two
// projections that happen to agree; these are one projection rendered twice,
// with the walk reversed and nothing else changed. So the conversion is the
// assertion, and Go makes it the compiler's: the day the two projections stop
// being identical, in field name, type or order, this stops building rather than
// filling the wrong fields.
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

// listWindow is the filter window every generated list statement binds. One
// reading of the filter, restated into each nominal params type by the
// constructors below.
//
// The UTC normalization on the four times is load-bearing on SQLite, not
// cosmetic: that column compares as text, the stored shape is UTC
// `YYYY-MM-DD HH:MM:SS`, and the driver renders a bound time.Time with its own
// zone's clock in exactly that prefix position — so a UTC value compares
// correctly to the second and a zoned one is off by its offset, silently.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here, and
// it defaults to excluding, which is what the statement's COALESCE would have
// done with a NULL anyway — bound explicitly so the parameter is a bool rather
// than a pointer whose nil means the same thing.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

// Notifications.

func notificationFromRow(r *notificationsdb.GetNotificationRow) *Notification {
	return &Notification{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
		ReadAt:        utcPtr(r.ReadAt),
		ID:            r.ID,
		Scope:         r.Scope,
		Principal:     r.Principal,
		Topic:         r.Topic,
		Title:         r.Title,
		Body:          r.Body,
		Link:          r.Link,
	}
}

func notificationPageRow(r *notificationsdb.ListNotificationsRow) pageRow[Notification] {
	return pageRow[Notification]{
		value: &Notification{
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
			ReadAt:        utcPtr(r.ReadAt),
			ID:            r.ID,
			Scope:         r.Scope,
			Principal:     r.Principal,
			Topic:         r.Topic,
			Title:         r.Title,
			Body:          r.Body,
			Link:          r.Link,
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

// unreadPageRow is notificationPageRow for the unread list's nominally distinct
// row type. The two projections are identical — the unread statement is the
// same SELECT with one more predicate — so this is the conversion rather than a
// second restatement, and it stops compiling if they ever diverge.
func unreadPageRow(r *notificationsdb.ListUnreadNotificationsRow) pageRow[Notification] {
	converted := notificationsdb.ListNotificationsRow(*r)

	return notificationPageRow(&converted)
}

func createNotificationParams(scope tenancy.Scope, n *Notification) notificationsdb.CreateNotificationParams {
	return notificationsdb.CreateNotificationParams{
		ID:        n.ID,
		Scope:     scope,
		Principal: n.Principal,
		Topic:     n.Topic,
		Title:     n.Title,
		Body:      n.Body,
		Link:      n.Link,
		ReadAt:    utcPtr(n.ReadAt),
	}
}

func listNotificationsParams(
	scope tenancy.Scope,
	principal string,
	filter *filtering.QueryFilter,
) notificationsdb.ListNotificationsParams {
	w := windowFrom(filter)

	return notificationsdb.ListNotificationsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		Principal:       principal,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// Devices.

func deviceFromRow(r *notificationsdb.GetDeviceByTokenRow) *Device {
	return &Device{
		CreatedAt:  r.CreatedAt.UTC(),
		LastSeenAt: r.LastSeenAt.UTC(),
		ID:         r.ID,
		Scope:      r.Scope,
		Principal:  r.Principal,
		Token:      r.Token,
		Platform:   Platform(r.Platform),
	}
}

func devicePageRow(r *notificationsdb.ListDevicesRow) pageRow[Device] {
	return pageRow[Device]{
		value: &Device{
			CreatedAt:  r.CreatedAt.UTC(),
			LastSeenAt: r.LastSeenAt.UTC(),
			ID:         r.ID,
			Scope:      r.Scope,
			Principal:  r.Principal,
			Token:      r.Token,
			Platform:   Platform(r.Platform),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func deviceFromSetRow(r *notificationsdb.ListDevicesByPrincipalsRow) *Device {
	return &Device{
		CreatedAt:  r.CreatedAt.UTC(),
		LastSeenAt: r.LastSeenAt.UTC(),
		ID:         r.ID,
		Scope:      r.Scope,
		Principal:  r.Principal,
		Token:      r.Token,
		Platform:   Platform(r.Platform),
	}
}

func registerDeviceParams(scope tenancy.Scope, d *Device) notificationsdb.RegisterDeviceParams {
	return notificationsdb.RegisterDeviceParams{
		ID:         d.ID,
		Scope:      scope,
		Principal:  d.Principal,
		Platform:   d.Platform.String(),
		Token:      d.Token,
		LastSeenAt: d.LastSeenAt.UTC(),
	}
}

func listDevicesParams(
	scope tenancy.Scope,
	principal string,
	filter *filtering.QueryFilter,
) notificationsdb.ListDevicesParams {
	w := windowFrom(filter)

	return notificationsdb.ListDevicesParams{
		CreatedAfter:  w.createdAfter,
		CreatedBefore: w.createdBefore,
		Scope:         scope,
		Principal:     principal,
		PageCursor:    w.pageCursor,
		ResultLimit:   w.resultLimit,
	}
}
