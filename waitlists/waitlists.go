package waitlists

import (
	"strings"
	"time"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"
)

const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "waitlists"

	scopeKey     = serviceName + ".scope"
	listKey      = serviceName + ".list"
	signupKey    = serviceName + ".signup"
	statusKey    = serviceName + ".status"
	countKey     = serviceName + ".count"
	subjectKey   = serviceName + ".subject_type"
	subjectIDKey = serviceName + ".subject_id"
)

// Status is where one signup stands.
//
// It is a closed set, unlike [SubjectType], and the two differ for a reason
// worth stating: a subject type names a kind of principal, which is the
// application's to invent, while a status decides which transitions the store
// will make and what a withdrawal means. A status this package does not
// implement is a row nothing can move.
type Status string

const (
	// StatusWaiting is somebody who has joined and not yet been invited. It is
	// where every signup starts.
	StatusWaiting Status = "waiting"
	// StatusInvited is somebody who has been let in and has not yet taken it
	// up. [SignupStore.Invite] is what puts them here, and
	// [Signup.StatusChangedAt] is when — which is the column a reminder is
	// scheduled off.
	StatusInvited Status = "invited"
	// StatusConverted is somebody who took the invitation up. The waitlist has
	// done its job and whatever they became is somebody else's row.
	StatusConverted Status = "converted"
	// StatusWithdrawn is somebody who asked to come off the list, and it is the
	// status this package is shaped around.
	//
	// It is the one status nothing moves out of, and it is a suppression rather
	// than a deletion. The row keeps its contact digest and loses everything
	// else that identifies a person, so a later signup from the same address is
	// recognized and refused instead of quietly re-subscribing whoever asked to
	// be left alone. See the package documentation.
	StatusWithdrawn Status = "withdrawn"
)

// Valid reports whether s is one of the four statuses.
//
// It is exported for the caller decoding one out of a request or a stored
// document, which is the only place a status this package does not implement can
// come from: the store writes the column itself and every transition names both
// ends of its move.
func (s Status) Valid() bool {
	switch s {
	case StatusWaiting, StatusInvited, StatusConverted, StatusWithdrawn:
		return true
	default:
		return false
	}
}

// String renders the status as it is stored.
func (s Status) String() string { return string(s) }

// SubjectType distinguishes the kinds of thing a signup can belong to.
//
// Like settings.SubjectType and audit.ActorType this is a bare string with
// suggested constants rather than a closed set: an application whose signups
// hang off a third kind of principal — a device, a workspace, an API client —
// should say so rather than misfile it as one of these.
type SubjectType string

const (
	// SubjectUser is a signup made by somebody who already has an account.
	SubjectUser SubjectType = "user"
	// SubjectAccount is a signup made on an account's behalf, for a list a
	// whole organization is queueing for.
	SubjectAccount SubjectType = "account"
)

// String renders the subject type as it is stored.
func (t SubjectType) String() string { return string(t) }

// Subject is who a signup belongs to, where anybody does.
//
// The zero value is the ordinary case: a pre-launch list has an address and
// nothing else, and a signup that names nobody is stored with both columns
// empty. It is two fields rather than one composite string for the reason the
// tenancy doctrine gives for the scope: a key spelling "user:abc123" carries two
// facts in a column that can only be indexed as one.
type Subject struct {
	// Type says what kind of principal this is.
	Type SubjectType `json:"type"`
	// ID identifies the principal within that type.
	ID string `json:"id"`
}

// Anonymous reports whether the subject names nobody, which is what a signup
// carrying only a contact looks like.
func (s Subject) Anonymous() bool { return s.Type == "" && s.ID == "" }

// Validate reports whether the subject is one this package can store: either
// wholly absent, or naming both halves.
//
// Half a subject is refused rather than stored, because the read that finds one
// binds both columns — a signup with a type and no id is a row nothing will ever
// list, and a signup with an id and no type is a row the wrong list would find.
func (s Subject) Validate() error {
	switch {
	case s.Anonymous():
		return nil
	case s.Type == "":
		return ErrEmptySubjectType
	case s.ID == "":
		return ErrEmptySubjectID
	default:
		return nil
	}
}

// List is a waitlist: a named queue people join before the thing they are
// queueing for exists.
//
// Lists are administrative rows. Nothing on a public request path creates one —
// which lists a deployment runs is a decision, in the same sense that a product
// launch is — and what a public path does is read one and add a signup to it.
type List struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the list was opened. It is the database's clock rather
	// than the application's, read back by the write — see waitlists/migrations.
	CreatedAt time.Time `json:"createdAt"`

	// ClosesAt is when the list stops taking signups. Required.
	//
	// It is a value rather than a pointer, and the column behind it is NOT NULL,
	// which is the one shape in this package worth arguing about — see the
	// package documentation. A list whose end is not yet decided names a far
	// horizon and is brought in by an update; a list that should stop this
	// instant is archived.
	ClosesAt time.Time `json:"closesAt"`

	// LastUpdatedAt is when the list last changed, or nil for one that has not
	// been edited.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the list was retired. An archived list is excluded from
	// every read that does not ask for archived rows, and takes no further
	// signups; the signups already against it are left alone, because archiving
	// is not erasure.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the list. Minted on write when empty.
	ID string `json:"id"`

	// Name is what the list is called, for whoever administers it and for
	// whatever renders the signup form. Required.
	//
	// It is not unique and is not a handle: a list is addressed by its id
	// everywhere in this package, so two lists may share a name and neither is
	// reachable by it.
	Name string `json:"name"`

	// Description is prose about what people are queueing for.
	Description string `json:"description"`

	// Scope is whose list this is. See the tenancy package.
	Scope tenancy.Scope `json:"scope"`
}

