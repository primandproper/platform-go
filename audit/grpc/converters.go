package grpc

import (
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between audit's Go types and the generated messages.
//
// They are hand-written and one-per-type rather than reflective, for the reason
// identity/grpc's are: the interesting lines are the ones where the two
// representations disagree, and a reflective converter hides exactly those.
// Here the disagreements are three.
//
// # Scope has no proto side
//
// audit.Entry carries a scope and no message does. Going out it is dropped: a
// client already knows whose log it asked for, and telling it again would be
// the one field a converter could later be asked to read back in. Coming in
// there is nothing to read — the schema reserves the name, so a request has no
// scope to convert and this file has no function that could accept one.
//
// # A change's values are protobuf Values, and a value with no representation
// is an error
//
// audit.Change holds `any` on both sides, which is what came back out of the
// stored JSON. structpb.NewValue covers everything that round trip can produce
// and refuses what it cannot — a time.Time or a struct a caller built in
// memory. That refusal is returned rather than swallowed: a change silently
// converted to nothing is an audit entry that reads as though the field never
// changed, which is worse on this surface than on any other.
//
// # A zero time is absent, not the epoch
//
// A RecordedAt is always set and a verification's bounds may not be. All of
// them become an absent message rather than a zero timestamp and come back as
// the zero time rather than 1970 — because protobuf already has a word for
// absent, and an unbounded verification range is exactly what that word means
// here.

// timeToProto renders a stamp. The zero time is absent.
func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// timeFromProto reads a stamp. An absent message is the zero time, which every
// caller of this package reads as "unbounded" or "unset".
func timeFromProto(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.AsTime()
}

// ActorToProto renders who did the thing.
func ActorToProto(actor audit.Actor) *auditpb.Actor {
	return &auditpb.Actor{
		Id:   actor.ID,
		Type: string(actor.Type),
		Ip:   actor.IP,
	}
}

// ActorFromProto reads an actor back. An absent message is the zero Actor,
// which is an entry nobody is responsible for and is refused at recording time.
func ActorFromProto(in *auditpb.Actor) audit.Actor {
	if in == nil {
		return audit.Actor{}
	}

	return audit.Actor{
		ID:   in.GetId(),
		Type: audit.ActorType(in.GetType()),
		IP:   in.GetIp(),
	}
}

// ChangesToProto renders the per-field before and after.
//
// It reports the field whose value protobuf has no representation for rather
// than dropping it. See this file's documentation.
func ChangesToProto(changes map[string]audit.Change) (map[string]*auditpb.Change, error) {
	if changes == nil {
		return nil, nil //nolint:nilnil // an entry with no changes is the normal reading, not an error
	}

	out := make(map[string]*auditpb.Change, len(changes))

	// Ranged over keys and read through the map, rather than over pairs: a
	// Change is two interface values, and copying one per field buys nothing
	// here.
	for field := range changes {
		old, err := structpb.NewValue(changes[field].Old)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "converting the previous value of %q", field)
		}

		updated, err := structpb.NewValue(changes[field].New)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "converting the new value of %q", field)
		}

		out[field] = &auditpb.Change{OldValue: old, NewValue: updated}
	}

	return out, nil
}

// ChangesFromProto reads the per-field before and after back.
func ChangesFromProto(in map[string]*auditpb.Change) map[string]audit.Change {
	if in == nil {
		return nil
	}

	out := make(map[string]audit.Change, len(in))

	for field, change := range in {
		out[field] = audit.Change{
			Old: change.GetOldValue().AsInterface(),
			New: change.GetNewValue().AsInterface(),
		}
	}

	return out
}

// EntryToProto renders one entry, without its scope.
func EntryToProto(entry *audit.Entry) (*auditpb.Entry, error) {
	if entry == nil {
		return nil, nil //nolint:nilnil // nil in, nil out — the reading every converter in this module takes of an absent value
	}

	changes, err := ChangesToProto(entry.Changes)
	if err != nil {
		return nil, err
	}

	return &auditpb.Entry{
		Id:           entry.ID,
		RecordedAt:   timeToProto(entry.RecordedAt),
		EventType:    string(entry.EventType),
		ResourceType: entry.ResourceType,
		ResourceId:   entry.ResourceID,
		Actor:        ActorToProto(entry.Actor),
		Changes:      changes,
		Metadata:     entry.Metadata,
		PrevHash:     entry.PrevHash,
		Hash:         entry.Hash,
		Seq:          entry.Seq,
	}, nil
}

// EntriesToProto renders a page, which is what a
// filtering.QueryFilteredResult's Data holds.
//
// A nil entry in the page converts to a nil message rather than being dropped,
// so a client's page length is the one the pagination reported.
func EntriesToProto(entries []*audit.Entry) ([]*auditpb.Entry, error) {
	out := make([]*auditpb.Entry, 0, len(entries))

	for _, entry := range entries {
		converted, err := EntryToProto(entry)
		if err != nil {
			return nil, err
		}

		out = append(out, converted)
	}

	return out, nil
}

