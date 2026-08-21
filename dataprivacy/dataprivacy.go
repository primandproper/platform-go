package dataprivacy

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/primandproper/platform-go/v12/database"
	platformerrors "github.com/primandproper/platform-go/v12/errors"
	"github.com/primandproper/platform-go/v12/filtering"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "dataprivacy"

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the dataprivacy. prefix is applied uniformly — an un-namespaced attribute
// name collides with every other component writing to the same trace.
//
// Nothing here carries a collected fragment, an artifact's bytes, or anything
// that came back from a Collector. This package exists to move a file
// containing everything an application knows about a person, and a span
// exporter is durable storage that person never consented to.
const (
	requestIDKey    = "dataprivacy.request_id"
	requestTypeKey  = "dataprivacy.request_type"
	subjectIDKey    = "dataprivacy.subject_id"
	subjectTypeKey  = "dataprivacy.subject_type"
	subjectScopeKey = "dataprivacy.subject_scope"
	statusKey       = "dataprivacy.status"
	sectionKey      = "dataprivacy.section"
	sectionCountKey = "dataprivacy.section_count"
	failureCountKey = "dataprivacy.failure_count"
	artifactRefKey  = "dataprivacy.artifact_ref"
	artifactSizeKey = "dataprivacy.artifact_bytes"
	deletedKey      = "dataprivacy.deleted"
	anonymizedKey   = "dataprivacy.anonymized"
	retainedKey     = "dataprivacy.retained"
	shreddedKey     = "dataprivacy.key_shredded"
	keyDestroyedKey = "dataprivacy.key_destroyed"
	expiredKey      = "dataprivacy.expired"
	overdueKey      = "dataprivacy.overdue"
	sweptKey        = "dataprivacy.swept"
	finalKey        = "dataprivacy.final_attempt"

	// operationIDKey is namespaced to this package rather than reusing the
	// operations. prefix, because on a dataprivacy span it answers "which
	// operation is fulfilling this request" — a fact about the request. The
	// operations spans carry their own, and a trace that spans both wants to see
	// the same value from two vantage points rather than one attribute set twice.
	operationIDKey = "dataprivacy.operation_id"

	// Store-layer keys. The database client traces the statement, but with the
	// SQL text suppressed by default — so without these a trace shows an
	// anonymous query span and no indication of which request it was about.
	storeOpKey      = "dataprivacy.store_operation"
	fromStatusKey   = "dataprivacy.from_status"
	rowsAffectedKey = "dataprivacy.rows_affected"
	guardMissedKey  = "dataprivacy.guard_missed"
	resultCountKey  = "dataprivacy.result_count"
	resultTotalKey  = "dataprivacy.result_total"
	limitKey        = "dataprivacy.limit"
	lapsedKey       = "dataprivacy.lapsed"
	reapedKey       = "dataprivacy.reaped"
)

