package mediaregistry

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// The keys this package attaches to spans and log lines. Declared once so a
// trace and a log line name the same fact the same way.
const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "uploads_registry"

	scopeKey     = serviceName + ".scope"
	objectIDKey  = serviceName + ".object_id"
	objectKeyKey = serviceName + ".object_key"
	ownerIDKey   = serviceName + ".owner_id"
	subjectKey   = serviceName + ".belongs_to"
	countKey     = serviceName + ".count"
)

// ObjectAttributeKey is the metric and span attribute a caller labels its own
// instruments with when the thing being measured is about one registered
// object. It is exported so a consumer's attributes agree with this package's
// rather than merely resembling them.
const ObjectAttributeKey = objectIDKey

// Subject is what an object hangs off in the consumer's own schema: an avatar's
// user, a receipt's invoice, an attachment's ticket.
//
// It is one value rather than two fields because the pair is one key. An id
// without its type is an id that means something else in another one of the
// consumer's tables, so a list keyed on the id alone would return one caller's
// receipts beside another's avatars — and a signature taking two strings is a
// signature whose arguments can be passed in the wrong order.
//
// Type is the consumer's own word for the kind of thing — this package neither
// knows nor validates the vocabulary, because a registry that could only
// reference identity's tables would be a registry only identity's consumers
// could use. The zero Subject is an object attached to nothing, which is the
// ordinary state of a standalone upload.
type Subject struct {
	_ struct{} `json:"-"`

	// Type is the kind of thing the object hangs off, in the consumer's words.
	Type string `json:"type"`

	// ID identifies the thing, within its type.
	ID string `json:"id"`
}

// Attached reports whether the subject names something.
//
// Both halves are required together: a type with no id names a table rather
// than a row, and an id with no type names a row in no particular table. Either
// alone is a value nothing can look up, so [Subject.Validate] refuses it and
// this reports the pair.
func (s Subject) Attached() bool { return s.Type != "" && s.ID != "" }

// Validate refuses a half-filled subject.
func (s Subject) Validate() error {
	if s.Type == "" && s.ID == "" {
		return nil
	}

	if s.Attached() {
		return nil
	}

	return ErrPartialSubject
}

// String renders the subject as "type:id", for a span attribute. An unattached
// subject renders empty.
func (s Subject) String() string {
	if !s.Attached() {
		return ""
	}

	return s.Type + ":" + s.ID
}

// Object is one row of the registry: what an object in storage is, as opposed
// to the bytes themselves.
//
// It is deliberately not a handle on those bytes. Nothing here opens, reads or
// removes an object — uploads.UploadManager does that, keyed on Key — and the
// separation is what lets a consumer store and register in either order, or
// register something a different process stored. What this type is for is the
// question the bucket cannot answer: whether the caller in front of you may
// have the object at this key, which is decided from OwnerID and Scope.
type Object struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the object was registered, stamped by the database and
	// read back — see the store's RecordObject, which is where a caller gets a
	// value with this field filled in.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the row last changed. It is always nil today: no
	// statement this package emits assigns a column after the insert, because
	// every column is a fact about bytes that are already in a bucket. The
	// field and the column are here because the filter window compares against
	// them and because the convention triple is the convention — see
	// mediaregistry/internal/queries.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the row was soft-deleted. Archival here is
	// metadata-only: the object is still in the bucket until the consumer's
	// retention policy removes it. See the retention package.
	ArchivedAt *time.Time `json:"archivedAt"`

	// BelongsTo is what the object hangs off, if anything.
	BelongsTo Subject `json:"belongsTo"`

	// ID identifies the row. Assigned by RecordObject when the object handed to
	// it has none, and reported on the object that call returns rather than
	// written back onto the argument; a caller that has already minted one — to
	// reference the upload from a row it is writing in the same request — keeps
	// it.
	ID string `json:"id"`

	// Key is where the bytes live, as uploads.UploadManager.Save was given it.
	// It is unique within a scope, archived rows included: two rows for one
	// object is the drift this table exists to prevent.
	Key string `json:"key"`

	// ContentType is what the object is — "image/png", "application/pdf" — as
	// stored. Empty is legitimate for a provider that sniffed it and did not
	// report back.
	ContentType string `json:"contentType"`

	// OwnerID is whoever the consumer's authorization model calls a principal:
	// a user, a service account, an API key. This package does not know which,
	// which is why the column carries no foreign key.
	OwnerID string `json:"ownerID"`

	// Scope is whose object this is. tenancy.Global() for a single-tenant
	// application, which is then exactly what it would have been without the
	// column.
	//
	// A write takes the scope as an argument and the row it returns carries it
	// here. There is nothing to set it to on the way in: a write takes an
	// [ObjectInput], which has no Scope, so this field is only ever what a read
	// filled in.
	Scope tenancy.Scope `json:"scope"`

	// Size is how many bytes were stored, counted while they went past rather
	// than taken from a client's Content-Length. A quota read off claimed sizes
	// is a quota that does not hold.
	Size int64 `json:"size"`
}

