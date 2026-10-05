package issuereports

import (
	"context"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeReport is what an audit entry about a Report names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "report" is never mistaken for it.
const ResourceTypeReport = "issuereports.report"

// The events this package's writes emit, one per write. They are platform's
// names for platform's own writes, as waitlists' are.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
const (
	// EventReportCreated says somebody filed a report.
	EventReportCreated webhooks.EventType = "issuereports.report.created"
	// EventReportUpdated says a report's reporter revised what it says. It never
	// moves the status.
	EventReportUpdated webhooks.EventType = "issuereports.report.updated"
	// EventReportTransitioned says a report moved through its lifecycle; the
	// payload carries the status it left and the one it is in.
	EventReportTransitioned webhooks.EventType = "issuereports.report.transitioned"
	// EventReportArchived says a report was removed from the queue.
	EventReportArchived webhooks.EventType = "issuereports.report.archived"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, issuereports.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventReportCreated:      {Description: "Somebody filed an issue report."},
		EventReportUpdated:      {Description: "An issue report's reporter revised it."},
		EventReportTransitioned: {Description: "An issue report moved through its lifecycle."},
		EventReportArchived:     {Description: "An issue report was removed from the queue."},
	}
}

// ReportEvent is the payload of every report event.
//
// It names the report, its reporter, its kind and where it stands, and not
// what it says. The details are free text the reporter wrote, and the
// resolution a triager's note about it; an event is copied into every
// subscriber's logs, and a subscriber that needs either reads the report.
type ReportEvent struct {
	_ struct{} `json:"-"`

	// ReportID is the report the event is about.
	ReportID string `json:"reportID"`
	// Reporter is who filed it.
	Reporter string `json:"reporter"`
	// Kind is the application's category for it, which is what a triage
	// subscriber routes by.
	Kind string `json:"kind"`
	// Status is where the report stands after the write.
	Status Status `json:"status"`
	// PreviousStatus is where it stood before, on a transition, and empty on
	// every other event.
	PreviousStatus Status `json:"previousStatus,omitempty"`

	// Changed names the fields a revision or a transition moved, sorted, and is
	// empty for every other event. The timestamp every save stamps is left off.
	Changed []string `json:"changed,omitempty"`
}

// The metadata keys an audit entry here carries.
const (
	metadataKind           = "kind"
	metadataStatus         = "status"
	metadataPreviousStatus = "previousStatus"
)

// lastUpdatedAtField is the json name of the timestamp every save stamps, which
// a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write but the erasure records
// an audit entry naming the report and emits the event above for it, both on
// the write's transaction, through the recording.Recorder it is built with. An
// erasure records nothing, for the reason AfterDeleteReportsByReporter gives.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. A consumer that wants one entry shaped differently embeds it and
// overrides that method.
//
// Where an entry is filed and who made it are the Recorder's. Every report
// entry sets Entry.SubjectID to the report's reporter, so a Recorder filing by
// subject can; none names the reporter in its metadata, for the reason
// waitlists.RecordingHooks gives.
//
// Nor does it redact. A revision's diff carries the details old and new, and a
// transition's the resolution note it replaced; whether either is personal data
// is the deployment's call, made with audit.WithRedaction for
// [ResourceTypeReport] on the audit recorder.
type RecordingHooks struct {
	recorder *recording.Recorder
}

var _ Hooks = (*RecordingHooks)(nil)

// NewRecordingHooks builds the Hooks that record every write through recorder.
func NewRecordingHooks(recorder *recording.Recorder) (*RecordingHooks, error) {
	if recorder == nil {
		return nil, ErrNilRecorder
	}

	return &RecordingHooks{recorder: recorder}, nil
}

// AfterCreateReport records a report being filed.
func (h *RecordingHooks) AfterCreateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) error {
	if report == nil {
		return ErrNilReport
	}

	return h.record(ctx, tx, scope, report, audit.EventCreated, EventReportCreated, "", nil)
}

// AfterUpdateReport records a revision. It never records a transition: a
// revision cannot move the status, so the entry and the event carry the status
// the report is in and no previous one.
func (h *RecordingHooks) AfterUpdateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Report) error {
	if before == nil || after == nil {
		return ErrNilReport
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the revised issue report")
	}

	return h.record(ctx, tx, scope, after, audit.EventUpdated, EventReportUpdated, "", changes)
}

// AfterTransitionReport records a report moving through its lifecycle. The
// diff carries the status, the resolution note and the closing stamp it moved
// away from, so who resolved a report and what it said before somebody
// reopened it is answerable from the log after the row no longer says.
func (h *RecordingHooks) AfterTransitionReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Report) error {
	if before == nil || after == nil {
		return ErrNilReport
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the transitioned issue report")
	}

	return h.record(ctx, tx, scope, after, audit.EventUpdated, EventReportTransitioned, before.Status, changes)
}

// AfterArchiveReport records a report being removed from the queue. The row is
// the one the archive left, so the status recorded is the one it was archived
// in.
func (h *RecordingHooks) AfterArchiveReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) error {
	if report == nil {
		return ErrNilReport
	}

	return h.record(ctx, tx, scope, report, audit.EventArchived, EventReportArchived, "", nil)
}

// AfterDeleteReportsByReporter records nothing, deliberately.
//
// Store.DeleteReportsByReporter is an erasure's write, one table of the many a
// single erasure request reaches, and dataprivacy.Fulfiller records that request
// once: one audit entry naming it, with this table's count among the
// per-section counts in its metadata, and one dataprivacy.EventErasureFulfilled.
// An entry and an event here as well would be the request's fan-out across
// stores written down as a separate fact per store, none of which says which
// erasure it belonged to.
//
// A consumer that calls Store.DeleteReportsByReporter outside an erasure has a
// deletion nothing else recorded, and embeds this type to override the method.
func (h *RecordingHooks) AfterDeleteReportsByReporter(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// record writes the entry and the event for a write to one report. The entry's
// SubjectID is the report's reporter, so a Recorder filing by subject can.
func (h *RecordingHooks) record(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	report *Report,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	previous Status,
	changes map[string]audit.Change,
) error {
	metadata := map[string]string{
		metadataKind:   report.Kind,
		metadataStatus: string(report.Status),
	}

	if previous != "" {
		metadata[metadataPreviousStatus] = string(previous)
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeReport,
		ResourceID:   report.ID,
		SubjectID:    report.Reporter,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: report.ID,
		Payload: &ReportEvent{
			ReportID:       report.ID,
			Reporter:       report.Reporter,
			Kind:           report.Kind,
			Status:         report.Status,
			PreviousStatus: previous,
			Changed:        changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every save stamps. It is nil for a nil diff, so an event for a
// write that is not an update carries no changed list at all.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastUpdatedAtField })
}
