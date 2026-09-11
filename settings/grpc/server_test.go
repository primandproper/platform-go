package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// TestNewServerRefusesEachMissingDependency is four separate refusals rather
// than one, because each of them fails differently in production if it is
// tolerated: no store is a surface whose reads panic, no client is one with no
// transaction to open, no principal extractor is either an unusable service or
// an open one, and no subject authorizer is a service where a grant on SetValue
// is a grant to write anybody's settings.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)
	authorizer := settingsgrpc.SubjectAuthorizerFunc(selfOnly)

	cases := map[string]struct {
		build func() (*settingsgrpc.Server, error)
		want  error
	}{
		"no store": {
			build: func() (*settingsgrpc.Server, error) {
				return settingsgrpc.NewServer(nil, h.db, extractPrincipal, authorizer)
			},
			want: settingsgrpc.ErrNilStore,
		},
		"no database client": {
			build: func() (*settingsgrpc.Server, error) {
				return settingsgrpc.NewServer(h.store, nil, extractPrincipal, authorizer)
			},
			want: settingsgrpc.ErrNilDatabaseClient,
		},
		"no principal extractor": {
			build: func() (*settingsgrpc.Server, error) {
				return settingsgrpc.NewServer(h.store, h.db, nil, authorizer)
			},
			want: settingsgrpc.ErrNilPrincipalExtractor,
		},
		"no subject authorizer": {
			build: func() (*settingsgrpc.Server, error) {
				return settingsgrpc.NewServer(h.store, h.db, extractPrincipal, nil)
			},
			want: settingsgrpc.ErrNilSubjectAuthorizer,
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

	h := newHarness(T, nil)

	srv, err := settingsgrpc.NewServer(h.store, h.db, extractPrincipal,
		settingsgrpc.SubjectAuthorizerFunc(selfOnly), nil)
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestRegisterOnMountsTheService is the signature server/grpc's
// RegistrationFunc wants, checked by using it as one.
func TestRegisterOnMountsTheService(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	srv := grpc.NewServer()
	T.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	_, mounted := srv.GetServiceInfo()["primandproper.platform.settings.v1.SettingsService"]
	test.True(T, mounted)
}

// TestTheServerSatisfiesTheGeneratedInterface keeps the thirteen handlers and
// the schema from drifting: an RPC added to the .proto and not implemented here
// fails to compile rather than answering Unimplemented at runtime.
//
// The embedded Unimplemented struct is what makes that a choice rather than a
// certainty — it is left in place deliberately, so an RPC added later is
// additive for consumers who implemented the interface exhaustively.
func TestTheServerSatisfiesTheGeneratedInterface(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	var _ interface {
		RegisterOn(*grpc.Server)
	} = h.server

	// And the seam the server is built over is the interface rather than the
	// concrete type, so a consumer with a store of their own can mount this
	// surface over it.
	var _ settings.Store = h.store
}
