package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	"github.com/primandproper/platform-go/v14/callers"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const operatorID = "operator_1"

// operatorPrincipal is who an operator's read is recorded against.
type operatorPrincipal struct{ scope tenancy.Scope }

func (operatorPrincipal) UserID() string          { return operatorID }
func (p operatorPrincipal) Scope() tenancy.Scope  { return p.scope }
func (operatorPrincipal) ActiveAccountID() string { return "" }

func operatorPrincipals(ctx context.Context) (callers.Principal, bool) {
	scope, err := resolveScope(ctx)
	if err != nil {
		return nil, false
	}

	return operatorPrincipal{scope: scope}, true
}

// operatorHarness is a harness armed to serve AuditAdministrationService,
// recording each read with the real recorder into the log it serves.
//
// Whether a caller may reach those methods at all is the authorization
// interceptor's, which these tests do not install: the method is the grant,
// and the server trusts the interceptor that decided it.
func operatorHarness(t *testing.T, opts ...auditgrpc.Option) *harness {
	t.Helper()

	recorder, err := audit.NewRecorder(dialect.SQLite)
	must.NoError(t, err)

	return newHarness(t, append([]auditgrpc.Option{
		auditgrpc.WithOperatorRecorder(recorder, operatorPrincipals),
	}, opts...)...)
}

// operatorReads is every operator's read recorded in our chain.
func (h *harness) operatorReads(t *testing.T) []*audit.Entry {
	t.Helper()

	page, err := h.reader.List(t.Context(), h.db.Reader(), ours,
		&audit.Query{ActorID: operatorID, EventType: audit.EventOperatorBypass}, nil)
	must.NoError(t, err)

	return page.Data
}

// failingRecorder refuses every record.
type failingRecorder struct{}

var errRecordRefused = errors.New("recording refused")

func (failingRecorder) Record(context.Context, database.Tx, tenancy.Scope, ...*audit.Entry) error {
	return errRecordRefused
}

func TestServer_GetAnyEntry(T *testing.T) {
	T.Parallel()

	T.Run("reads another tenant's entry, names its owner, and records the read in the operator's own chain", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t)

		response, err := h.admin.GetAnyEntry(h.asOurs(), &auditpb.GetAnyEntryRequest{EntryId: h.yours.ID})
		must.NoError(t, err)
		test.EqOp(t, "recipe_2", response.GetEntry().GetEntry().GetResourceId())
		test.EqOp(t, theirs.Owner(), response.GetEntry().GetOwnerId())

		reads := h.operatorReads(t)
		must.SliceLen(t, 1, reads)
		test.EqOp(t, "audit_entry", reads[0].ResourceType)
		test.EqOp(t, h.yours.ID, reads[0].ResourceID)
		test.EqOp(t, string(auditgrpc.PermissionReadAnyEntries), reads[0].Metadata[audit.MetadataOperatorPermission])
		test.EqOp(t, auditpb.AuditAdministrationService_GetAnyEntry_FullMethodName, reads[0].Metadata[audit.MetadataOperatorMethod])
	})

	T.Run("an id no chain holds is NotFound, and records nothing", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t)

		_, err := h.admin.GetAnyEntry(h.asOurs(), &auditpb.GetAnyEntryRequest{EntryId: "no_such_entry"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.SliceEmpty(t, h.operatorReads(t))
	})

	T.Run("a read that cannot be recorded is not answered", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, auditgrpc.WithOperatorRecorder(failingRecorder{}, operatorPrincipals))

		response, err := h.admin.GetAnyEntry(h.asOurs(), &auditpb.GetAnyEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
		test.Nil(t, response.GetEntry(), test.Sprint("an unrecorded read handed the entry back"))
	})

	T.Run("a server with nowhere to record serves no operator's read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.admin.GetAnyEntry(h.asOurs(), &auditpb.GetAnyEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.Unimplemented, status.Code(err))
	})
}

func TestServer_ListAnyEntries(T *testing.T) {
	T.Parallel()

	T.Run("pages every tenant's chain, each entry naming its owner, and records the read", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t)

		response, err := h.admin.ListAnyEntries(h.asOurs(), &auditpb.ListAnyEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceType: "recipe"},
		})
		must.NoError(t, err)

		owners := map[string]string{}
		for _, owned := range response.GetResults() {
			owners[owned.GetEntry().GetResourceId()] = owned.GetOwnerId()
		}

		test.EqOp(t, ours.Owner(), owners["recipe_1"])
		test.EqOp(t, theirs.Owner(), owners["recipe_2"])

		reads := h.operatorReads(t)
		must.SliceLen(t, 1, reads)
		test.EqOp(t, "audit_log", reads[0].ResourceType)
		test.MapNotContainsKey(t, reads[0].Metadata, auditgrpc.MetadataOperatorOwner,
			test.Sprint("a read of every tenant was recorded as narrowed to one"))
	})

	T.Run("an owner narrows the page to that tenant's chain, and the record names it", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t)
		owner := theirs.Owner()

		response, err := h.admin.ListAnyEntries(h.asOurs(), &auditpb.ListAnyEntriesRequest{OwnerId: &owner})
		must.NoError(t, err)
		must.SliceLen(t, 1, response.GetResults())
		test.EqOp(t, "recipe_2", response.GetResults()[0].GetEntry().GetResourceId())

		reads := h.operatorReads(t)
		must.SliceLen(t, 1, reads)
		test.EqOp(t, owner, reads[0].Metadata[auditgrpc.MetadataOperatorOwner])
	})

	T.Run("the ordinary read is still the caller's own chain", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t)

		response, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		for _, entry := range response.GetResults() {
			test.NotEqOp(t, "recipe_2", entry.GetResourceId(), test.Sprint("an armed server widened an ordinary read"))
		}

		test.SliceEmpty(t, h.operatorReads(t), test.Sprint("an ordinary read was recorded as an operator's"))
	})

	T.Run("a server with nowhere to record serves no operator's read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.admin.ListAnyEntries(h.asOurs(), &auditpb.ListAnyEntriesRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.Unimplemented, status.Code(err))
	})
}
