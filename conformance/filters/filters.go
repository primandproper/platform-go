package filters

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/pagedrpc"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Suite asserts that every paged read refuses a malformed filter.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "filters",

		// Each read's surface is checked inside, as the anonymous suite does.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

// malformed are the filters no surface may answer with a page.
//
// They are the two things filtering/grpc reports rather than corrects: a sort
// direction it does not recognize, and a timestamp protobuf itself considers
// out of range. A page size above the ceiling is not here, because it is
// clamped rather than refused, and whether the clamp is reported honestly is the
// pagination suite's question.
func malformed() map[string]*filteringpb.QueryFilter {
	return map[string]*filteringpb.QueryFilter{
		"an unrecognized sort direction": {SortBy: new("sideways")},

		// The first second of the year 10000, which Timestamp's documented range
		// ends just before.
		"a timestamp outside protobuf's range": {CreatedAfter: &timestamppb.Timestamp{Seconds: 253402300800}},
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

			ctx := subject.Context(t.Context())

			control, reason := read.Request(subject, &seams, &filteringpb.QueryFilter{SortBy: new("asc")})
			if reason != "" {
				t.Skipf("conformance: %s cannot be asserted here: %s", read.FullName, reason)
			}

			controlErr := subject.Conn.Invoke(ctx, read.FullName, control, read.Response())
			must.NotEqOp(t, codes.InvalidArgument, status.Code(controlErr), must.Sprintf(
				"%s refused a well-formed filter as InvalidArgument (%v), so a refusal of a malformed one could not be told apart from it",
				read.FullName, controlErr))

			for name, filter := range malformed() {
				req, _ := read.Request(subject, &seams, filter)

				err := subject.Conn.Invoke(ctx, read.FullName, req, read.Response())
				test.EqOp(t, codes.InvalidArgument, status.Code(err), test.Sprintf(
					"%s answered %s with %v rather than refusing it", read.FullName, name, err))
			}
		})
	}
}
