package grpc

import (
	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between waitlists' types and the generated messages.
//
// The exported halves are the ones a consumer composing this domain into a
// larger response would otherwise write again — an admin console assembling a
// page, a client turning a stored row into the same message this service sends —
// and getting one of the assignments wrong is exactly what an exported converter
// prevents.
//
// The unexported halves read requests, and every one of them is narrower than
// the message it reads. That narrowness is the schema's decision arriving here:
// a request names an address and a list, and never a scope, a subject, a status
// or a digest, so there is nothing in these functions to forget to ignore.

// statusToProto renders a stored status for the wire.
//
// A status this package does not implement cannot come off a row — the store
// writes the column itself and every transition names both ends of its move — so
// the unspecified arm is reachable only from a value assembled in process, and
// it is the honest rendering of one: a client is told the field was not set
// rather than told the wrong status.
func statusToProto(s waitlists.Status) waitlistspb.SignupStatus {
	switch s {
	case waitlists.StatusWaiting:
		return waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING
	case waitlists.StatusInvited:
		return waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED
	case waitlists.StatusConverted:
		return waitlistspb.SignupStatus_SIGNUP_STATUS_CONVERTED
	case waitlists.StatusWithdrawn:
		return waitlistspb.SignupStatus_SIGNUP_STATUS_WITHDRAWN
	default:
		return waitlistspb.SignupStatus_SIGNUP_STATUS_UNSPECIFIED
	}
}

// ListToProto renders a waitlist for the wire.
//
// It carries no scope, and that is not an omission: the catalog is the caller's
// own, resolved off their principal or their connection, so a response naming it
// would be telling a client something it supplied.
func ListToProto(l *waitlists.List) *waitlistspb.Waitlist {
	if l == nil {
		return nil
	}

	out := &waitlistspb.Waitlist{
		CreatedAt:   timestamppb.New(l.CreatedAt),
		ClosesAt:    timestamppb.New(l.ClosesAt),
		Id:          l.ID,
		Name:        l.Name,
		Description: l.Description,
	}

	// The two nullable times stay unset rather than becoming the zero
	// timestamp: a client rendering "last updated" wants to know there was no
	// update, and 1970 is not that answer. ClosesAt above is not among them —
	// the column is NOT NULL, and a list with no closing time is refused rather
	// than stored.
	if l.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*l.LastUpdatedAt)
	}

	if l.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*l.ArchivedAt)
	}

	return out
}

// ListsToProto renders a page of waitlists.
func ListsToProto(lists []*waitlists.List) []*waitlistspb.Waitlist {
	out := make([]*waitlistspb.Waitlist, 0, len(lists))
	for _, l := range lists {
		out = append(out, ListToProto(l))
	}

	return out
}

// SubjectToProto renders a signup's subject.
//
// A subject naming nobody renders as a message with both fields empty rather
// than as an absent one, so that a client reading it does not have to tell "no
// subject" apart from "no subject field" — the store already distinguishes the
// anonymous subject from half a subject, and this keeps that the only
// distinction there is.
func SubjectToProto(s waitlists.Subject) *waitlistspb.SignupSubject {
	return &waitlistspb.SignupSubject{Type: s.Type.String(), Id: s.ID}
}

// subjectFromProto reads the subject a request named.
//
// A nil message is the zero Subject, which the store reads as naming nobody.
// That is the right answer for the read — a page of signups belonging to nobody
// is a page — and the wrong one for the erasure, so the handler that cannot
// accept it refuses the nil message itself rather than having this return one
// more error nobody would read differently.
func subjectFromProto(in *waitlistspb.SignupSubject) waitlists.Subject {
	if in == nil {
		return waitlists.Subject{}
	}

	return waitlists.Subject{
		Type: waitlists.SubjectType(in.GetType()),
		ID:   in.GetId(),
	}
}

// SignupToProto renders a signup for the wire.
//
// It carries no scope and no contact digest. The digest's absence is the one
// worth stating: it is unsalted over a fast hash, deliberately, so a client
// holding one could test any address it liked against it offline —
// waitlists.SQLStore.Digest still exports it in process, to the one caller that
// already holds the addresses. waitlists.proto reserves the name so there is
// nowhere for it to come back.
//
// A withdrawn signup renders with its contact and notes empty, which is what the
// row holds: a withdrawal erases them and keeps the digest, so the suppression
// outlives the address.
func SignupToProto(s *waitlists.Signup) *waitlistspb.Signup {
	if s == nil {
		return nil
	}

	out := &waitlistspb.Signup{
		CreatedAt: timestamppb.New(s.CreatedAt),
		Subject:   SubjectToProto(s.Subject),
		Id:        s.ID,
		ListId:    s.ListID,
		Contact:   s.Contact,
		Notes:     s.Notes,
		Status:    statusToProto(s.Status),
	}

	if s.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*s.LastUpdatedAt)
	}

	// StatusChangedAt is the one whose absence a client acts on rather than
	// merely renders: it is nil for a signup still where it started, and a
	// reminder scheduled off the epoch would go out immediately.
	if s.StatusChangedAt != nil {
		out.StatusChangedAt = timestamppb.New(*s.StatusChangedAt)
	}

	if s.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*s.ArchivedAt)
	}

	return out
}

// SignupsToProto renders a page of signups.
func SignupsToProto(signups []*waitlists.Signup) []*waitlistspb.Signup {
	out := make([]*waitlistspb.Signup, 0, len(signups))
	for _, s := range signups {
		out = append(out, SignupToProto(s))
	}

	return out
}

// listFromProto reads the list a create or an update named.
//
// A nil message is nil rather than an empty list, so a request that named none
// is refused as malformed instead of reaching the store as a list with no name —
// which the store would refuse anyway, with a message about the name rather than
// about the request.
//
// The scope is deliberately left unset. waitlists.Store takes the scope as an
// argument and refuses an entity that names a different one; leaving it empty is
// how a value the handler has just assembled adopts the argument, which is the
// case the store's ErrScopeMismatch documentation calls ordinary.
func listFromProto(in *waitlistspb.WaitlistInput) *waitlists.List {
	if in == nil {
		return nil
	}

	out := &waitlists.List{
		ID:          in.GetId(),
		Name:        in.GetName(),
		Description: in.GetDescription(),
	}

	// An unset closing time stays the zero instant rather than becoming the
	// epoch, so the store refuses it with ErrEmptyClosesAt — which names the
	// field — instead of opening a list that closed in 1970.
	if closes := in.GetClosesAt(); closes != nil {
		out.ClosesAt = closes.AsTime()
	}

	return out
}

// signupFromJoin builds the signup a join request describes: the address it
// named, and the subject the caller is.
//
// The subject comes off the principal and never off the request, which is the
// reading webhooks/grpc takes of an endpoint's provenance and identity/grpc's
// pattern section states in general. A signup attributed to somebody the caller
// merely named is a signup that says whatever the caller wanted it to; an
// anonymous request names nobody, which is the ordinary case for a pre-launch
// list and is a subject the store stores as two empty columns.
//
// Nothing else is read. The status and every timestamp are the store's, the
// notes are the operator's column, and the list is an argument to the write.
func signupFromJoin(in *waitlistspb.JoinRequest, caller Principal) *waitlists.Signup {
	if in == nil {
		return nil
	}

	out := &waitlists.Signup{Contact: in.GetContact()}

	if caller != nil && caller.UserID() != "" {
		out.Subject = waitlists.Subject{Type: waitlists.SubjectUser, ID: caller.UserID()}
	}

	return out
}