var (
	// ErrNilStore indicates a nil Store. It wraps errors.ErrNilInputParameter,
	// so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy store")

	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilExecutor indicates a Store method that runs in the caller's
	// transaction was called without one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")

	// ErrNilRequest indicates a nil *Request.
	ErrNilRequest = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy request")

	// ErrNilOperations indicates a Service built without an operations.Service.
	//
	// It has no default. A Service that could not start operations would record
	// requests nothing ever fulfills, which looks exactly like a working Service
	// until a subject's statutory window runs out.
	ErrNilOperations = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy operations service")

	// ErrNotInProgress indicates a runner whose request row is not in
	// StatusInProgress.
	//
	// It means the request left the state the runner was started for while the
	// operation was queued or running: cancelled, already completed by an
	// earlier attempt, or reaped. It is unretryable — none of those become
	// StatusInProgress again by waiting.
	ErrNotInProgress = platformerrors.New("dataprivacy request is not in progress")

	// ErrEmptySubjectID indicates a Subject with no ID. Every request is about
	// somebody, and a request about nobody would fan out over every collector
	// asking for the empty string's data — which some of them will answer.
	ErrEmptySubjectID = platformerrors.New("empty dataprivacy subject ID")

	// ErrUnknownRequestType indicates a RequestType outside the two this package
	// implements.
	ErrUnknownRequestType = platformerrors.New("unknown dataprivacy request type")

	// ErrRequestNotFound indicates a request ID that is not in the table. It may
	// mean the request never existed, or that retention has swept it.
	ErrRequestNotFound = platformerrors.New("dataprivacy request not found")

	// ErrNotAwaitingConfirmation indicates a Confirm or Cancel naming a request
	// that is not waiting for one — because it was never two-phase, because it
	// has already been confirmed, or because its confirmation window lapsed.
	ErrNotAwaitingConfirmation = platformerrors.New("dataprivacy request is not awaiting confirmation")

	// ErrNoCollectors indicates an export Service built with no registered
	// Collector. It is refused at construction rather than at fulfillment: an
	// export service with no collectors produces a valid, empty, and entirely
	// wrong artifact, and a subject who receives one has been told that nothing
	// is held about them.
	ErrNoCollectors = platformerrors.New("no dataprivacy collectors registered")

	// ErrNoErasers indicates an erasure Service built with no registered Eraser,
	// refused for the same reason as ErrNoCollectors and with a worse failure
	// mode: an erasure that erases nothing reports success.
	ErrNoErasers = platformerrors.New("no dataprivacy erasers registered")

	// ErrDuplicateKey indicates two registrations under one key. Keys become
	// section names in the artifact, so a silent overwrite would drop a domain
	// from every export without any signal that it had.
	ErrDuplicateKey = platformerrors.New("duplicate dataprivacy registration key")

	// ErrInvalidKey indicates a registration key that is empty or is not a
	// plain identifier. Keys are JSON object keys in the artifact and path
	// segments in telemetry, so they are restricted rather than escaped.
	ErrInvalidKey = platformerrors.New("invalid dataprivacy registration key")

	// ErrArtifactUnavailable indicates a Download or Open for a request that has
	// no artifact: an erasure, a request that has not completed, or one whose
	// artifact has expired and been deleted.
	ErrArtifactUnavailable = platformerrors.New("dataprivacy artifact is unavailable")

	// ErrArtifactEncrypted indicates a Download against a Service configured
	// with an Encryptor.
	//
	// The two are genuinely incompatible rather than merely awkward. A signed
	// URL hands the client the stored object, and the stored object is
	// ciphertext this package base64s — a subject who follows that link gets a
	// file they cannot open, and finds out thirty days into a statutory window.
	// Encryption at rest and direct-to-bucket delivery are a choice between two
	// things, so configuring both fails here rather than at the subject.
	ErrArtifactEncrypted = platformerrors.New("dataprivacy artifact is encrypted and cannot be delivered by signed URL")

	// ErrNoURLSigner indicates a Download against an UploadManager that cannot
	// sign URLs. Not every provider can — the filesystem one certainly cannot —
	// and Open is the path that works everywhere.
	ErrNoURLSigner = platformerrors.New("dataprivacy upload manager cannot sign URLs")

	// ErrNilFetch indicates CollectAll or CollectorFor given no read to page
	// through. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilFetch = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy paged read")

	// ErrNilPage indicates a paged read that returned neither a page nor an
	// error.
	//
	// It is an error rather than an end-of-results, because the two are not the
	// same thing and only one of them is safe to guess at: a nil result treated
	// as the end produces a short export that reads as a complete one.
	ErrNilPage = platformerrors.New("dataprivacy paged read returned no result")

	// ErrCursorStalled indicates a paged read that answered a full page with
	// the cursor it was asked for, so the walk would repeat that page forever.
	//
	// Stopping there instead would be worse than failing. The rows past the
	// stall are held about the subject and would be missing from the artifact,
	// and a truncated subject access request is indistinguishable from a
	// complete one to everyone except the regulator asking about the rest.
	ErrCursorStalled = platformerrors.New("dataprivacy paged read did not advance")
)

// SubjectType distinguishes the kinds of thing a request can be about.
//
// Like audit.ActorType this is a bare string with suggested constants rather
// than a closed set: an application whose data hangs off a third kind of
// principal should say so rather than misfile it as one of these.
type SubjectType string

