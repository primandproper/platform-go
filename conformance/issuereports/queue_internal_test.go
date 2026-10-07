package issuereports

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// pagedQueue answers ListReports two reports to a page, the cursor being the
// index of the next row, and holds its archived rows apart so a request that
// does not ask for them never sees them. A queue with endless set never
// answers a last page.
type pagedQueue struct {
	issuereportspb.IssueReportsServiceClient

	refusal       error
	live, archive []string
	requests      []*issuereportspb.ListReportsRequest
	endless       bool
}

func (q *pagedQueue) ListReports(
	_ context.Context,
	req *issuereportspb.ListReportsRequest,
	_ ...grpc.CallOption,
) (*issuereportspb.ListReportsResponse, error) {
	q.requests = append(q.requests, req)

	if q.refusal != nil {
		return nil, q.refusal
	}

	if q.endless {
		return &issuereportspb.ListReportsResponse{
			Results:    []*issuereportspb.IssueReport{{Id: "again"}},
			Pagination: &filteringpb.Pagination{Cursor: "again"},
		}, nil
	}

	rows := q.live
	if req.GetFilter().GetIncludeArchived() {
		rows = append(append([]string{}, q.live...), q.archive...)
	}

	start := 0
	if filter := req.GetFilter(); filter != nil && filter.Cursor != nil {
		var err error
		if start, err = strconv.Atoi(req.GetFilter().GetCursor()); err != nil {
			return nil, err
		}
	}

	end := min(start+2, len(rows))

	page := &issuereportspb.ListReportsResponse{Pagination: &filteringpb.Pagination{}}
	for _, id := range rows[start:end] {
		page.Results = append(page.Results, &issuereportspb.IssueReport{Id: id})
	}

	if end < len(rows) {
		page.Pagination.Cursor = strconv.Itoa(end)
	}

	return page, nil
}

func TestListings(t *testing.T) {
	t.Parallel()

	list := listings()["ListReports"]

	t.Run("a report past the first page of a shared queue is found", func(t *testing.T) {
		t.Parallel()

		queue := &pagedQueue{live: []string{"a", "b", "c", "d", "mine"}}
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{IssueReports: queue}}

		ids, err := list(t.Context(), caller, nil, nil)
		must.NoError(t, err)

		test.Eq(t, []string{"a", "b", "c", "d", "mine"}, ids)
		test.SliceLen(t, 3, queue.requests)

		// The first request is the caller's own, which here asked for no
		// filter at all and so carries no cursor — an empty cursor is a
		// cursor.
		test.Nil(t, queue.requests[0].GetFilter())
		test.EqOp(t, "2", queue.requests[1].GetFilter().GetCursor())
	})

	t.Run("the archive is asked for on every page, not the first alone", func(t *testing.T) {
		t.Parallel()

		queue := &pagedQueue{live: []string{"a", "b", "c"}, archive: []string{"gone"}}
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{IssueReports: queue}}

		with, err := list(t.Context(), caller, nil, includeArchived())
		must.NoError(t, err)
		test.SliceContains(t, with, "gone")

		for _, req := range queue.requests {
			test.True(t, req.GetFilter().GetIncludeArchived())
		}

		without, err := list(t.Context(), caller, nil, nil)
		must.NoError(t, err)
		test.SliceNotContains(t, without, "gone")
	})

	t.Run("a refusal on any page is the listing's answer", func(t *testing.T) {
		t.Parallel()

		refused := errors.New("refused")
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{IssueReports: &pagedQueue{refusal: refused}}}

		_, err := list(t.Context(), caller, nil, nil)
		test.ErrorIs(t, err, refused)
	})

	t.Run("a cursor that never ends is bounded rather than walked forever", func(t *testing.T) {
		t.Parallel()

		queue := &pagedQueue{endless: true}
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{IssueReports: queue}}

		_, err := list(t.Context(), caller, nil, nil)
		must.Error(t, err)
		test.SliceLen(t, maxPages, queue.requests)
	})
}
