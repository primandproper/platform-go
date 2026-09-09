package grpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNewServerRefusesEachMissingDependency covers the four a server cannot be
// built without, and it is one test per dependency rather than a table because
// the interesting part is that each has its own sentinel — a consumer who wired
// three of the four is told which one is missing.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	db, err := sqlite.NewDatabaseClient(T.Context(),
		&testClientConfig{connectionString: filepath.Join(T.TempDir(), "waitlists.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = db.Close() })

	store, err := waitlists.NewSQLStore(db)
	must.NoError(T, err)

	T.Run("no store", func(t *testing.T) {
		t.Parallel()

		srv, newErr := waitlistsgrpc.NewServer(nil, db, extractPrincipal, permitWithdrawals())
		test.Nil(t, srv)
		must.Error(t, newErr)
		test.ErrorIs(t, newErr, waitlistsgrpc.ErrNilStore)
		test.True(t, platformerrors.Is(newErr, platformerrors.ErrNilInputParameter))
	})

	T.Run("no database client", func(t *testing.T) {
		t.Parallel()

		srv, newErr := waitlistsgrpc.NewServer(store, nil, extractPrincipal, permitWithdrawals())
		test.Nil(t, srv)
		must.Error(t, newErr)
		test.ErrorIs(t, newErr, waitlistsgrpc.ErrNilDatabaseClient)
	})

	T.Run("no principal extractor", func(t *testing.T) {
		t.Parallel()

		srv, newErr := waitlistsgrpc.NewServer(store, db, nil, permitWithdrawals())
		test.Nil(t, srv)
		must.Error(t, newErr)
		test.ErrorIs(t, newErr, waitlistsgrpc.ErrNilPrincipalExtractor)
	})

	// The seam with no default, refused at construction rather than defaulted —
	// see waitlistsgrpc.SignupAuthorizer for why every available default is
	// wrong in a way nothing reports.
	T.Run("no signup authorizer", func(t *testing.T) {
		t.Parallel()

		srv, newErr := waitlistsgrpc.NewServer(store, db, extractPrincipal, nil)
		test.Nil(t, srv)
		must.Error(t, newErr)
		test.ErrorIs(t, newErr, waitlistsgrpc.ErrNilSignupAuthorizer)
	})
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)
	test.NotNil(T, h.server)
}

// TestRegisterOnMountsTheService is the whole of what RegisterOn does, and it is
// worth one test because its signature is server/grpc's RegistrationFunc and a
// consumer passes it by name rather than calling it.
func TestRegisterOnMountsTheService(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv := grpc.NewServer()
	T.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	_, mounted := srv.GetServiceInfo()[waitlistspb.WaitlistsService_ServiceDesc.ServiceName]
	test.True(T, mounted)
}

// TestTheDefaultScopeResolverIsGlobal pins what a consumer who names no resolver
// gets, which is the single-tenant answer.
//
// It is the right default and not a lax one: a multi-tenant deployment that
// forgot to configure one offers every anonymous visitor the global catalog,
// which holds no lists, so the failure is a signup page with nothing on it
// rather than one that joins somebody to the wrong tenant's list. This asserts
// the second half of that sentence.
func TestTheDefaultScopeResolverIsGlobal(T *testing.T) {
	T.Parallel()

	h := newHarnessWithScope(T, nil)

	inTenant := h.seedOpenList(T, testScope)
	global := h.seedOpenList(T, tenancy.Global())

	res, err := h.server.ListOpenLists(h.anonCtx(T), &waitlistspb.ListOpenListsRequest{})
	must.NoError(T, err)

	must.SliceLen(T, 1, res.GetResults())
	test.EqOp(T, global.ID, res.GetResults()[0].GetId())
	test.NotEqOp(T, inTenant.ID, res.GetResults()[0].GetId())
}

// TestAResolverThatCannotPlaceARequestRefusesIt covers the one error a
// ScopeResolver may return, and the code it becomes: a request that arrived
// somewhere it cannot be placed is a bad request rather than a server fault.
func TestAResolverThatCannotPlaceARequestRefusesIt(T *testing.T) {
	T.Parallel()

	unplaceable := errors.New("no tenant on this connection")

	h := newHarnessWithScope(T, func(context.Context) (tenancy.Scope, error) {
		return tenancy.Scope{}, unplaceable
	})

	res, err := h.server.ListOpenLists(h.anonCtx(T), &waitlistspb.ListOpenListsRequest{})
	test.Nil(T, res)
	must.Error(T, err)

	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.ErrorIs(T, err, unplaceable)
}

// TestTheResolverIsNotAskedOfACallerWhoHasOne is the other half of "each call
// has exactly one source": a request carrying a principal never reaches the
// resolver, so a resolver that would have failed does not fail such a request.
func TestTheResolverIsNotAskedOfACallerWhoHasOne(T *testing.T) {
	T.Parallel()

	var asked bool

	h := newHarnessWithScope(T, func(context.Context) (tenancy.Scope, error) {
		asked = true

		return tenancy.Scope{}, errors.New("the resolver was asked")
	})

	list := h.seedOpenList(T, testScope)

	res, err := h.server.ListOpenLists(h.ctx(T), &waitlistspb.ListOpenListsRequest{})
	must.NoError(T, err)

	must.SliceLen(T, 1, res.GetResults())
	test.EqOp(T, list.ID, res.GetResults()[0].GetId())
	test.False(T, asked)
}

// TestEveryAdministrativeMethodRefusesAnAnonymousCaller is the second lock the
// handler holds behind the permission interceptor.
//
// It walks the service descriptor rather than a list, so an RPC added later is
// covered by it: whichever half the new method lands in, one of this test and
// TestEveryPublicMethodAnswersAnAnonymousCaller has something to say about it.
func TestEveryAdministrativeMethodRefusesAnAnonymousCaller(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for _, method := range administrativeMethods(T) {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			err := invokeByName(t, h, method, h.anonCtx(t))
			must.Error(t, err)

			test.EqOp(t, codes.Unauthenticated, status.Code(err), test.Sprintf(
				"%s answered an anonymous request with %v", method, status.Code(err)))
			test.ErrorIs(t, err, waitlistsgrpc.ErrNoPrincipal)
		})
	}
}
