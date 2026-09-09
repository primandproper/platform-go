package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestEntryRoundTrip is what a client sees: everything the Go type carries
// survives the wire except the scope, which the message deliberately has no
// room for.
func TestEntryRoundTrip(T *testing.T) {
	T.Parallel()

	want := &audit.Entry{
		ID:           "audit_1",
		RecordedAt:   time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		EventType:    audit.EventUpdated,
		ResourceType: "recipe",
		ResourceID:   "recipe_1",
		Scope:        "acct_ours",
		Actor:        audit.Actor{ID: "user_1", Type: audit.ActorUser, IP: "203.0.113.7"},
		Changes: map[string]audit.Change{
			"name":     {Old: "Soup", New: "Stew"},
			"servings": {Old: float64(2), New: float64(4)},
			"created":  {New: "yes"},
			"deleted":  {Old: "was"},
		},
		Metadata: map[string]string{"reason": "a typo"},
		PrevHash: "abc",
		Hash:     "def",
		Seq:      3,
	}

	rendered, err := auditgrpc.EntryToProto(want)
	must.NoError(T, err)

	got := auditgrpc.EntryFromProto(rendered)
	must.NotNil(T, got)

	test.EqOp(T, want.ID, got.ID)
	test.EqOp(T, want.RecordedAt, got.RecordedAt.UTC())
	test.EqOp(T, want.EventType, got.EventType)
	test.EqOp(T, want.ResourceType, got.ResourceType)
	test.EqOp(T, want.ResourceID, got.ResourceID)
	test.EqOp(T, want.Actor, got.Actor)
	test.Eq(T, want.Changes, got.Changes)
	test.Eq(T, want.Metadata, got.Metadata)
	test.EqOp(T, want.PrevHash, got.PrevHash)
	test.EqOp(T, want.Hash, got.Hash)
	test.EqOp(T, want.Seq, got.Seq)

	// The one field that does not survive, and the whole reason it does not.
	// There is nothing on the message to read it from, so a client cannot be
	// told whose log this is and a converter cannot be asked to honor a scope
	// a request named.
	test.EqOp(T, "", got.Scope)
}

// TestEntryToProtoReportsAnUnrepresentableChange is the one conversion here that
// can fail. A change silently dropped would be an audit entry that reads as
// though the field never changed, which is worse on this surface than on any
// other.
func TestEntryToProtoReportsAnUnrepresentableChange(T *testing.T) {
	T.Parallel()

	// A time.Time is not a JSON value, so it is not a structpb one either. It
	// cannot come back out of the stored log — everything there went through
	// JSON — but it can be handed straight to a converter by a caller who built
	// the entry in memory.
	rendered, err := auditgrpc.EntryToProto(&audit.Entry{
		Changes: map[string]audit.Change{"when": {New: time.Now()}},
	})
	test.Error(T, err)
	test.Nil(T, rendered)
	test.StrContains(T, err.Error(), "when")
}

// TestZeroTimesAreAbsent covers the disagreement between the two
// representations: protobuf has a word for absent and time.Time does not, so a
// zero stamp is a missing message rather than 1970.
func TestZeroTimesAreAbsent(T *testing.T) {
	T.Parallel()

	rendered, err := auditgrpc.EntryToProto(&audit.Entry{ID: "audit_1"})
	must.NoError(T, err)
	test.Nil(T, rendered.GetRecordedAt())

	test.EqOp(T, time.Time{}, auditgrpc.EntryFromProto(rendered).RecordedAt)

	result := auditgrpc.VerificationResultToProto(&audit.VerificationResult{})
	test.Nil(T, result.GetFrom())
	test.Nil(T, result.GetTo())
}

func TestNilConversions(T *testing.T) {
	T.Parallel()

	entry, err := auditgrpc.EntryToProto(nil)
	must.NoError(T, err)
	test.Nil(T, entry)

	test.Nil(T, auditgrpc.EntryFromProto(nil))
	test.Nil(T, auditgrpc.BreakToProto(nil))
	test.Nil(T, auditgrpc.BreakFromProto(nil))
	test.Nil(T, auditgrpc.VerificationResultToProto(nil))
	test.Nil(T, auditgrpc.VerificationResultFromProto(nil))
	test.EqOp(T, audit.Actor{}, auditgrpc.ActorFromProto(nil))

	changes, err := auditgrpc.ChangesToProto(nil)
	must.NoError(T, err)
	test.Nil(T, changes)
	test.Nil(T, auditgrpc.ChangesFromProto(nil))

	entries, err := auditgrpc.EntriesToProto(nil)
	must.NoError(T, err)
	test.SliceEmpty(T, entries)
	test.SliceEmpty(T, auditgrpc.EntriesFromProto(nil))
}

// TestVerificationResultRoundTrip pins the one field the message deliberately
// does not carry: a chain held together exactly when there is no first break,
// and a bool saying so is a field that can disagree with it.
func TestVerificationResultRoundTrip(T *testing.T) {
	T.Parallel()

	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)

	intact := auditgrpc.VerificationResultFromProto(
		auditgrpc.VerificationResultToProto(&audit.VerificationResult{
			From: from, To: to, Checked: 12,
		}))
	must.NotNil(T, intact)
	test.EqOp(T, from, intact.From.UTC())
	test.EqOp(T, to, intact.To.UTC())
	test.EqOp(T, 12, intact.Checked)
	test.True(T, intact.Intact())

	broken := auditgrpc.VerificationResultFromProto(
		auditgrpc.VerificationResultToProto(&audit.VerificationResult{
			Checked: 4,
			FirstBreak: &audit.Break{
				EntryID:  "audit_2",
				Reason:   audit.BreakLinkMismatch,
				Expected: "abc",
				Actual:   "def",
				Seq:      2,
			},
		}))
	must.NotNil(T, broken)
	test.False(T, broken.Intact())
	test.EqOp(T, audit.BreakLinkMismatch, broken.FirstBreak.Reason)
	test.EqOp(T, "audit_2", broken.FirstBreak.EntryID)
	test.EqOp(T, int64(2), broken.FirstBreak.Seq)

	// The Scope is left zero coming back: a client holds the scope it connected
	// as, and filling one in here would be the converter inventing the fact the
	// surface exists to bind.
	test.EqOp(T, "", broken.Scope.Owner())
}

// TestBreakReasonsRoundTrip covers the one closed set in this schema. It is an
// enum where the event and actor vocabularies are strings, because a fourth way
// for a chain to break would be this package finding one rather than a consumer
// naming one.
func TestBreakReasonsRoundTrip(T *testing.T) {
	T.Parallel()

	for _, reason := range []audit.BreakReason{
		audit.BreakContentAltered,
		audit.BreakLinkMismatch,
		audit.BreakMissingEntry,
	} {
		test.EqOp(T, reason, auditgrpc.BreakReasonFromProto(auditgrpc.BreakReasonToProto(reason)))
	}

	// A reason this package does not recognize becomes UNSPECIFIED rather than
	// being guessed at, and UNSPECIFIED comes back as no reason at all.
	test.EqOp(T, auditpb.BreakReason_BREAK_REASON_UNSPECIFIED, auditgrpc.BreakReasonToProto("sideways"))
	test.EqOp(T, audit.BreakReason(""), auditgrpc.BreakReasonFromProto(auditpb.BreakReason(9999)))
}
