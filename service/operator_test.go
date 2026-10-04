package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// operatorLog is the audit.Recorder a service resolves, keeping what the
// surfaces record.
type operatorLog struct {
	entries []*audit.Entry
	mu      sync.Mutex
}

func (l *operatorLog) Record(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, entries...)

	return nil
}

func (l *operatorLog) recorded() []*audit.Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]*audit.Entry(nil), l.entries...)
}

// transactingClient is a database client whose transactions run their body,
// which is all an operator's record needs of one.
func transactingClient() *databasemock.ClientMock {
	return &databasemock.ClientMock{
		ReaderFunc: func() database.SQLQueryExecutor { return &databasemock.SQLQueryExecutorMock{} },
		WithTransactionFunc: func(_ context.Context, fn func(database.Tx) error) error {
			return fn(nil)
		},
	}
}

// holdsEverything is the grants of a caller the deployment made an operator of
// everything.
func holdsEverything(context.Context) (authorization.Grants, bool) {
	return authorization.AllowAll(), true
}

// TestRegisterTransports_operatorReads is the assertion that Transports.Grants
// reaches identity's operator bypass, that audit's administration service is
// mounted beside its log, and that the audit.Recorder the service resolved is
// what arms both.
func TestRegisterTransports_operatorReads(T *testing.T) {
	T.Parallel()

	caller := testPrincipal{userID: "operator_1", scope: tenancy.Global()}

	// identityOver mounts identity over a store in which the caller is a member
	// of nothing, so every account they name is refused by the row check.
	identityOver := func(t *testing.T, grants authorization.GrantsExtractor, log audit.Recorder) identitypb.IdentityServiceClient {
		t.Helper()

		store := &identitymock.StoreMock{
			GetMembershipFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string, string) (*identity.Membership, error) {
				return nil, identity.ErrMembershipNotFound
			},
			GetAccountFunc: func(_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, accountID string) (*identity.Account, error) {
				return &identity.Account{ID: accountID}, nil
			},
		}

		client := transactingClient()

		svc, err := identity.NewService(client, store, identity.NoopHooks{})
		must.NoError(t, err)

		i := newTransportInjector(t)
		do.ProvideValue[database.Client](i, client)
		do.ProvideValue[identity.Store](i, store)
		do.ProvideValue(i, svc)

		if log != nil {
			do.ProvideValue(i, log)
		}

		RegisterTransports(i, &Transports{
			Extractor:   withPrincipal,
			TenantOf:    DirectoryTenant,
			Authorizers: allAuthorizers(),
			Grants:      grants,
		})

		return identitypb.NewIdentityServiceClient(serveMounted(t, i, "identity gRPC", caller))
	}

	T.Run("identity lets a holder past the row check, and records it", func(t *testing.T) {
		t.Parallel()

		log := &operatorLog{}
		client := identityOver(t, holdsEverything, log)

		got, err := client.GetAccount(t.Context(), &identitypb.GetAccountRequest{AccountId: "acct_somebody_elses"})
		must.NoError(t, err)
		test.EqOp(t, "acct_somebody_elses", got.GetAccount().GetId())

		entries := log.recorded()
		must.SliceLen(t, 1, entries)
		test.EqOp(t, audit.EventOperatorBypass, entries[0].EventType)
		test.EqOp(t, "operator_1", entries[0].Actor.ID)
	})

	T.Run("identity with no recorder resolved lets nobody past", func(t *testing.T) {
		t.Parallel()

		client := identityOver(t, holdsEverything, nil)

		_, err := client.GetAccount(t.Context(), &identitypb.GetAccountRequest{AccountId: "acct_somebody_elses"})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("identity with no grants lets nobody past", func(t *testing.T) {
		t.Parallel()

		log := &operatorLog{}
		client := identityOver(t, nil, log)

		_, err := client.GetAccount(t.Context(), &identitypb.GetAccountRequest{AccountId: "acct_somebody_elses"})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.SliceEmpty(t, log.recorded())
	})

	// auditOver mounts audit over a reader that counts the reads across every
	// tenant, and returns the administration client.
	auditOver := func(t *testing.T, log audit.Recorder, across *atomic.Int32) auditpb.AuditAdministrationServiceClient {
		t.Helper()

		reader := &auditmock.ReaderMock{
			ListAcrossScopesFunc: func(context.Context, database.SQLQueryExecutor, *audit.Query, *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				across.Add(1)

				return &filtering.QueryFilteredResult[audit.Entry]{Data: []*audit.Entry{}}, nil
			},
		}

		i := newTransportInjector(t)
		do.ProvideValue[database.Client](i, transactingClient())
		do.ProvideValue[audit.Reader](i, reader)

		if log != nil {
			do.ProvideValue(i, log)
		}

		// No Grants: the administration service is gated at the method, and
		// asks the grants extractor nothing.
		RegisterTransports(i, &Transports{
			Extractor:   withPrincipal,
			TenantOf:    DirectoryTenant,
			Authorizers: allAuthorizers(),
		})

		return auditpb.NewAuditAdministrationServiceClient(serveMounted(t, i, "audit gRPC", caller))
	}

	T.Run("audit's administration service is armed by the recorder, and records each read", func(t *testing.T) {
		t.Parallel()

		var across atomic.Int32

		log := &operatorLog{}
		client := auditOver(t, log, &across)

		_, err := client.ListAnyEntries(t.Context(), &auditpb.ListAnyEntriesRequest{})
		must.NoError(t, err)
		test.EqOp(t, int32(1), across.Load())

		entries := log.recorded()
		must.SliceLen(t, 1, entries)
		test.EqOp(t, audit.EventOperatorBypass, entries[0].EventType)
	})

	T.Run("audit with no recorder resolved serves no operator's read", func(t *testing.T) {
		t.Parallel()

		var across atomic.Int32

		client := auditOver(t, nil, &across)

		_, err := client.ListAnyEntries(t.Context(), &auditpb.ListAnyEntriesRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.Unimplemented, status.Code(err))
		test.EqOp(t, int32(0), across.Load(), test.Sprint("an unrecorded server read every tenant's log"))
	})
}
