package audit

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// verification asserts VerifyChain on chains nobody tampered with.
//
// A break is not asserted here, and cannot honestly be: producing one means
// editing a recorded row behind the recorder's back, which no client can do
// and no deployment should offer a seam for. audit/grpc keeps that assertion,
// against a database its own test owns.
func verification(t *testing.T, s *conformance.Session) {
	t.Helper()

	// A break is a finding rather than a failure to answer, so what an intact
	// chain looks like is the half a client has to be able to tell apart.
	t.Run("a chain nobody tampered with verifies intact", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		act(t, mine)
		act(t, mine)

		response, err := mine.Surfaces.Audit.VerifyChain(mine.Context(t.Context()), &auditpb.VerifyChainRequest{})
		must.NoError(t, err)

		result := response.GetResult()
		must.NotNil(t, result)
		test.Nil(t, result.GetFirstBreak(), test.Sprintf("an untouched chain reported a break: %v", result.GetFirstBreak()))
		test.True(t, result.GetComplete())
		test.Positive(t, result.GetChecked(), test.Sprint("a chain this caller appended to verified nothing"))
	})

	// The pair that pins why after_seq is optional rather than defaulted. An
	// absent field walks from the start; a present zero resumes past position
	// zero, which every chain has — its first entry. Read the absent field as
	// the zero it decodes to and every chain this surface verifies would have
	// its first entry checked by nobody.
	//
	// Phrased as a comparison between two walks of one chain rather than as a
	// number, because how many entries a deployment records for one action is
	// the deployment's business.
	t.Run("verification walks from the first entry unless told where to resume", func(t *testing.T) {
		t.Parallel()

		mine, act := subject(t, s)
		act(t, mine)
		act(t, mine)

		ctx := mine.Context(t.Context())

		fromStart, err := mine.Surfaces.Audit.VerifyChain(ctx, &auditpb.VerifyChainRequest{})
		must.NoError(t, err)
		must.Positive(t, fromStart.GetResult().GetChecked(),
			must.Sprint("a chain this caller appended to verified nothing; the comparison below proves nothing"))

		zero := int64(0)

		pastFirst, err := mine.Surfaces.Audit.VerifyChain(ctx, &auditpb.VerifyChainRequest{AfterSeq: &zero})
		must.NoError(t, err)

		test.EqOp(t, fromStart.GetResult().GetChecked()-1, pastFirst.GetResult().GetChecked(),
			test.Sprint("resuming past the first position did not skip exactly the first entry"))
		test.EqOp(t, fromStart.GetResult().GetLastSeq(), pastFirst.GetResult().GetLastSeq())
		test.True(t, fromStart.GetResult().GetComplete())
		test.True(t, pastFirst.GetResult().GetComplete())
	})

	t.Run("verification carries its window back as it was given", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		// Whole seconds, so the echo is compared at a precision every dialect
		// and every encoding keeps.
		from := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

		response, err := mine.Surfaces.Audit.VerifyChain(mine.Context(t.Context()),
			&auditpb.VerifyChainRequest{From: timestamppb.New(from)})
		must.NoError(t, err)

		must.NotNil(t, response.GetResult().GetFrom(), must.Sprint("the window's start was dropped"))
		test.EqOp(t, from, response.GetResult().GetFrom().AsTime())

		// An absent bound stays absent rather than becoming the epoch.
		test.Nil(t, response.GetResult().GetTo())
	})
}