const (
	// SubjectUser is a natural person — the subject GDPR and CCPA are written
	// about.
	SubjectUser SubjectType = "user"
	// SubjectAccount is an account, tenant, or organization. An account-scoped
	// request is the one that arrives when a business customer leaves.
	SubjectAccount SubjectType = "account"
)

// Subject is who or what a request is about.
type Subject struct {
	// ID identifies the subject. Required.
	ID string `json:"id"`
	// Scope is the account or tenant the request is confined to, when it is
	// confined at all. Empty means the request spans every scope the subject
	// appears in, which is what a plain "give me my data" asks for.
	//
	// It is one opaque string rather than a typed tenancy path for the same
	// reason audit.Entry.Scope is: tenancy depth is an application's decision,
	// and a two-level model cannot express one level or three.
	Scope string `json:"scope,omitempty"`
	// Type says what kind of subject it is.
	Type SubjectType `json:"type,omitempty"`
}

// validate reports whether the subject names anything.
func (s Subject) validate() error {
	if s.ID == "" {
		return ErrEmptySubjectID
	}

	return nil
}

// RequestType names what a request asks for.
type RequestType string

const (
	// RequestExport is a subject access request: collect everything held about
	// the subject and deliver it.
	RequestExport RequestType = "export"
	// RequestErasure is a right-to-be-forgotten request: delete or anonymize
	// what is held about the subject, retaining only what must be retained.
	RequestErasure RequestType = "erasure"
)

// Valid reports whether t is a request type this package implements.
func (t RequestType) Valid() bool {
	return t == RequestExport || t == RequestErasure
}

// Status is where a request has got to.
//
// The transitions between these are diagrammed in the package overview.
//
// It is not the operation's state and does not mirror it. The operation says how
// the current attempt is going — pending, running, how many units in, which
// attempt — and is reaped on its own retention. This says what the request is,
// as the statutory record of it: whether somebody still has to confirm it,
// whether it was fulfilled, and whether the artifact it produced still exists.
// Those are different questions with different lifetimes, and collapsing them
// into one column would have the record of a request somebody made three years
// ago disappear along with the progress bar.
//
// The one state to dwell on is expired. It is reachable only from completed, and
// only for an export, and it is the state people forget. An export artifact
// contains everything an application knows about a person; without an expiry it
// is a permanent object in a bucket.
type Status string

const (
	// StatusAwaitingConfirmation is an erasure that has been submitted but not
	// yet confirmed. Reachable only when a confirmation window is configured,
	// and the one state in which no operation exists yet.
	StatusAwaitingConfirmation Status = "awaiting_confirmation"
	// StatusInProgress is a request an operation is fulfilling.
	//
	// It covers what used to be two states, pending and processing, because the
	// difference between them is now the operation's to record and this row
	// genuinely does not know it: whether a worker has picked the operation up,
	// which attempt it is on, and how far through the domains it has got are all
	// answers Request.OperationID points at.
	StatusInProgress Status = "in_progress"
	// StatusCompleted is a request that was fulfilled. An export in this state
	// has an ArtifactRef; it may also have Failures, which is what a partial
	// export looks like.
	StatusCompleted Status = "completed"
	// StatusFailed is a request whose operation gave up: it exhausted its
	// attempts, or failed for a reason no retry would fix.
	//
	// It is written by the runner on its final attempt rather than by the
	// operations worker, which knows the operation failed and has no notion of
	// the request behind it. See operations.Attempt.
	StatusFailed Status = "failed"
	// StatusExpired is a completed export whose artifact has been deleted.
	StatusExpired Status = "expired"
	// StatusCancelled is a request that was withdrawn: an erasure nobody
	// confirmed before its window lapsed, one the subject cancelled, or one
	// stopped mid-flight through the operation's cancellation.
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether a status is one nothing moves out of.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusExpired, StatusCancelled:
		return true
	case StatusAwaitingConfirmation, StatusInProgress:
		return false
	default:
		return false
	}
}

