package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// TestNewServerRefusesEachMissingDependency is three separate refusals rather
// than one, because each of them fails differently in production if it is
// tolerated: no store is a surface whose every call panics, no client is one
// with no transaction to open and nothing to read on, and no principal
// extractor is either an unusable service or an open one.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	cases := map[string]struct {
		build func() (*commentsgrpc.Server, error)
		want  error
	}{
		"no store": {
			build: func() (*commentsgrpc.Server, error) {
				return commentsgrpc.NewServer(nil, h.db, extractPrincipal)
			},
			want: commentsgrpc.ErrNilStore,
		},
		"no database client": {
			build: func() (*commentsgrpc.Server, error) {
				return commentsgrpc.NewServer(h.store, nil, extractPrincipal)
			},
			want: commentsgrpc.ErrNilDatabaseClient,
		},
		"no principal extractor": {
			build: func() (*commentsgrpc.Server, error) {
				return commentsgrpc.NewServer(h.store, h.db, nil)
			},
			want: commentsgrpc.ErrNilPrincipalExtractor,
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

	srv, err := commentsgrpc.NewServer(h.store, h.db, extractPrincipal, nil)
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

	_, mounted := srv.GetServiceInfo()["primandproper.platform.comments.v1.CommentsService"]
	test.True(T, mounted)
}

// TestTheSurfaceIsBuiltOverTheStoreSeam pins that this package depends on the
// interface rather than on the SQL implementation, which is what lets a consumer
// with their own backing mount it.
func TestTheSurfaceIsBuiltOverTheStoreSeam(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	var store comments.Store = h.store

	srv, err := commentsgrpc.NewServer(store, h.db, extractPrincipal)
	must.NoError(T, err)

	var _ interface {
		RegisterOn(*grpc.Server)
	} = srv
}
