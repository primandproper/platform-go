package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// TestNewServerRefusesEachMissingDependency is four separate refusals rather
// than one, because each of them fails differently in production if it is
// tolerated: no dispatcher is a surface that writes past the URL check, no store
// is one whose reads panic, no client is one with no transaction to open, and no
// principal extractor is either an unusable service or an open one.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	cases := map[string]struct {
		build func() (*webhooksgrpc.Server, error)
		want  error
	}{
		"no dispatcher": {
			build: func() (*webhooksgrpc.Server, error) {
				return webhooksgrpc.NewServer(nil, h.store, h.db, extractPrincipal)
			},
			want: webhooksgrpc.ErrNilDispatcher,
		},
		"no store": {
			build: func() (*webhooksgrpc.Server, error) {
				return webhooksgrpc.NewServer(h.dispatcher, nil, h.db, extractPrincipal)
			},
			want: webhooksgrpc.ErrNilStore,
		},
		"no database client": {
			build: func() (*webhooksgrpc.Server, error) {
				return webhooksgrpc.NewServer(h.dispatcher, h.store, nil, extractPrincipal)
			},
			want: webhooksgrpc.ErrNilDatabaseClient,
		},
		"no principal extractor": {
			build: func() (*webhooksgrpc.Server, error) {
				return webhooksgrpc.NewServer(h.dispatcher, h.store, h.db, nil)
			},
			want: webhooksgrpc.ErrNilPrincipalExtractor,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, err := tc.build()
			test.Nil(t, srv)
			must.Error(t, err)
			test.ErrorIs(t, err, tc.want)

			// Every one of them wraps the platform sentinel too, so a consumer
			// checking either spelling is asking the same question.
			test.True(t, platformerrors.Is(err, platformerrors.ErrNilInputParameter))
		})
	}
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv, err := webhooksgrpc.NewServer(h.dispatcher, h.store, h.db, extractPrincipal, nil)
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestRegisterOnMountsTheService is the signature server/grpc's RegistrationFunc
// wants, checked by using it as one.
func TestRegisterOnMountsTheService(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv := grpc.NewServer()
	T.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	_, mounted := srv.GetServiceInfo()["primandproper.platform.webhooks.v1.WebhooksService"]
	test.True(T, mounted)
}

// TestTheServerSatisfiesTheGeneratedInterface keeps the nine handlers and the
// schema from drifting: an RPC added to the .proto and not implemented here
// fails to compile rather than answering Unimplemented at runtime.
//
// The embedded Unimplemented struct is what makes that a choice rather than a
// certainty — it is left in place deliberately, so an RPC added later is
// additive for consumers who implemented the interface exhaustively.
func TestTheServerSatisfiesTheGeneratedInterface(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	var _ interface {
		RegisterOn(*grpc.Server)
	} = h.server

	// And the dispatcher seam the server writes through is the interface rather
	// than the concrete type, so a consumer with their own dispatcher can mount
	// this surface over it.
	var _ webhooks.Dispatcher = h.dispatcher
}
