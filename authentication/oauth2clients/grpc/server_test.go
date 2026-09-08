package grpc_test

import (
	"context"
	"testing"

	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

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
// wires a dozen components has been told nothing. The principal extractor is the
// one worth the most: it is positional and has no default precisely because
// every default is wrong in a way nothing reports.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	// One live set of dependencies, so each case below differs in exactly the
	// one it leaves out.
	h := newHarness(T)

	for _, tc := range []struct {
		build func() (*oauth2clientsgrpc.Server, error)
		want  error
		name  string
	}{
		{
			name: "no service",
			want: oauth2clientsgrpc.ErrNilService,
			build: func() (*oauth2clientsgrpc.Server, error) {
				return oauth2clientsgrpc.NewServer(nil, h.store, h.db, extractPrincipal)
			},
		},
		{
			name: "no store",
			want: oauth2clientsgrpc.ErrNilStore,
			build: func() (*oauth2clientsgrpc.Server, error) {
				return oauth2clientsgrpc.NewServer(h.svc, nil, h.db, extractPrincipal)
			},
		},
		{
			name: "no database client",
			want: oauth2clientsgrpc.ErrNilDatabaseClient,
			build: func() (*oauth2clientsgrpc.Server, error) {
				return oauth2clientsgrpc.NewServer(h.svc, h.store, nil, extractPrincipal)
			},
		},
		{
			name: "no principal extractor",
			want: oauth2clientsgrpc.ErrNilPrincipalExtractor,
			build: func() (*oauth2clientsgrpc.Server, error) {
				return oauth2clientsgrpc.NewServer(h.svc, h.store, h.db, nil)
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

	srv, err := oauth2clientsgrpc.NewServer(h.svc, h.store, h.db, extractPrincipal, nil)
	must.NoError(t, err)
	test.NotNil(t, srv)
}

// TestServerRegistersOnAGRPCServer is the mounting a consumer performs, and the
// reason RegisterOn has server/grpc's RegistrationFunc signature: mounting the
// registry beside the directory is one more entry in a slice rather than a
// closure per service.
func TestServerRegistersOnAGRPCServer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	srv := grpc.NewServer()
	t.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	// Registered under the name the generated descriptor declares, which is what
	// a client's generated stub dials.
	_, ok := srv.GetServiceInfo()[oauth2clientspb.OAuth2ClientsService_ServiceDesc.ServiceName]
	test.True(t, ok, test.Sprintf("the service is not mounted under %s",
		oauth2clientspb.OAuth2ClientsService_ServiceDesc.ServiceName))
}

// TestAnExtractorThatAnswersWithNobodyIsAnAnonymousRequest is the second half of
// the principal seam's contract, and it is a different failure from the one the
// suites already cover.
//
// A context carrying no principal at all reports (nil, false). An extractor that
// reports (nil, true) — a consumer's interceptor that found a session and could
// not build a principal from it — is the case a `!ok` check alone would let
// through, and what it would let through is every RPC running against a nil
// principal.
func TestAnExtractorThatAnswersWithNobodyIsAnAnonymousRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	present := func(context.Context) (identitygrpc.Principal, bool) { return nil, true }

	srv, err := oauth2clientsgrpc.NewServer(h.svc, h.store, h.db, present)
	must.NoError(t, err)

	res, err := srv.ListOAuth2Clients(t.Context(), &oauth2clientspb.ListOAuth2ClientsRequest{})
	must.Error(t, err)
	test.Nil(t, res)
	test.ErrorIs(t, err, oauth2clientsgrpc.ErrNoPrincipal)
	test.EqOp(t, codes.Unauthenticated, status.Code(err))
}
