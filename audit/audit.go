package audit

import (
	"regexp"
	"time"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "audit"

// DefaultTablePrefix is the namespace the audit tables carry when none is
// configured, which is none — rendering audit_log_entries and audit_log_chains.
//
// The audit_log_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_audit_log_entries, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the audit. prefix is applied uniformly — an un-namespaced attribute name
// collides with every other component writing to the same trace.
//
// Nothing here carries a value from an Entry's Changes or Metadata. Those are
// the fields most likely to hold exactly what Redaction exists to keep out of
// durable storage, and a span exporter is durable storage.
const (
	entryIDKey      = "audit.entry_id"
	entryCountKey   = "audit.entry_count"
	scopeKey        = "audit.scope"
	scopeCountKey   = "audit.scope_count"
	seqKey          = "audit.seq"
	resourceTypeKey = "audit.resource_type"
	resourceIDKey   = "audit.resource_id"
	eventTypeKey    = "audit.event_type"
	actorIDKey      = "audit.actor_id"
	actorTypeKey    = "audit.actor_type"
	checkedKey      = "audit.checked"
	intactKey       = "audit.intact"
	breakReasonKey  = "audit.break_reason"
)

var (
	// ErrNilExecutor indicates Record was called without a query executor. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")

	// ErrNilEntry indicates a nil *Entry was passed to Record.
	ErrNilEntry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit entry")

	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrEmptyResourceType indicates an Entry that does not say what it is about.
	ErrEmptyResourceType = platformerrors.New("empty audit resource type")

	// ErrEmptyEventType indicates an Entry with no event type. Use EventOther
	// rather than the empty string for events outside the vocabulary below.
	ErrEmptyEventType = platformerrors.New("empty audit event type")

	// ErrEmptyActor indicates an Entry with no actor ID. Every recorded event
	// has someone or something responsible for it — a background job that
	// belongs to no user is ActorSystem with the job's name, not an absence.
	ErrEmptyActor = platformerrors.New("empty audit actor")

	// ErrEntryNotFound indicates a Get for an ID that is not in the log. It may
	// mean the entry never existed, that retention has pruned it, or that it was
	// deleted — Verify is what distinguishes the third case from the first two.
	ErrEntryNotFound = platformerrors.New("audit entry not found")

	// ErrChainBroken indicates a scope's hash chain failed to verify.
	//
	// Verify does not return it — a break is a finding, not a failure to
	// answer, and the caller needs the VerificationResult's detail either way.
	// It exists so that a caller escalating a break has a sentinel to wrap, and
	// so the line this package logs on a break is attributable to a cause rather
	// than to a bare message.
	ErrChainBroken = platformerrors.New("audit chain verification failed")

	// ErrInvalidTablePrefix indicates a prefix that is not a plain SQL
	// identifier fragment. Prefixes are interpolated into queries rather than
	// bound, so they are restricted rather than escaped.
	ErrInvalidTablePrefix = platformerrors.New("invalid audit table prefix")
)

// validPrefix matches a table prefix safe to interpolate into query text: a
// bare identifier fragment, or nothing.
var validPrefix = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)?$`)

// ValidateTablePrefix reports whether a prefix is safe to interpolate into this
// package's query text, wrapping ErrInvalidTablePrefix when it is not.
//
// It is exported because the prefix has to be the same in four places — the
// Recorder that writes, the Reader that verifies, the PruneTarget that removes,
// and the migrations that created the tables — and a caller assembling those
// from one configuration field should be able to refuse a bad value once,
// before any of them is built.
func ValidateTablePrefix(prefix string) error {
	if !validPrefix.MatchString(prefix) {
		return platformerrors.Wrapf(ErrInvalidTablePrefix, "audit table prefix %q", prefix)
	}

	return nil
}

// EventType names what happened to a resource.
//
// The constants below are the vocabulary this package expects to see and the
// one the prior art converged on, but the type is a bare string and nothing
// validates against the set: the event taxonomy is an application concern, and
// a consumer whose domain distinguishes "approved" from "updated" should say
// so rather than flatten both into EventOther.
type EventType string

const (
	// EventCreated records a resource coming into existence.
	EventCreated EventType = "created"
	// EventUpdated records a change to an existing resource.
	EventUpdated EventType = "updated"
	// EventDeleted records a resource being destroyed.
	EventDeleted EventType = "deleted"
	// EventArchived records a resource being soft-deleted.
	EventArchived EventType = "archived"
	// EventAccessed records a read of a sensitive resource.
	//
	// Read-auditing is a real HIPAA and SOC 2 requirement, and it is also the
	// event type most likely to swamp the table: reads outnumber writes by
	// orders of magnitude, and unlike writes they are not already paying for a
	// transaction. Record it for the resources that genuinely require it, and
	// consider giving it its own table via WithRecorderTablePrefix, so the
	// retention window and the index set can differ from the mutation log's.
	EventAccessed EventType = "accessed"
	// EventOther records an event outside the vocabulary above.
	EventOther EventType = "other"
)

// ActorType distinguishes the kinds of principal that can act.
//
// Like EventType this is a bare string with suggested constants rather than a
// closed set, so a consumer with a fourth kind of principal is not forced to
// misfile it.
type ActorType string

const (
	// ActorUser is a human principal.
	ActorUser ActorType = "user"
	// ActorService is another service acting under its own credentials.
	ActorService ActorType = "service"
	// ActorSystem is the application itself: migrations, schedulers, background
	// jobs — anything with no external principal behind it.
	ActorSystem ActorType = "system"
)

// Actor is who did the thing.
type Actor struct {
	// ID identifies the principal. Required.
	ID string `json:"id"`
	// Type says what kind of principal it is.
	Type ActorType `json:"type,omitempty"`
	// IP is the address the action arrived from, where there was one. It is
	// recorded rather than derived at read time because the association between
	// a principal and an address is exactly what an investigation needs and is
	// not recoverable afterwards.
	IP string `json:"ip,omitempty"`
}

// Change is one field's before and after.
//
// Old and New are typed values rather than the rendered strings the prior art
// used, so that a change to a numeric field stays numeric through storage and
// a caller reading the log back is not left parsing. Diff produces these from a
// before/after pair; Redaction decides which of them are ever written.
type Change struct {
	// Old is the value before the event. It is absent for a creation.
	Old any `json:"old,omitempty"`
	// New is the value after the event. It is absent for a deletion.
	New any `json:"new,omitempty"`
}

// Entry is one record in the audit log.
//
// The fields divide into three groups. The caller supplies what happened
// (EventType, ResourceType, ResourceID, Actor, Scope, Changes, Metadata); the
// Recorder supplies identity and time (ID, RecordedAt) unless the caller has
// already set them; and the Recorder always supplies the chain (Seq, PrevHash,
// Hash), overwriting whatever was there. Record writes those assignments back
// into the value it was passed, so a caller that needs the entry's ID or hash
// afterwards — to reference it, or to notarize it somewhere outside this
// database — has them without a re-read.
type Entry struct {
	// RecordedAt is when the event happened. The Recorder stamps it from its
	// clock when zero.
	//
	// It is truncated to microseconds before it is written. Postgres and MySQL
	// store microseconds, so a nanosecond-precision value would not survive the
	// round trip — and a timestamp that changes on the way back out is a
	// timestamp the hash chain would report as tampering on every single entry.
	RecordedAt time.Time `json:"recordedAt"`

	// Changes is the per-field before/after of the event, keyed by field name.
	// Diff builds it from a before/after pair; Redaction filters it.
	Changes map[string]Change `json:"changes,omitempty"`

	// Metadata is free-form context — a request ID, a reason, a ticket
	// reference. It passes through Redaction on the same field names as
	// Changes, because a secret dropped in here is exactly as durable as one
	// dropped in there.
	Metadata map[string]string `json:"metadata,omitempty"`

	// ID identifies the entry. The Recorder assigns one when empty.
	ID string `json:"id"`

	// Actor is who did it. Actor.ID is required.
	Actor Actor `json:"actor"`

	// Scope is the tenancy boundary the entry belongs to — an account, an
	// organization, a workspace. It is one opaque string rather than the
	// two-level user/account pair the prior art hardcoded, because tenancy
	// depth is an application's decision and a two-level model cannot express
	// one level or three.
	//
	// It is also the hash chain's partition: entries chain per scope, so two
	// tenants writing concurrently do not serialize against each other. Empty
	// is a scope like any other, and is the right one for platform-level events
	// that belong to no tenant.
	Scope string `json:"scope,omitempty"`

	// ResourceType names the kind of thing acted on. Required.
	ResourceType string `json:"resourceType"`

	// ResourceID identifies the instance acted on.
	ResourceID string `json:"resourceID,omitempty"`

	// EventType names what happened. Required.
	EventType EventType `json:"eventType"`

	// PrevHash is the Hash of the preceding entry in this scope, hex-encoded,
	// or empty for the first entry in a scope. Assigned by Record.
	PrevHash string `json:"prevHash"`

	// Hash is this entry's digest over PrevHash and its own canonical image,
	// hex-encoded. Assigned by Record. See the package documentation for what
	// it does and does not prove.
	Hash string `json:"hash"`

	// Seq is the entry's position in its scope's chain, starting at zero.
	// Assigned by Record, and unique per scope: the chain cannot fork, because
	// the database will not accept two entries claiming the same position.
	Seq int64 `json:"seq"`
}

// validate reports whether the caller-supplied half of an entry is complete.
// The chain fields are not checked because Record assigns them.
func (e *Entry) validate() error {
	switch {
	case e == nil:
		return ErrNilEntry
	case e.ResourceType == "":
		return ErrEmptyResourceType
	case e.EventType == "":
		return ErrEmptyEventType
	case e.Actor.ID == "":
		return ErrEmptyActor
	default:
		return nil
	}
}