// EntryFromProto reads an entry back, which is what a client does with a
// response.
//
// The Scope it leaves zero, and there is nothing on the message to fill it from
// — see this file's documentation.
func EntryFromProto(in *auditpb.Entry) *audit.Entry {
	if in == nil {
		return nil
	}

	return &audit.Entry{
		ID:           in.GetId(),
		RecordedAt:   timeFromProto(in.GetRecordedAt()),
		EventType:    audit.EventType(in.GetEventType()),
		ResourceType: in.GetResourceType(),
		ResourceID:   in.GetResourceId(),
		Actor:        ActorFromProto(in.GetActor()),
		Changes:      ChangesFromProto(in.GetChanges()),
		Metadata:     in.GetMetadata(),
		PrevHash:     in.GetPrevHash(),
		Hash:         in.GetHash(),
		Seq:          in.GetSeq(),
	}
}

// EntriesFromProto reads a page back.
func EntriesFromProto(in []*auditpb.Entry) []*audit.Entry {
	out := make([]*audit.Entry, 0, len(in))

	for _, entry := range in {
		out = append(out, EntryFromProto(entry))
	}

	return out
}

// queryFromProto reads the narrowings a client asked for.
//
// It is unexported, and it is the one converter in this package that is: it
// deliberately does not set Query.Scope, and an exported version would be a
// function a consumer could call and then fill that field in on the result. The
// RPC sets it, off the connection, on the value this returns.
func queryFromProto(in *auditpb.EntryQuery) *audit.Query {
	if in == nil {
		return &audit.Query{}
	}

	return &audit.Query{
		ActorID:      in.GetActorId(),
		ActorType:    audit.ActorType(in.GetActorType()),
		ResourceID:   in.GetResourceId(),
		ResourceType: in.GetResourceType(),
		EventType:    audit.EventType(in.GetEventType()),
	}
}

// BreakReasonToProto renders how a chain failed. A reason this package does not
// recognize becomes UNSPECIFIED rather than being guessed at.
func BreakReasonToProto(reason audit.BreakReason) auditpb.BreakReason {
	switch reason {
	case audit.BreakContentAltered:
		return auditpb.BreakReason_BREAK_REASON_CONTENT_ALTERED
	case audit.BreakLinkMismatch:
		return auditpb.BreakReason_BREAK_REASON_LINK_MISMATCH
	case audit.BreakMissingEntry:
		return auditpb.BreakReason_BREAK_REASON_MISSING_ENTRY
	default:
		return auditpb.BreakReason_BREAK_REASON_UNSPECIFIED
	}
}

// BreakReasonFromProto reads it back. UNSPECIFIED and anything unrecognized are
// the empty reason, which is what a Break carries when there is no break.
func BreakReasonFromProto(reason auditpb.BreakReason) audit.BreakReason {
	switch reason {
	case auditpb.BreakReason_BREAK_REASON_CONTENT_ALTERED:
		return audit.BreakContentAltered
	case auditpb.BreakReason_BREAK_REASON_LINK_MISMATCH:
		return audit.BreakLinkMismatch
	case auditpb.BreakReason_BREAK_REASON_MISSING_ENTRY:
		return audit.BreakMissingEntry
	case auditpb.BreakReason_BREAK_REASON_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

// BreakToProto renders where and how a chain stopped verifying.
func BreakToProto(in *audit.Break) *auditpb.Break {
	if in == nil {
		return nil
	}

	return &auditpb.Break{
		EntryId:  in.EntryID,
		Reason:   BreakReasonToProto(in.Reason),
		Expected: in.Expected,
		Actual:   in.Actual,
		Seq:      in.Seq,
	}
}

// BreakFromProto reads it back.
func BreakFromProto(in *auditpb.Break) *audit.Break {
	if in == nil {
		return nil
	}

	return &audit.Break{
		EntryID:  in.GetEntryId(),
		Reason:   BreakReasonFromProto(in.GetReason()),
		Expected: in.GetExpected(),
		Actual:   in.GetActual(),
		Seq:      in.GetSeq(),
	}
}

// VerificationResultToProto renders what a verification found, without the
// scope it walked: it is the caller's own, and the caller supplied it.
//
// Intact is not carried. It is a method on the Go type rather than a field so
// that it cannot disagree with FirstBreak, and a bool on the message would be
// exactly the field that can — a report calling a broken log clean.
func VerificationResultToProto(in *audit.VerificationResult) *auditpb.VerificationResult {
	if in == nil {
		return nil
	}

	return &auditpb.VerificationResult{
		From:       timeToProto(in.From),
		To:         timeToProto(in.To),
		FirstBreak: BreakToProto(in.FirstBreak),
		Checked:    in.Checked,
	}
}

// VerificationResultFromProto reads a verification back, which is what a client
// calls Intact on.
//
// The Scope is left zero. A client holds the scope it connected as and this
// message never carried one; filling it in from somewhere would be this
// converter inventing the fact the surface exists to bind.
func VerificationResultFromProto(in *auditpb.VerificationResult) *audit.VerificationResult {
	if in == nil {
		return nil
	}

	return &audit.VerificationResult{
		From:       timeFromProto(in.GetFrom()),
		To:         timeFromProto(in.GetTo()),
		FirstBreak: BreakFromProto(in.GetFirstBreak()),
		Checked:    in.GetChecked(),
	}
}
