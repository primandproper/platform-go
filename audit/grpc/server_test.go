package grpc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNewServer(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil reader", func(t *testing.T) {
		t.Parallel()

		srv, err := auditgrpc.NewServer(nil, auditgrpc.GlobalScope)
		test.ErrorIs(t, err, auditgrpc.ErrNilReader)
		test.Nil(t, srv)
	})

	// Not defaulted to the global scope, which is where this service and
	// authentication/signin/grpc part company — see ErrNilScopeResolver.
	T.Run("refuses a nil scope resolver", func(t *testing.T) {
		t.Parallel()

		srv, err := auditgrpc.NewServer(&audit.SQLReader{}, nil)
		test.ErrorIs(t, err, auditgrpc.ErrNilScopeResolver)
		test.Nil(t, srv)
	})

	T.Run("builds with observability and without", func(t *testing.T) {
		t.Parallel()

		bare, err := auditgrpc.NewServer(&audit.SQLReader{}, auditgrpc.GlobalScope)
		must.NoError(t, err)
		must.NotNil(t, bare)

		observed, err := auditgrpc.NewServer(&audit.SQLReader{}, auditgrpc.GlobalScope,
			auditgrpc.WithPillars(&observability.Pillars{}),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, observed)
	})
}

func TestServer_GetEntry(T *testing.T) {
	T.Parallel()

	T.Run("reads an entry in the caller's scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		response, err := h.client.GetEntry(h.asOurs(), &auditpb.GetEntryRequest{EntryId: h.mine.ID})
		must.NoError(t, err)

		entry := response.GetEntry()
		must.NotNil(t, entry)
		test.EqOp(t, h.mine.ID, entry.GetId())
		test.EqOp(t, "recipe_1", entry.GetResourceId())
		test.EqOp(t, string(audit.EventUpdated), entry.GetEventType())
		test.EqOp(t, "user_1", entry.GetActor().GetId())
		test.EqOp(t, h.mine.Hash, entry.GetHash())

		// The change survives as a value rather than as a rendered string.
		test.EqOp(t, "Stew", entry.GetChanges()["name"].GetNewValue().GetStringValue())
	})

	// The acceptance test for the whole surface: the id is real, the entry
	// exists, and the caller is somebody else. It reads as absent, which is
	// what it is from here.
	T.Run("answers an entry in another scope as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		response, err := h.client.GetEntry(h.asOurs(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.Error(t, err)
		test.Nil(t, response.GetEntry())
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.ErrorIs(t, err, audit.ErrEntryNotFound)

		// And it is indistinguishable from an id that was never written, which
		// is what stops this being an oracle for another tenant's ids. Both
		// answers are the same code and the same sentence about the id the
		// client itself sent — the only thing that differs between them is that
		// id, which the client supplied.
		_, absent := h.client.GetEntry(h.asOurs(), &auditpb.GetEntryRequest{EntryId: "audit_nonexistent"})
		must.Error(t, absent)
		test.EqOp(t, status.Code(absent), status.Code(err))
		test.ErrorIs(t, absent, audit.ErrEntryNotFound)
		test.EqOp(t,
			fmt.Sprintf("reading audit entry %q", "audit_nonexistent"),
			status.Convert(absent).Message())
		test.EqOp(t,
			fmt.Sprintf("reading audit entry %q", h.yours.ID),
			status.Convert(err).Message())

		// The same entry read by the tenant it belongs to is there, so the
		// refusal above is the scope and not the fixture.
		mine, err := h.client.GetEntry(h.asTheirs(), &auditpb.GetEntryRequest{EntryId: h.yours.ID})
		must.NoError(t, err)
		test.EqOp(t, h.yours.ID, mine.GetEntry().GetId())
	})

	T.Run("refuses an empty id", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.GetEntry(h.asOurs(), &auditpb.GetEntryRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestServer_ListEntries(T *testing.T) {
	T.Parallel()

	T.Run("pages the caller's scope and nobody else's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.record(t, entryFor(ours, "recipe_3"), entryFor(theirs, "recipe_4"))

		response, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{})
		must.NoError(t, err)

		results := response.GetResults()
		must.SliceLen(t, 2, results)

		for _, entry := range results {
			test.SliceContains(t, []string{"recipe_1", "recipe_3"}, entry.GetResourceId(),
				test.Sprintf("%s belongs to another tenant's log", entry.GetResourceId()))
		}
	})

	// A query is a narrowing of the caller's scope and never a widening of it.
	// There is no field on EntryQuery that could say otherwise — see
	// TestRequestsCannotNameAScope — so what this asserts is that the narrowings
	// there are do not disturb the one the connection bound.
	T.Run("narrows within the scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.record(t, entryFor(ours, "recipe_3"))

		response, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceId: "recipe_3"},
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, response.GetResults())
		test.EqOp(t, "recipe_3", response.GetResults()[0].GetResourceId())

		// A narrowing that matches only the other tenant's entry finds nothing,
		// rather than reaching across.
		empty, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Query: &auditpb.EntryQuery{ResourceId: "recipe_2"},
		})
		must.NoError(t, err)
		test.SliceEmpty(t, empty.GetResults())
	})

	T.Run("refuses a malformed filter", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Filter: &filteringpb.QueryFilter{SortBy: pointer.To("sideways")},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestServer_VerifyChain(T *testing.T) {
	T.Parallel()

	T.Run("reports an intact chain", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		response, err := h.client.VerifyChain(h.asOurs(), &auditpb.VerifyChainRequest{})
		must.NoError(t, err)

		result := response.GetResult()
		must.NotNil(t, result)
		test.Nil(t, result.GetFirstBreak())
		test.EqOp(t, int64(1), result.GetChecked())

		// The client's own reading of it, which is what a caller writes.
		test.True(t, auditgrpc.VerificationResultFromProto(result).Intact())
	})

	// A break is a finding rather than a failure to answer, so it arrives as an
	// ordinary response.
	T.Run("reports a break without failing the call", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.db.Writer().ExecContext(t.Context(),
			"UPDATE audit_log_entries SET resource_id = 'tampered' WHERE id = ?", h.mine.ID)
		must.NoError(t, err)

		response, err := h.client.VerifyChain(h.asOurs(), &auditpb.VerifyChainRequest{})
		must.NoError(t, err)

		firstBreak := response.GetResult().GetFirstBreak()
		must.NotNil(t, firstBreak)
		test.EqOp(t, auditpb.BreakReason_BREAK_REASON_CONTENT_ALTERED, firstBreak.GetReason())
		test.EqOp(t, h.mine.ID, firstBreak.GetEntryId())
		test.False(t, auditgrpc.VerificationResultFromProto(response.GetResult()).Intact())

		// The other tenant's chain is untouched, which is the property the
		// per-scope chain exists for.
		theirsResponse, err := h.client.VerifyChain(h.asTheirs(), &auditpb.VerifyChainRequest{})
		must.NoError(t, err)
		test.Nil(t, theirsResponse.GetResult().GetFirstBreak())
	})

	T.Run("carries the window back as it was given", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		from := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

		response, err := h.client.VerifyChain(h.asOurs(), &auditpb.VerifyChainRequest{
			From: timestamppb.New(from),
		})
		must.NoError(t, err)

		test.EqOp(t, from, response.GetResult().GetFrom().AsTime())

		// An absent bound stays absent rather than becoming the epoch.
		test.Nil(t, response.GetResult().GetTo())
	})
}

// TestScopeResolution covers the two ways the connection can fail to say whose
// log a request is against. Both are refusals, and neither falls back to a scope
// — a fallback here reads somebody's log.
func TestScopeResolution(T *testing.T) {
	T.Parallel()

	T.Run("refuses a request it cannot place", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for _, method := range []func() error{
			func() error {
				_, err := h.client.GetEntry(h.rootCtx, &auditpb.GetEntryRequest{EntryId: h.mine.ID})

				return err
			},
			func() error {
				_, err := h.client.ListEntries(h.rootCtx, &auditpb.ListEntriesRequest{})

				return err
			},
			func() error {
				_, err := h.client.VerifyChain(h.rootCtx, &auditpb.VerifyChainRequest{})

				return err
			},
		} {
			err := method()
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		}
	})

	// A resolver that answers with no error and no scope is the mistake the
	// server refuses rather than trusts: the zero Scope's owner is the empty
	// string, which is the platform chain, so trusting it would answer a
	// request that named nobody with the entries that belong to nobody.
	T.Run("refuses a resolver that answered with no scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.ListEntries(asScope(h.rootCtx, scopeUnset), &auditpb.ListEntriesRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})
}