// ObjectInput is what a caller supplies to register an object: the facts about
// bytes that are already in a bucket, and nothing the write settles.
//
// It is a separate type from [Object] rather than the same one, and the reason
// is that the two are answers to different questions. An Object is a row — what
// the registry holds, stamps included. An ObjectInput is a request to write one.
// Sharing a type between them makes every field the write settles look like a
// field the caller may set, and makes the argument a caller assembled
// indistinguishable, to the compiler, from the row that came back.
//
// That indistinguishability is the whole cost. A caller that passed its own
// argument onward — to a response, an audit entry, a cache — passed a struct
// whose CreatedAt is the zero time and, after [StoreAndRecord], whose Size is
// zero, and every line of it compiled. The fields are absent here so that the
// mistake is a type error at the point it is made rather than a zero value on
// the wire.
//
// Scope is absent for the same reason and one more: the write binds the scope it
// was called with, so an ObjectInput has no second copy to disagree with it.
// There is no reconciliation, and so no sentinel for one.
//
// It is taken by value. There is no nil to check and no way for a write to
// modify what a caller still holds.
type ObjectInput struct {
	_ struct{} `json:"-"`

	// BelongsTo is what the object hangs off, if anything.
	BelongsTo Subject `json:"belongsTo"`

	// ID identifies the row. Optional: [Store.RecordObject] mints one when this
	// is empty, and reports whichever it used on the row it returns. A caller
	// that has already minted one keeps it — a storage key is often built from
	// the id, so the bytes have to know where they are going before anything
	// writes them.
	ID string `json:"id"`

	// Key is where the bytes live, as uploads.UploadManager.Save was given it.
	// Required. It is unique within a scope, archived rows included.
	Key string `json:"key"`

	// ContentType is what the object is — "image/png", "application/pdf" — as
	// stored. Empty is legitimate for a provider that sniffed it and did not
	// report back.
	ContentType string `json:"contentType"`

	// OwnerID is whoever the consumer's authorization model calls a principal.
	// Required.
	OwnerID string `json:"ownerID"`

	// Size is how many bytes were stored. [StoreAndRecord] fills it in from what
	// actually went past and ignores whatever was here; a caller registering
	// bytes somebody else stored sets it themselves.
	Size int64 `json:"size"`
}

var _ validation.ValidatableWithContext = (*ObjectInput)(nil)

// ValidateWithContext refuses a row that could not answer the question the
// registry exists for.
//
// Key and OwnerID are required: a row with no key names no object, and a row
// with no owner cannot decide who may read it, which makes it worse than no row
// at all — an access check that reads it finds an owner nobody matches, or, in
// the version somebody writes to make the check pass, one everybody does.
//
// It is on the input rather than on [Object] because validating is a write-time
// question. An Object is a row the database answered with, and there is no
// useful thing to tell a caller about one of those that the statement did not
// already enforce.
//
// The scope is not checked here. It is validated where it is bound, by
// tenancy.Scope itself, so an unset scope is a driver error on every statement
// rather than a rule this one type remembers.
func (in *ObjectInput) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, in,
		validation.Field(&in.Key, validation.Required),
		validation.Field(&in.OwnerID, validation.Required),
		validation.Field(&in.Size, validation.Min(0)),
	); err != nil {
		return err
	}

	return in.BelongsTo.Validate()
}
