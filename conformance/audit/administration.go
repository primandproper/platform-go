package audit

import (
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/audit/auditpb"
	"github.com/primandproper/platform-go/v15/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The operator's reads of every tenant's log.
const (
	getAnyEntry    = auditpb.AuditAdministrationService_GetAnyEntry_FullMethodName
	listAnyEntries = auditpb.AuditAdministrationService_ListAnyEntries_FullMethodName
)

// administration asserts AuditAdministrationService from the operator's side:
// it reaches past the operator's own chain, names the tenant each entry is in,
// and leaves a record of the read in the operator's own chain.
//
// That a member is refused it is not asserted here. It is the reservations
// suite's, which holds every call the subject names in Seams.OperatorMethods to
// a refusal, and a deployment that grants these to nobody but its staff names
// them there.
func administration(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an operator reads another session's entry by id, and the read is recorded in their own chain", func(t *testing.T) {
		t.Parallel()

		op, theirs, act := twoChains(t, s, listEntries, getAnyEntry)
		if op.Surfaces.AuditAdministration == nil {
			conformance.Skip(t, "conformance: this subject serves no operator's read of the audit log; skipping")
		}

		neighbor := findEntry(t, theirs, act(t, theirs))
		must.NotNil(t, neighbor, must.Sprint("the neighbor's own action recorded nothing to read"))

		// The control: the operator's own entry, through the same method, so
		// that the owners compared below are two answers of one server.
		own := findEntry(t, op, act(t, op))
		must.NotNil(t, own, must.Sprint("the operator's own action recorded nothing"))

		ctx := op.Context(t.Context())

		mine, err := op.Surfaces.AuditAdministration.GetAnyEntry(ctx, &auditpb.GetAnyEntryRequest{EntryId: own.GetId()})
		must.NoError(t, err, must.Sprint("an operator could not read their own entry through the operator's read"))

		read, err := op.Surfaces.AuditAdministration.GetAnyEntry(ctx, &auditpb.GetAnyEntryRequest{EntryId: neighbor.GetId()})
		must.NoError(t, err, must.Sprint("an operator could not read another session's entry"))
		test.EqOp(t, neighbor.GetId(), read.GetEntry().GetEntry().GetId())
		test.NotEqOp(t, mine.GetEntry().GetOwnerId(), read.GetEntry().GetOwnerId(),
			test.Sprint("two sessions' entries named one owner; the answer does not say whose chain each is in"))

		// The record is the whole of what makes the reach acceptable, so it
		// is read back through the ordinary door, from the operator's own
		// chain, where anybody auditing the operator would look.
		recorded, err := op.Surfaces.Audit.ListEntries(ctx, &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{EventType: string(audit.EventOperatorBypass), ResourceId: neighbor.GetId()},
		})
		must.NoError(t, err)
		test.SliceNotEmpty(t, recorded.GetResults(),
			test.Sprint("an operator read another session's entry and their own chain holds no record of it"))
	})

	t.Run("an operator's page spans tenants, and the ordinary page does not", func(t *testing.T) {
		t.Parallel()

		op, theirs, act := twoChains(t, s, listEntries, listAnyEntries)
		if op.Surfaces.AuditAdministration == nil {
			conformance.Skip(t, "conformance: this subject serves no operator's read of the audit log; skipping")
		}

		neighbor := act(t, theirs)
		ctx := op.Context(t.Context())

		wide, err := op.Surfaces.AuditAdministration.ListAnyEntries(ctx, &auditpb.ListAnyEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceType: neighbor.ResourceType, ResourceId: neighbor.ResourceID},
		})
		must.NoError(t, err, must.Sprint("an operator could not page every tenant's log"))

		found := false
		for _, owned := range wide.GetResults() {
			found = found || owned.GetEntry().GetResourceId() == neighbor.ResourceID
		}

		test.True(t, found, test.Sprint("an operator's page of every tenant's log missed another session's entry"))

		// The same caller, through AuditService: holding the operator's read
		// changes nothing about the ordinary one.
		narrow, err := op.Surfaces.Audit.ListEntries(ctx, &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceType: neighbor.ResourceType, ResourceId: neighbor.ResourceID},
		})
		must.NoError(t, err)
		test.False(t, holdsResource(narrow.GetResults(), neighbor),
			test.Sprint("a caller who may read every tenant's log had their ordinary read widened"))
	})
}