// OpenAt reports whether the list was taking signups at t: not archived, and not
// yet closed.
//
// It is the same question [SignupStore.Join] asks before it writes and the same
// one ListOpenLists pages by, spelled once here so a caller rendering a form and
// the store refusing a write cannot disagree about the boundary. The boundary is
// exclusive on the open side — a list whose closing instant is exactly t is
// closed — which is the reading that leaves no instant at which a list is
// neither open nor closed.
func (l *List) OpenAt(t time.Time) bool {
	return l != nil && l.ArchivedAt == nil && l.ClosesAt.After(t)
}

// validate reports whether the list is one this package can store.
func (l *List) validate() error {
	if l == nil {
		return ErrNilList
	}

	if l.Name == "" {
		return ErrEmptyListName
	}

	if l.ClosesAt.IsZero() {
		return ErrEmptyClosesAt
	}

	return nil
}

// Signup is one person's place on one list.
type Signup struct {
	_ struct{} `json:"-"`

	// CreatedAt is when they joined, which is also the order they joined in —
	// the id sorts by creation time, and every page here walks it.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the row last changed, or nil for one nobody has
	// touched since it was written.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// StatusChangedAt is when the signup last moved through the lifecycle, or
	// nil for one that is still where it started.
	//
	// It is not LastUpdatedAt, and the difference is the point: an administrator
	// fixing a typo in Notes changes the row without moving anybody, and the
	// reminder that goes out three days after an invitation is scheduled off
	// this column.
	StatusChangedAt *time.Time `json:"statusChangedAt"`

	// ArchivedAt is when the signup was retired administratively. It is not a
	// withdrawal — see [StatusWithdrawn], which is what somebody asking to come
	// off the list gets.
	ArchivedAt *time.Time `json:"archivedAt"`

	// Subject is who the signup belongs to, where anybody does. The zero value
	// is a signup that names nobody, which is the ordinary case.
	Subject Subject `json:"subject"`

	// ID identifies the signup. Minted on write when empty.
	ID string `json:"id"`

	// ListID is the list this signup is for.
	ListID string `json:"listID"`

	// Contact is the address the list exists to write to, as it was given —
	// [Normalize] is what the digest is taken of, not what this holds, so a
	// mail client renders the capitalization somebody typed.
	//
	// It is empty for a withdrawn signup, which is the whole of what a
	// withdrawal erases from this column.
	Contact string `json:"contact"`

	// ContactDigest is what the row is found by and what survives a withdrawal.
	//
	// It is [SQLStore.Digest] of [Normalize] of the contact, and it is
	// exported because a deployment migrating off a hand-written table has to
	// write the column from the addresses it is holding. It is not reversible,
	// so a caller holding one holds nothing.
	ContactDigest string `json:"contactDigest"`

	// Notes is whatever whoever administers the list wrote about this signup.
	// It is empty for a withdrawn signup.
	Notes string `json:"notes"`

	// Status is where the signup stands.
	Status Status `json:"status"`

	// Scope is whose list this signup is on.
	Scope tenancy.Scope `json:"scope"`
}

// Withdrawn reports whether the signup has been taken off the list.
func (s *Signup) Withdrawn() bool { return s != nil && s.Status == StatusWithdrawn }

// Normalize renders a contact as it is digested: trimmed of surrounding space
// and folded to lower case.
//
// It is exported because it is half of what a caller needs to reproduce a
// digest, and because it is the answer to "why did my signup collide". Two
// people typing Ada@Example.com and ada@example.com are one person, and a
// suppression that missed the second would be a suppression that did not work.
//
// It goes no further than that on purpose. Plus-addressing, dots in a Gmail
// local part and unicode normalization are each a provider's own policy about
// which addresses are the same mailbox, and a library that guessed would be
// merging two people's signups at some providers and splitting one person's at
// others. What is stored in Contact is what the caller passed, so the address
// the list writes to is the address somebody gave it.
func Normalize(contact string) string {
	return strings.ToLower(strings.TrimSpace(contact))
}

// validateContact reports whether a contact is one this package can store.
func validateContact(contact string) error {
	if Normalize(contact) == "" {
		return ErrEmptyContact
	}

	return nil
}

// requireID reports an invalid id for the empty string, which is the one input
// every keyed method here shares.
func requireID(id string) error {
	if id == "" {
		return platformerrors.ErrInvalidIDProvided
	}

	return nil
}

// matchScope reports a write whose entity names a different tenant than the
// scope the call named.
//
// The argument is what the statement binds, so the entity's own Scope is checked
// against it rather than read in its place — see [Store] for why that direction
// and not the other. An entity naming no scope is one that adopts the argument,
// which is the ordinary case for a value the caller has just assembled;
// tenancy.Scope tells its zero value apart from Global(), so "unset" here is
// genuinely unset rather than the global scope written shortly. The entity is
// named in the message because a caller holding both a list and a signup has two
// places to have got it wrong.
func matchScope(scope, named tenancy.Scope, entity string) error {
	if named != (tenancy.Scope{}) && named != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"%s names %q, the write names %q", entity, named, scope)
	}

	return nil
}
