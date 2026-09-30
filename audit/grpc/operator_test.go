package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	"github.com/primandproper/platform-go/v14/callers"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mdOperator marks a request as the operator's. Whether the operator holds the
// permission is the grants extractor's to say, and the tests below vary it.
const (
	mdOperator = "test-operator"
	operatorID = "operator_1"
)

// operatorPrincipal is who an operator's read is recorded against.
type operatorPrincipal struct{ scope tenancy.Scope }

func (operatorPrincipal) UserID() string          { return operatorID }
func (p operatorPrincipal) Scope() tenancy.Scope  { return p.scope }
func (operatorPrincipal) ActiveAccountID() string { return "" }

func isOperator(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)

	return ok && len(md.Get(mdOperator)) > 0
}

// operatorGrants hands the operator the permissions named and everybody else
// nothing.
func operatorGrants(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(ctx context.Context) (authorization.Grants, bool) {
		if !isOperator(ctx) {
			return authorization.DenyAll(), true
		}

		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

func operatorPrincipals(ctx context.Context) (callers.Principal, bool) {
	scope, err := resolveScope(ctx)
	if err != nil {
		return nil, false
	}

	return operatorPrincipal{scope: scope}, true
}

// asOperator is a request against our log, made by the operator.
func (h *harness) asOperator() context.Context {
	return metadata.AppendToOutgoingContext(h.asOurs(), mdOperator, "yes")
}

// operatorHarness is a harness whose operator holds perms, recording their
// reads with the real recorder into the log it serves.
func operatorHarness(t *testing.T, perms []authorization.Permission, opts ...auditgrpc.Option) *harness {
	t.Helper()

	recorder, err := audit.NewRecorder(dialect.SQLite)
	must.NoError(t, err)

	return newHarness(t, append([]auditgrpc.Option{
		auditgrpc.WithGrantsExtractor(operatorGrants(perms...)),
		auditgrpc.WithOperatorRecorder(recorder, operatorPrincipals),
	}, opts...)...)
}

// operatorReads is every operator admission recorded in our chain.
func (h *harness) operatorReads(t *testing.T) []*audit.Entry {
	t.Helper()

	page, err := h.reader.List(t.Context(), h.db.Reader(), &audit.Query{Scope: &ours, ActorID: operatorID}, nil)
	must.NoError(t, err)

	var reads []*audit.Entry

	for _, entry := range page.Data {
		if entry.EventType == audit.EventOperatorBypass {
			reads = append(reads, entry)
		}
	}

	return reads
}

func TestAnOperatorReadsPastTheConnectionsChains(T *testing.T) {
	T.Parallel()

	read := []authorization.Permission{auditgrpc.PermissionOperatorRead}

	T.Run("a holder reads another tenant's entry by id, and the read is recorded in their own chain", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t, read)

		response, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.NoError(t, err)
		test.EqOp(t, "recipe_2", response.GetEntry().GetResourceId())

		reads := h.operatorReads(t)
		must.SliceLen(t, 1, reads)
		test.EqOp(t, "audit_entry", reads[0].ResourceType)
		test.EqOp(t, h.yours.ID, reads[0].ResourceID)
		test.EqOp(t, string(auditgrpc.PermissionOperatorRead), reads[0].Metadata[audit.MetadataOperatorPermission])
		test.EqOp(t, auditpb.AuditService_GetEntry_FullMethodName, reads[0].Metadata[audit.MetadataOperatorMethod])
	})

	T.Run("a caller without it gets the usual absence, and nothing is recorded", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t, read)

		_, err := h.client.GetEntry(h.asOurs(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.ErrorIs(t, err, audit.ErrEntryNotFound)

		response, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		for _, entry := range response.GetResults() {
			test.NotEq(t, h.yours.ID, entry.GetId(), test.Sprint("a caller without the permission paged another tenant's log"))
		}

		test.SliceEmpty(t, h.operatorReads(t))
	})

	T.Run("a holder reading their own entry is an ordinary read, and is not recorded", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t, read)

		_, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.mine.ID})
		must.NoError(t, err)
		test.SliceEmpty(t, h.operatorReads(t))
	})

	T.Run("an id nobody wrote is absent to a holder too, and costs the log nothing", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t, read)

		_, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: "audit_nonexistent"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.SliceEmpty(t, h.operatorReads(t))
	})

	T.Run("a holder's page spans every chain, and the page is recorded", func(t *testing.T) {
		t.Parallel()

		h := operatorHarness(t, read)

		response, err := h.client.ListEntries(h.asOperator(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		var ids []string
		for _, entry := range response.GetResults() {
			ids = append(ids, entry.GetId())
		}

		test.SliceContains(t, ids, h.mine.ID)
		test.SliceContains(t, ids, h.yours.ID)

		reads := h.operatorReads(t)
		must.SliceLen(t, 1, reads)
		test.EqOp(t, "audit_log", reads[0].ResourceType)
		test.EqOp(t, auditpb.AuditService_ListEntries_FullMethodName, reads[0].Metadata[audit.MetadataOperatorMethod])
	})
}

func TestAnOperatorReadNobodyCanSeeIsNoRead(T *testing.T) {
	T.Parallel()

	T.Run("with nowhere to record it, a holder reads their own chains", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, auditgrpc.WithGrantsExtractor(operatorGrants(auditgrpc.PermissionOperatorRead)))

		_, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))

		response, err := h.client.ListEntries(h.asOperator(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, response.GetResults())
		test.EqOp(t, h.mine.ID, response.GetResults()[0].GetId())
	})

	T.Run("a record that could not be written fails the read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t,
			auditgrpc.WithGrantsExtractor(operatorGrants(auditgrpc.PermissionOperatorRead)),
			auditgrpc.WithOperatorRecorder(failingRecorder{}, operatorPrincipals))

		response, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
		test.Nil(t, response.GetEntry())

		listed, err := h.client.ListEntries(h.asOperator(), &auditpb.ListEntriesRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
		test.SliceEmpty(t, listed.GetResults())
	})
}

func TestTheOperatorReadPermissionIsTheDeploymentsToName(T *testing.T) {
	T.Parallel()

	T.Run("a renamed permission admits its holder, and the default name no longer does", func(t *testing.T) {
		t.Parallel()

		const console authorization.Permission = "console.audit.read"

		h := operatorHarness(t, []authorization.Permission{console}, auditgrpc.WithOperatorPermission(console))

		_, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.NoError(t, err)

		stale := operatorHarness(t, []authorization.Permission{auditgrpc.PermissionOperatorRead},
			auditgrpc.WithOperatorPermission(console))

		_, err = stale.client.GetEntry(stale.asOperator(), &auditpb.GetEntryRequest{EntryId: stale.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("an empty permission widens nobody's read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t,
			auditgrpc.WithGrantsExtractor(func(context.Context) (authorization.Grants, bool) {
				return authorization.AllowAll(), true
			}),
			auditgrpc.WithOperatorRecorder(failingRecorder{}, operatorPrincipals),
			auditgrpc.WithOperatorPermission(""))

		_, err := h.client.GetEntry(h.asOperator(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("it is not a method permission, so it is not in the default fragment", func(t *testing.T) {
		t.Parallel()

		for method, perms := range auditgrpc.Permissions() {
			test.SliceNotContains(t, perms, auditgrpc.PermissionOperatorRead,
				test.Sprintf("%s requires the operator read permission", method))
		}
	})
}

// failingRecorder refuses every record.
type failingRecorder struct{}

func (failingRecorder) Record(context.Context, database.Tx, tenancy.Scope, ...*audit.Entry) error {
	return errors.New("the audit log is down")
}
