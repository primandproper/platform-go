package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The suite's stand-in for the half of a consumer's wiring that says what the
// caller may do, and the five reads' answer to a client that asked for the
// reports somebody took out of the queue.
//
// The extractor is what a deployment hands both this surface and
// primitives-go's authorization/grpc enforcer. It is not the enforcer: nothing
// in these tests checks whether the method may be called at all, because that
// check is an interceptor's and runs before the handler. What is under test is
// the second question — which rows the answer may contain — and it is asked
// inside the handler, where the filter is.

// grantsKey is where this suite puts the caller's authority.
type grantsKey struct{}

// withGrants narrows a request context to exactly the permissions named, which
// is how a test describes a caller who may triage and may not archive.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file. A test that cares
// says so with [withGrants].
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// includeArchived is the filter a client sets to ask for the removed rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// archiveReport removes a seeded report from the queue directly through the
// store, so the row a test is about was archived without going through the
// surface the test is about.
func (h *harness) archiveReport(tb testing.TB, scope tenancy.Scope, reportID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveReport(tb.Context(), tx, scope, reportID)

		return err
	}))
}

// reportIDs is what every assertion below compares, because the rows' identity
// is the whole of the question: which reports came back.
func reportIDs(results []*issuereportspb.IssueReport) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling, executed, on all five
// paged reads.
//
// Each case seeds one live report and one somebody archived, sends
// include_archived: true, and asks the same question twice: as somebody holding
// only the grant the RPC requires, and as somebody who also holds
// issues.reports.archive. The first gets the live queue, the second gets what
// was taken out of it.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	// The five RPCs, each reduced to "which report ids does this answer with",
	// so the table below is about the rule rather than about five response
	// shapes.
	reads := map[string]struct {
		read  func(tb testing.TB, h *harness, ctx context.Context) []string
		grant authorization.Permission
	}{
		"ListReports": {
			grant: issuereportsgrpc.PermissionTriageReports,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListReports(ctx, &issuereportspb.ListReportsRequest{
					Filter: includeArchived(),
				})
				must.NoError(tb, err)

				return reportIDs(res.GetResults())
			},
		},
		"ListReportsByStatus": {
			grant: issuereportsgrpc.PermissionTriageReports,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListReportsByStatus(ctx, &issuereportspb.ListReportsByStatusRequest{
					Status: issuereportsgrpc.StatusToProto(issuereports.StatusOpen),
					Filter: includeArchived(),
				})
				must.NoError(tb, err)

				return reportIDs(res.GetResults())
			},
		},
		"ListReportsByReporter": {
			grant: issuereportsgrpc.PermissionReadReports,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListReportsByReporter(ctx, &issuereportspb.ListReportsByReporterRequest{
					Filter: includeArchived(),
				})
				must.NoError(tb, err)

				return reportIDs(res.GetResults())
			},
		},
		"ListReportsBySubjectType": {
			grant: issuereportsgrpc.PermissionTriageReports,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListReportsBySubjectType(ctx, &issuereportspb.ListReportsBySubjectTypeRequest{
					SubjectType: "recipe",
					Filter:      includeArchived(),
				})
				must.NoError(tb, err)

				return reportIDs(res.GetResults())
			},
		},
		"ListReportsForSubject": {
			grant: issuereportsgrpc.PermissionTriageReports,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListReportsForSubject(ctx, &issuereportspb.ListReportsForSubjectRequest{
					SubjectType: "recipe",
					SubjectId:   "recipe_1",
					Filter:      includeArchived(),
				})
				must.NoError(tb, err)

				return reportIDs(res.GetResults())
			},
		},
	}

	for name, read := range reads {
		T.Run(name+" hides the archived rows from a caller who cannot archive", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			live := h.seedReport(t, testScope, testReporter)
			removed := h.seedReport(t, testScope, testReporter)
			h.archiveReport(t, testScope, removed.ID)

			got := read.read(t, h, withGrants(h.ctx(t, testReporter), read.grant))

			test.Eq(t, []string{live.ID}, got)
		})

		T.Run(name+" answers the archived rows to a caller who can archive", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			live := h.seedReport(t, testScope, testReporter)
			removed := h.seedReport(t, testScope, testReporter)
			h.archiveReport(t, testScope, removed.ID)

			got := read.read(t, h,
				withGrants(h.ctx(t, testReporter), read.grant, issuereportsgrpc.PermissionArchiveReports))

			test.SliceContains(t, got, live.ID)
			test.SliceContains(t, got, removed.ID)
		})
	}

	// The narrowing is not a refusal, which is the half a caller notices: the
	// read succeeds and answers with what they were entitled to ask for.
	T.Run("clearing the field is not an error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedReport(t, testScope, testReporter)

		_, err := h.server.ListReports(
			withGrants(h.ctx(t, testReporter), issuereportsgrpc.PermissionTriageReports),
			&issuereportspb.ListReportsRequest{Filter: includeArchived()})

		must.NoError(t, err)
	})

	// The fail-closed default. A consumer who wired no extractor cannot be told
	// apart from one whose caller holds nothing, so the surface answers the same
	// way rather than guessing.
	T.Run("a server built with no grants extractor clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, issuereportsgrpc.WithGrantsExtractor(nil))
		live := h.seedReport(t, testScope, testReporter)
		removed := h.seedReport(t, testScope, testReporter)
		h.archiveReport(t, testScope, removed.ID)

		res, err := h.server.ListReports(h.ctx(t, testReporter), &issuereportspb.ListReportsRequest{
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, reportIDs(res.GetResults()))
	})

	// An extractor that reports no authority is the interceptor's "no grants
	// could be determined", and it is a denial everywhere else in the stack.
	T.Run("an extractor reporting no authority clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, issuereportsgrpc.WithGrantsExtractor(
			func(context.Context) (authorization.Grants, bool) { return authorization.AllowAll(), false }))

		live := h.seedReport(t, testScope, testReporter)
		removed := h.seedReport(t, testScope, testReporter)
		h.archiveReport(t, testScope, removed.ID)

		res, err := h.server.ListReports(h.ctx(t, testReporter), &issuereportspb.ListReportsRequest{
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, reportIDs(res.GetResults()))
	})

	// A filter that never asked is left alone, which is what stops the
	// confinement from being a rewrite of everybody's page.
	T.Run("a filter that did not ask is untouched", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedReport(t, testScope, testReporter)
		removed := h.seedReport(t, testScope, testReporter)
		h.archiveReport(t, testScope, removed.ID)

		res, err := h.server.ListReports(
			withGrants(h.ctx(t, testReporter),
				issuereportsgrpc.PermissionTriageReports, issuereportsgrpc.PermissionArchiveReports),
			&issuereportspb.ListReportsRequest{})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, reportIDs(res.GetResults()))
	})
}
