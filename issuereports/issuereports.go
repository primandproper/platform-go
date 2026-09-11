package issuereports

import (
	"slices"
	"strings"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The keys this package attaches to spans and log lines. Declared once so a
// trace and a log line name the same fact the same way.
const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "issuereports"

	scopeKey       = serviceName + ".scope"
	reportIDKey    = serviceName + ".report_id"
	reporterKey    = serviceName + ".reporter"
	statusKey      = serviceName + ".status"
	fromStatusKey  = serviceName + ".from_status"
	subjectTypeKey = serviceName + ".subject_type"
	subjectIDKey   = serviceName + ".subject_id"
	countKey       = serviceName + ".count"
)

// ReporterAttributeKey is the metric and span attribute a caller labels its own
// instruments with when the thing being measured is about one reporter. It is
// exported so a consumer's attributes agree with this package's rather than
// merely resembling them.
const ReporterAttributeKey = reporterKey

// Status is where a report stands.
//
// It is a type rather than a string because the set is closed and because the
// moves between its members are the whole of what this package adds to a table
// of free text. A report that could hold any status would need every caller to
// agree on the spelling of "resolved", and the first one to write "closed"
// would drop its reports out of every queue without anything reporting an
// error.
type Status string

const (
	// StatusOpen is a report nobody has picked up. Every report is born here.
	StatusOpen Status = "open"
	// StatusAcknowledged is a report somebody has picked up and not yet
	// finished with.
	//
	// It is a status rather than an assignee field because the fact worth
	// storing is that the report has been seen. Who is working it is a question
	// with a different answer in every organization — a person, a rota, a team
	// — and this package does not own the directory any of those live in.
	StatusAcknowledged Status = "acknowledged"
	// StatusResolved is a report that has been dealt with.
	StatusResolved Status = "resolved"
	// StatusDeclined is a report that will not be dealt with: a duplicate, a
	// misunderstanding, working as intended.
	//
	// It is distinct from resolved because the difference is what a reporter is
	// told and what a product learns. Collapsing the two would make "how many of
	// last month's reports were real" unanswerable from the table.
	StatusDeclined Status = "declined"
)

// Statuses is every status, in lifecycle order.
//
// It is exported because a triage console renders one queue per status, and a
// list assembled at the call site is a list that goes stale the day this one
// grows.
var Statuses = []Status{StatusOpen, StatusAcknowledged, StatusResolved, StatusDeclined}

// transitions is the lifecycle: for each status, the statuses a report in it may
// move to.
//
// It is a table rather than a chain of conditions because that is what it is,
// and because the shape of the lifecycle is the thing a reader of this package
// came for. Four rules are worth reading out of it:
//
// A report is born open and can be picked up, dealt with, or turned down.
//
// An acknowledged report cannot go back to open. Somebody has seen it, and a
// status that could un-see it would make "acknowledged" mean "seen, unless
// somebody moved it back", which is not a fact anybody can act on.
//
// A closed report reopens to open rather than to acknowledged, because reopening
// is what happens when the resolution turned out not to hold — so nobody has
// dealt with it, which is what open means.
//
// Nothing transitions to itself. A second resolve is not a no-op: it would move
// closed_at forward and overwrite the note, and the caller doing it is a caller
// acting on a view of the row that is one write out of date.
var transitions = map[Status][]Status{
	StatusOpen:         {StatusAcknowledged, StatusResolved, StatusDeclined},
	StatusAcknowledged: {StatusResolved, StatusDeclined},
	StatusResolved:     {StatusOpen},
	StatusDeclined:     {StatusOpen},
}

// ParseStatus reads a status off the wire — a request body, a query string, an
// admin console's form — normalizing case and surrounding space, and reports
// whether it names one this package serves.
//
// It normalizes rather than merely comparing because the string reaching it was
// typed by a client. "Open" and "open" are one status, and a store that accepted
// both would hold reports in two queues neither of which is the whole queue.
func ParseStatus(s string) (Status, bool) {
	status := Status(strings.ToLower(strings.TrimSpace(s)))

	return status, status.Valid()
}

// Valid reports whether s is a status this package serves.
func (s Status) Valid() bool { return len(transitions[s]) > 0 }

// Terminal reports whether s is a status a report stops in: resolved or
// declined.
//
// It is what decides whether a transition stamps closed_at, so it is a method on
// the status rather than a list at the one call site that reads it — a status
// added without an answer here would be one whose reports have no time to
// resolution and nothing to say so.
func (s Status) Terminal() bool { return s == StatusResolved || s == StatusDeclined }

// CanTransitionTo reports whether a report in status s may move to status to.
//
// A status that is not one this package serves can move nowhere, and nothing can
// move to it: an unknown status is refused at the seam rather than stored and
// discovered later by a queue that has stopped returning some of its rows.
func (s Status) CanTransitionTo(to Status) bool {
	return slices.Contains(transitions[s], to)
}

// String renders the status as it is stored.
func (s Status) String() string { return string(s) }

// Report is one thing somebody told you about your product, and where that
// report stands.
//
// It is deliberately app-independent. Kind is the consumer's own category and
// nothing here validates it; SubjectType and SubjectID are how the application
// names the thing the report is about. What this package owns is the lifecycle
// around them — open, acknowledged, resolved or declined — which is the half
// that is the same in every application and the half that every application
// otherwise writes again.
type Report struct {
	// CreatedAt is when the report was filed, assigned by the database.
	CreatedAt time.Time `json:"createdAt"`
	// LastUpdatedAt is when the row last changed: a revision, or a move through
	// the lifecycle.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// ArchivedAt is when the report was removed from the queue, and nil while it
	// is still there.
	//
	// It is not "closed". A resolved report is still a report, and archiving is
	// what a consumer does to one that should stop being listed at all — a test
	// submission, a duplicate somebody filed twice by refreshing.
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	// ClosedAt is when the report reached a terminal status, and nil while it is
	// still open or acknowledged. A reopen clears it.
	//
	// A timestamp rather than a boolean beside Status, because "when" answers
	// "whether" and a boolean does not answer "when" — which is what a
	// time-to-resolution number is entirely about.
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// ID identifies the report.
	ID string `json:"id"`
	// Reporter is who filed it — a user id in every deployment this module has,
	// but a string here, because issuereports does not own the directory.
	Reporter string `json:"reporter"`
	// Kind is the application's own category: bug, billing, abuse. A triage
	// queue groups and routes by it.
	Kind string `json:"kind"`
	// Details is what the person actually said.
	Details string `json:"details"`
	// SubjectType is what the report is about, as the application names it — a
	// table, an entity kind. Empty is a report about the product in general.
	SubjectType string `json:"subjectType,omitempty"`
	// SubjectID is which one. Empty is a report about a kind of thing rather
	// than about one of them.
	SubjectID string `json:"subjectID,omitempty"`
	// Status is where the report stands.
	Status Status `json:"status"`
	// Resolution is why the report is in the terminal status it is in — the note
	// a triager leaves when they resolve or decline it. A reopen clears it.
	Resolution string `json:"resolution,omitempty"`
	// Scope is whose data this is.
	Scope tenancy.Scope `json:"scope"`
}

// Closed reports whether the report has reached a terminal status.
func (r *Report) Closed() bool { return r != nil && r.Status.Terminal() }