// Valid reports whether s is a status this package writes.
func (s Status) Valid() bool {
	switch s {
	case StatusAwaitingConfirmation, StatusInProgress,
		StatusCompleted, StatusFailed, StatusExpired, StatusCancelled:
		return true
	default:
		return false
	}
}

// Request is one export or erasure and everything known about how it went.
type Request struct {
	// RequestedAt is when the request was submitted. It is the instant the
	// statutory clock starts, so it is stamped once and never rewritten — not
	// by a confirmation, and not by a retry.
	RequestedAt time.Time `json:"requestedAt"`

	// DueAt is when the response is legally owed, computed at submission from
	// the configured response window for the request type. See Overdue.
	DueAt time.Time `json:"dueAt"`

	// ExpiresAt is when the artifact is deleted and an export moves to
	// StatusExpired. For an erasure it is when the confirmation window lapses,
	// and it is zero once the erasure is confirmed.
	ExpiresAt time.Time `json:"expiresAt"`

	// CompletedAt is when the request reached a terminal state. Nil until it
	// does.
	CompletedAt *time.Time `json:"completedAt,omitempty"`

	// KeyShreddedAt is when this subject's data key was destroyed, for an
	// erasure fulfilled by a Fulfiller with a shredder configured. Nil otherwise,
	// which covers three different situations worth telling apart: no shredder
	// is wired, the request is scoped and therefore cannot shred, or the erasure
	// has not run yet. Retained says which of the first two it was.
	//
	// It is set even when there was no key to destroy. The claim it records is
	// not "bytes were overwritten" but "as of this instant no key exists for
	// this subject and none can be minted", which is the property the erasure
	// rests on either way.
	KeyShreddedAt *time.Time `json:"keyShreddedAt,omitempty"`

	// Failures records the collector or eraser keys that errored, against the
	// rendered error. A completed export with a non-empty Failures is a partial
	// export: the artifact was delivered, and its manifest names these same
	// sections as missing.
	//
	// It is a rendered string rather than an error because it is stored and read
	// by a human — often a regulator — not re-wrapped by a caller.
	Failures map[string]string `json:"failures,omitempty"`

	// Retained records, per eraser key, what an erasure kept and why. It is the
	// answer to "you said you deleted everything", and the reason ErasureOutcome
	// carries a legal basis rather than only a count.
	Retained map[string]string `json:"retained,omitempty"`

	// ID identifies the request.
	ID string `json:"id"`

	// OperationID names the operation fulfilling this request, and is what a
	// client polls for progress. Empty only while an erasure awaits
	// confirmation, because until somebody confirms it there is nothing running.
	//
	// It is a separate identifier rather than the request's own, and the two are
	// deliberately not made equal. They are different objects with different
	// lifetimes: the operation is reaped on operations.Config.Retention — weeks —
	// and this record is kept for years, so an ID that meant both would go on
	// resolving to one of them long after it stopped resolving to the other.
	OperationID string `json:"operationID,omitempty"`

	// ArtifactRef is the uploads path of the export artifact. Empty for an
	// erasure, for an incomplete export, and for an expired one — the path is
	// cleared when the object is deleted, so a stale reference cannot outlive
	// the thing it referenced.
	ArtifactRef string `json:"artifactRef,omitempty"`

	// LastError is why a failed request failed, rendered. Empty otherwise.
	//
	// The operation carries the same failure in a shape a client can branch on,
	// and carries it better — a stable code rather than a string. This is the
	// copy that survives the operation being reaped, for the record that has to
	// last three years.
	LastError string `json:"lastError,omitempty"`

	// Subject is who the request is about.
	Subject Subject `json:"subject"`

	// Type is what was asked for.
	Type RequestType `json:"type"`

	// Status is where it got to.
	Status Status `json:"status"`

	// ArtifactBytes is the stored size of the artifact, after compression and
	// encryption. Zero for an erasure or an unfulfilled export.
	ArtifactBytes int64 `json:"artifactBytes,omitempty"`

	// Deleted and Anonymized are the erasure totals summed across every eraser.
	Deleted    int64 `json:"deleted,omitempty"`
	Anonymized int64 `json:"anonymized,omitempty"`
}

