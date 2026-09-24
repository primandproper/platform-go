package pagination

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/pagedrpc"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// widestNarrowable is the largest page size a uint16 holds.
	widestNarrowable = 65535

	// wrapsSmall is a page size that narrows to 10: large enough to be clamped,
	// and small once wrapped, so a surface that narrowed first answers a page
	// far smaller than the ceiling.
	wrapsSmall = 65546

	// sentCursor names nothing. A cursor is the last row's identifier and a
	// page resumes after it, so one naming no row is a request for whatever
	// sorts after it — an answerable one.
	sentCursor = "conformance-cursor"
)

// Suite asserts that every paged read reports the filter it applied.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "pagination",
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	subject := s.Subject(t)

	if subject.Conn == nil {
		t.Skip("conformance: this subject supplies no connection to invoke a read by name through")
	}

	reads := pagedrpc.Mounted(&subject.Surfaces)
	if len(reads) == 0 {
		t.Skip("conformance: this subject mounts no surface with a paged read")
	}

	seams := s.Seams()

	for i := range reads {
		read := reads[i]

		t.Run(read.Surface+"/"+string(read.Method.Name()), func(t *testing.T) {
			t.Parallel()

			page := func(t *testing.T, filter *filteringpb.QueryFilter) *filteringpb.Pagination {
				t.Helper()

				req, reason := read.Request(subject, &seams, filter)
				if reason != "" {
					t.Skipf("conformance: %s cannot be asserted here: %s", read.FullName, reason)
				}

				resp := read.Response()

				err := subject.Conn.Invoke(subject.Context(t.Context()), read.FullName, req, resp)
				if code := status.Code(err); code != codes.OK {
					t.Skipf("conformance: %s answers this request with %s rather than a page, so there is no pagination to read", read.FullName, code)
				}

				pagination, err := pagedrpc.Pagination(resp)
				must.NoError(t, err, must.Sprintf("reading %s's page", read.FullName))

				return pagination
			}

			t.Run("an absent page size is reported as the default", func(t *testing.T) {
				t.Parallel()

				p := page(t, nil)

				test.EqOp(t, uint32(filtering.DefaultQueryFilterLimit), p.GetMaxResponseSize())
				test.EqOp(t, uint32(filtering.DefaultQueryFilterLimit), p.GetAppliedQueryFilter().GetMaxResponseSize(),
					test.Sprint("the applied filter disagrees with the page about how large it was"))
			})

			t.Run("a page size too large to narrow is clamped rather than wrapped", func(t *testing.T) {
				t.Parallel()

				widest := page(t, &filteringpb.QueryFilter{MaxResponseSize: proto.Uint32(widestNarrowable)})
				wrapped := page(t, &filteringpb.QueryFilter{MaxResponseSize: proto.Uint32(wrapsSmall)})

				test.EqOp(t, widest.GetMaxResponseSize(), wrapped.GetMaxResponseSize(), test.Sprintf(
					"asking for %d was answered with a page of %d, and asking for %d with %d: the larger was narrowed before it was clamped",
					widestNarrowable, widest.GetMaxResponseSize(), wrapsSmall, wrapped.GetMaxResponseSize()))
				test.EqOp(t, wrapped.GetMaxResponseSize(), wrapped.GetAppliedQueryFilter().GetMaxResponseSize(),
					test.Sprint("the applied filter disagrees with the page about how large it was"))
			})

			t.Run("a sort direction is reported normalized", func(t *testing.T) {
				t.Parallel()

				p := page(t, &filteringpb.QueryFilter{SortBy: new("DESC")})

				test.EqOp(t, "desc", p.GetAppliedQueryFilter().GetSortBy())
			})

			t.Run("a cursor is echoed as the previous one", func(t *testing.T) {
				t.Parallel()

				p := page(t, &filteringpb.QueryFilter{Cursor: new(sentCursor)})

				test.EqOp(t, sentCursor, p.GetPreviousCursor())
			})
		})
	}
}
