package grpc_test

import (
	"context"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNewServerRefusesEachMissingDependency pins the four wiring failures, each
// with its own sentinel.
//
// Four sentinels rather than one because the four are fixed in four different
// places, and a consumer reading "nil input parameter" out of a container that
// wires a dozen components has been told nothing. The last two are the ones
// worth the most: both are positional and neither has a default, precisely
// because every default is wrong in a way nothing reports.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	// One live set of dependencies, so each case below differs in exactly the
	// one it leaves out.
	h := newHarness(T)

	for _, tc := range []struct {
		build func() (*issuereportsgrpc.Server, error)
		want  error
		name  string
	}{
		{
			name: "no store",
			want: issuereportsgrpc.ErrNilStore,
			build: func() (*issuereportsgrpc.Server, error) {
				return issuereportsgrpc.NewServer(nil, h.db, extractPrincipal, triageAuthorizer{})
			},
		},
		{
			name: "no database client",
			want: issuereportsgrpc.ErrNilDatabaseClient,
			build: func() (*issuereportsgrpc.Server, error) {
				return issuereportsgrpc.NewServer(h.store, nil, extractPrincipal, triageAuthorizer{})
			},
		},
		{
			name: "no principal extractor",
			want: issuereportsgrpc.ErrNilPrincipalExtractor,
			build: func() (*issuereportsgrpc.Server, error) {
				return issuereportsgrpc.NewServer(h.store, h.db, nil, triageAuthorizer{})
			},
		},
		{
			name: "no report authorizer",
			want: issuereportsgrpc.ErrNilReportAuthorizer,
			build: func() (*issuereportsgrpc.Server, error) {
				return issuereportsgrpc.NewServer(h.store, h.db, extractPrincipal, nil)
			},
		},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, err := tc.build()
			must.Error(t, err)
			test.ErrorIs(t, err, tc.want)

			// Nil rather than a server that would answer every RPC with the
			// failure it was built with.
			test.Nil(t, srv)
		})
	}
}

// TestNewServerToleratesANilOption keeps a consumer assembling options
// conditionally from writing a nil check per link.
func TestNewServerToleratesANilOption(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	srv, err := issuereportsgrpc.NewServer(h.store, h.db, extractPrincipal, triageAuthorizer{}, nil)
	must.NoError(t, err)
	test.NotNil(t, srv)
}

// TestServerRegistersOnAGRPCServer is the mounting a consumer performs, and the
// reason RegisterOn has server/grpc's RegistrationFunc signature: mounting the
// report queue beside the directory is one more entry in a slice rather than a
// closure per service.
func TestServerRegistersOnAGRPCServer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	srv := grpc.NewServer()
	t.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	// Registered under the name the generated descriptor declares, which is what
	// a client's generated stub dials.
	_, ok := srv.GetServiceInfo()[issuereportspb.IssueReportsService_ServiceDesc.ServiceName]
	test.True(t, ok, test.Sprintf("the service is not mounted under %s",
		issuereportspb.IssueReportsService_ServiceDesc.ServiceName))
}

// TestAnExtractorThatAnswersWithNobodyIsAnAnonymousRequest is the second half of
// the principal seam's contract, and it is a different failure from the one the
// suites already cover.
//
// A context carrying no principal at all reports (nil, false). An extractor that
// reports (nil, true) — a consumer's interceptor that found a session and could
// not build a principal from it — is the case a `!ok` check alone would let
// through, and what it would let through is every RPC running against a nil
// principal, which on this surface means a report filed by nobody.
func TestAnExtractorThatAnswersWithNobodyIsAnAnonymousRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	present := func(context.Context) (identitygrpc.Principal, bool) { return nil, true }

	srv, err := issuereportsgrpc.NewServer(h.store, h.db, present, triageAuthorizer{})
	must.NoError(t, err)

	res, err := srv.ListReports(t.Context(), &issuereportspb.ListReportsRequest{})
	must.Error(t, err)
	test.Nil(t, res)
	test.ErrorIs(t, err, issuereportsgrpc.ErrNoPrincipal)
	test.EqOp(t, codes.Unauthenticated, status.Code(err))
}