// Partial reports whether a completed request left something uncollected or
// unerased. A caller rendering a status page should say so: "completed" over a
// manifest with three missing sections is a misleading thing to show a subject
// who has thirty days to complain about it.
func (r *Request) Partial() bool {
	return r != nil && len(r.Failures) > 0
}

// Overdue reports whether the statutory response window has lapsed with the
// request still unfulfilled. A request that completed after its deadline is not
// overdue — it is late, which is a fact about the past and not a thing to page
// somebody about.
func (r *Request) Overdue(now time.Time) bool {
	if r == nil || r.DueAt.IsZero() || r.Status.Terminal() {
		return false
	}

	return now.After(r.DueAt)
}

// The operation kinds this package registers. They are written into every
// operation row this package starts, so they are stable by contract: renaming
// one strands every operation already queued under the old name, which the
// operations worker fails with operations.CodeUnknownKind.
const (
	// KindExport is the operations kind that collects, packages, and delivers a
	// subject access request.
	KindExport = "dataprivacy.export"

	// KindErasure is the operations kind that shreds and erases.
	KindErasure = "dataprivacy.erasure"
)

// KindFor names the operation kind that fulfills a request type, and reports
// whether there is one.
func KindFor(t RequestType) (string, bool) {
	switch t {
	case RequestExport:
		return KindExport, true
	case RequestErasure:
		return KindErasure, true
	default:
		return "", false
	}
}

// Job is the operation request both of this package's kinds are started with.
//
// It carries the request ID and nothing else, and that is deliberate. The
// operation row stores its request encoded in a column, and everything else
// about a privacy request — who it is about, what scope it covers — is exactly
// the material this package works hardest to keep out of places it does not need
// to be. The runner reads the request row, which is the one place that data has
// to live.
//
// It also means there is one source of truth for what the request says. A
// subject copied into the operation at submission and read back an hour later
// would be a second copy that nothing keeps current.
type Job struct {
	// RequestID names the dataprivacy request this operation fulfills.
	RequestID string `json:"requestID"`
}

// ExportSummary is what a completed export records in operations.Result.Detail,
// beside the artifact's key in Result.URI.
//
// It is deliberately narrower than Manifest, in two ways. It omits the subject,
// because the manifest travels inside the artifact — which is delivered to that
// person — and this travels in the operations table, read by a status endpoint
// that already knows who is asking. And it omits the section list, because the
// artifact's own manifest is the authority on what is in it.
//
// What is left is exactly what the request row holds, and that is the property
// worth having: a duplicate execution that finds the export already recorded can
// rebuild this summary from the row rather than reporting a thinner one than the
// attempt that did the work.
type ExportSummary struct {
	// GeneratedAt is when the artifact was assembled.
	GeneratedAt time.Time `json:"generatedAt"`

	// Failures maps a section name to why it is missing. Absent when the export
	// was complete, present — and the reason this type is worth having — when it
	// is a partial export that was delivered anyway.
	Failures map[string]string `json:"failures,omitempty"`

	// Bytes is the stored size of the artifact, after compression and
	// encryption.
	Bytes int64 `json:"bytes"`
}

// ErasureSummary is what a completed erasure records in
// operations.Result.Detail. There is no Result.URI: an erasure produces no
// artifact, which is the whole of what distinguishes it from an export here.
type ErasureSummary struct {
	// KeyShreddedAt is when the subject's data key was destroyed, or nil when no
	// shredder was configured or the request was scoped. Retained says which.
	KeyShreddedAt *time.Time `json:"keyShreddedAt,omitempty"`

	// Retained records, per eraser key, what was kept and why.
	Retained map[string]string `json:"retained,omitempty"`

	// Deleted and Anonymized are the totals summed across every eraser.
	Deleted    int64 `json:"deleted"`
	Anonymized int64 `json:"anonymized"`
}

// Collector produces one domain's view of a subject.
//
// Collect returns already-encoded JSON rather than a value to be marshaled, and
// that is the load-bearing decision in this package. The prior art this
// generalizes had every domain mutate one shared aggregate struct, so adding a
// domain meant editing a central type that imported every domain package — a
// cost paid on every schema change, by the one file most likely to conflict. A
// library cannot have that type at all, and it turns out not to need one: an
// opaque fragment per key composes into a document without the library knowing
// what any of it means.
//
// Returning nil, nil is how a domain says "nothing about this subject". The
// section is then omitted from the artifact rather than written as null, so an
// export's sections are the domains that actually held something.
//
// A Collector must not return partially-collected data alongside an error. The
// fragment is used or the error is recorded; there is no path that writes both.
//
// There is deliberately no as-of time in this signature, and it is worth being
// clear about what that means. A fragment is the domain's state at the instant
// Collect ran, which is when a worker got to the operation — not when the subject
// asked. The two differ by the queue depth plus any retries, and because
// collectors run concurrently they differ from each other as well: an artifact
// is a smear across the collection window rather than a snapshot at any one
// instant. Manifest.GeneratedAt is the only time the artifact states.
//
// This matches the ordinary reading of a subject access request — the data held
// when the response is produced — and it is the only thing a library can
// promise. Bounding an export to data created on or before Request.RequestedAt
// would have to be a parameter here, honored by every registered Collector, and
// nothing in this package could enforce it: a domain with no reliable creation
// timestamp cannot answer the question at all, and one that ignored the bound
// would be silently wrong in the direction that matters. An application whose
// jurisdiction or dispute posture needs that guarantee has to implement it in
// its collectors, and know that it has.
type Collector interface {
	Collect(ctx context.Context, subject Subject) (json.RawMessage, error)
}

// CollectorFunc adapts a function to Collector.
type CollectorFunc func(ctx context.Context, subject Subject) (json.RawMessage, error)

// Collect implements Collector.
func (f CollectorFunc) Collect(ctx context.Context, subject Subject) (json.RawMessage, error) {
	return f(ctx, subject)
}

// Eraser removes or anonymizes one domain's data about a subject.
//
// It is deliberately separate from Collector rather than derived from it.
// Erasure is not the inverse of export: some data must be retained (financial
// records under tax law, audit entries under legitimate interest) and some must
// be anonymized in place rather than deleted, because a foreign key still
// points at it. Only the domain knows which of the three applies to each of its
// tables, and a library that inferred "erase everything you would have
// exported" would be confidently wrong about all of it.
//
// Erase runs inside the request's transaction and must use the executor it is
// given rather than a handle of its own. Every registered eraser for one
// request shares that transaction, so an erasure is all-or-nothing: a subject
// is not left half-deleted across eleven domains because the ninth timed out.
type Eraser interface {
	Erase(ctx context.Context, q database.SQLQueryExecutor, subject Subject) (ErasureOutcome, error)
}

// EraserFunc adapts a function to Eraser.
type EraserFunc func(ctx context.Context, q database.SQLQueryExecutor, subject Subject) (ErasureOutcome, error)

// Erase implements Eraser.
func (f EraserFunc) Erase(ctx context.Context, q database.SQLQueryExecutor, subject Subject) (ErasureOutcome, error) {
	return f(ctx, q, subject)
}

// ErasureOutcome is what one domain did.
type ErasureOutcome struct {
	// Retained names what was kept and the legal basis for keeping it — the
	// string goes into the request record and, in practice, in front of a
	// regulator. "invoices: financial records, retained 7 years under
	// [statute]" is the shape that answers the question; "some data" is not.
	//
	// It is keyed so one domain can retain several things for different
	// reasons, which is the normal case rather than the exotic one.
	Retained map[string]string `json:"retained,omitempty"`
	// Deleted is how many rows were destroyed.
	Deleted int64 `json:"deleted"`
	// Anonymized is how many rows were kept but stripped of anything
	// identifying. A row that was both is counted once, here.
	Anonymized int64 `json:"anonymized"`
}

// Service is the application-facing seam: submit a request, ask after one, list
// them.
//
// Fulfillment is deliberately not on this interface. A Submit that collected
// eleven domains inline would tie a regulatory obligation to the lifetime of an
// HTTP request, and the one guarantee a subject access request needs is that it
// survives the process that accepted it. Submit writes a row and starts an
// operation; an operations Worker runs it.
//
// There is no status endpoint here either, and there deliberately is not one.
// "How far along is my export" is answered by operations/http against
// Request.OperationID — the same endpoint, the same shape, and the same event
// stream every other long-running thing in the application already uses.
type Service interface {
	// Submit records a new request, starts the operation that fulfills it, and
	// returns the request with Request.OperationID set.
	//
	// The row and the operation are written in one transaction, so a process
	// that dies between them leaves neither. The enqueue that follows is not,
	// and cannot be — see operations.Service.StartInTransaction — so a request
	// submitted at exactly the wrong moment waits for the operations recovery
	// sweep rather than for a worker. It is recorded and readable throughout.
	//
	// An erasure submitted to a Service with a confirmation window returns
	// StatusAwaitingConfirmation and an empty OperationID, and nothing runs
	// until Confirm.
	Submit(ctx context.Context, subject Subject, t RequestType) (*Request, error)

	// Get reads one request. It returns an error wrapping ErrRequestNotFound
	// when there is no such request.
	Get(ctx context.Context, requestID string) (*Request, error)

	// List pages through a subject's requests. A subject is entitled to know
	// what has been asked in their name, which is the reason this is scoped to
	// a subject rather than global.
	//
	// Ordering follows the filter's SortBy — ascending by default, as
	// filtering.DefaultQueryFilter asks. Requests are ordered by ID, which for
	// generated identifiers is submission order.
	List(ctx context.Context, subject Subject, f *filtering.QueryFilter) (*filtering.QueryFilteredResult[Request], error)

	// Confirm moves an erasure out of StatusAwaitingConfirmation and starts the
	// operation that fulfills it, returning the request with OperationID set.
	// It returns an error wrapping ErrNotAwaitingConfirmation for a request in
	// any other state, including one whose window has already lapsed.
	Confirm(ctx context.Context, requestID string) (*Request, error)

	// Cancel withdraws a request.
	//
	// An unconfirmed erasure is cancelled outright: nothing has begun and there
	// is nothing to unwind. A request already in progress has its operation
	// asked to stop, which is a request rather than a kill — the runner stops
	// between domains, at a point it can describe, and marks this row cancelled
	// when it does. So Cancel on an in-progress request returns it still
	// StatusInProgress, and the operation is where the answer arrives.
	//
	// An erasure that has begun erasing may finish anyway, and that is the
	// honest outcome rather than a gap: the erasers share one transaction and
	// the shred that precedes them cannot be undone, so the last moment at which
	// stopping means anything is before either has run.
	Cancel(ctx context.Context, requestID string) (*Request, error)

	// Download mints a time-limited URL for a completed export's artifact,
	// letting the subject fetch it from storage without the bytes passing
	// through the application. The URL expires; the artifact behind it is
	// deleted at ExpiresAt whether or not anyone fetched it.
	//
	// It returns an error wrapping ErrArtifactUnavailable for a request with no
	// artifact, ErrArtifactEncrypted when the Service encrypts artifacts at
	// rest, and ErrNoURLSigner when the storage provider cannot sign.
	Download(ctx context.Context, requestID string) (string, error)

	// Open returns a completed export's artifact as canonical JSON, reversing
	// whatever compression and encryption it was stored under. The caller must
	// close it.
	//
	// It does not stream, despite returning a reader: decryption and
	// decompression both need the whole object, so the artifact is read into
	// memory in full and the reader hands it back. Sizing follows from that —
	// see DefaultMaxDocumentBytes for the ceiling an export is built under.
	//
	// It is the path that always works — every storage provider, encrypted or
	// not — at the cost of proxying the bytes through the application. Prefer
	// Download where it is available, and reach for this when it is not.
	Open(ctx context.Context, requestID string) (io.ReadCloser, error)
}
