package grpc_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/callers"
	identitygrpc "github.com/primandproper/platform-go/v15/identity/grpc"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// bypassLog is an audit.Recorder that keeps what it was handed, and refuses
// every record when told to.
type bypassLog struct {
	fail    error
	entries []*audit.Entry
	scopes  []tenancy.Scope
	mu      sync.Mutex
}

var _ audit.Recorder = (*bypassLog)(nil)

func (l *bypassLog) Record(_ context.Context, tx database.Tx, scope tenancy.Scope, entries ...*audit.Entry) error {
	if tx == nil {
		return audit.ErrNilExecutor
	}

	if l.fail != nil {
		return l.fail
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, entries...)
	for range entries {
		l.scopes = append(l.scopes, scope)
	}

	return nil
}

func (l *bypassLog) recorded() []*audit.Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]*audit.Entry(nil), l.entries...)
}

// grantsFor is a GrantsExtractor that hands one user the permissions named and
// everybody else nothing.
func grantsFor(userID string, perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(ctx context.Context) (authorization.Grants, bool) {
		p, ok := extractPrincipal(ctx)
		if !ok {
			return authorization.DenyAll(), false
		}

		if p.UserID() != userID {
			return authorization.DenyAll(), true
		}

		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

// operatorNeighborhood is the neighborhood with an operator in it: a user in
// the directory who is a member of neither account, holding the permissions
// named, on a server that records admissions to log.
func operatorNeighborhood(
	t *testing.T,
	log *bypassLog,
	perms []authorization.Permission,
	opts ...identitygrpc.Option,
) (n *neighborhood, operator context.Context) {
	t.Helper()

	// The operator's ID is fixed before the server is built, because the
	// extractor that recognizes them is one of the server's options.
	const operatorName = "operator"

	var operatorID string

	extractor := func(ctx context.Context) (authorization.Grants, bool) {
		return grantsFor(operatorID, perms...)(ctx)
	}

	base := []identitygrpc.Option{identitygrpc.WithGrantsExtractor(extractor)}
	if log != nil {
		base = append(base, identitygrpc.WithOperatorRecorder(log))
	}

	n = newNeighborhood(t, append(base, opts...)...)
	operatorID = n.h.seedUser(t, testScope, operatorName).ID

	return n, n.h.as(&testPrincipal{userID: operatorID, scope: testScope})
}

func getTheirAccount(ctx context.Context, n *neighborhood) error {
	_, err := n.h.client.GetAccount(ctx, &identitypb.GetAccountRequest{AccountId: n.theirs.Account.ID})

	return err
}

func renameTheirAccount(ctx context.Context, n *neighborhood) error {
	_, err := n.h.client.UpdateAccount(ctx, &identitypb.UpdateAccountRequest{
		AccountId: n.theirs.Account.ID,
		Input:     &identitypb.AccountUpdateInput{Name: new("Renamed by an operator")},
	})

	return err
}

func refusedAsNotYours(t *testing.T, err error) {
	t.Helper()

	must.Error(t, err)
	test.EqOp(t, codes.PermissionDenied, status.Code(err))
	test.True(t, errors.Is(err, callers.ErrTargetNotPermitted))
}

func TestAnOperatorPermissionLetsItsHolderPastTheRowRule(T *testing.T) {
	T.Parallel()

	T.Run("a holder of the read permission reads another owner's account, and is recorded", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorRead})

		got, err := n.h.client.GetAccount(operator, &identitypb.GetAccountRequest{AccountId: n.theirs.Account.ID})
		must.NoError(t, err)
		test.EqOp(t, n.theirs.Account.ID, got.GetAccount().GetId())

		entries := log.recorded()
		must.SliceLen(t, 1, entries)

		entry := entries[0]
		test.EqOp(t, audit.EventOperatorBypass, entry.EventType)
		test.EqOp(t, "identity_account", entry.ResourceType)
		test.EqOp(t, n.theirs.Account.ID, entry.ResourceID)
		test.NotEq(t, "", entry.Actor.ID)
		test.EqOp(t, audit.ActorUser, entry.Actor.Type)
		test.EqOp(t, string(identitygrpc.PermissionOperatorRead), entry.Metadata[audit.MetadataOperatorPermission])
		test.EqOp(t, identitypb.IdentityService_GetAccount_FullMethodName, entry.Metadata[audit.MetadataOperatorMethod])
		test.EqOp(t, testScope.Owner(), log.scopes[0].Owner())
	})

	T.Run("a caller without it gets the usual refusal, and nothing is recorded", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n, _ := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorRead})

		// The owner of mine holds every method permission this harness
		// grants, and neither operator permission.
		refusedAsNotYours(t, getTheirAccount(n.ctx(), n))
		test.SliceEmpty(t, log.recorded())
	})

	T.Run("reading does not let its holder act", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorRead})

		refusedAsNotYours(t, renameTheirAccount(operator, n))
		test.SliceEmpty(t, log.recorded())
	})

	T.Run("acting does not let its holder read", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorAct})

		refusedAsNotYours(t, getTheirAccount(operator, n))
		must.NoError(t, renameTheirAccount(operator, n))

		entries := log.recorded()
		must.SliceLen(t, 1, entries)
		test.EqOp(t, string(identitygrpc.PermissionOperatorAct), entries[0].Metadata[audit.MetadataOperatorPermission])
		test.EqOp(t, identitypb.IdentityService_UpdateAccount_FullMethodName, entries[0].Metadata[audit.MetadataOperatorMethod])
	})

	T.Run("an invitation the operator neither sent nor is into is read on the permission", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorRead})

		got, err := n.h.client.GetInvitation(operator,
			&identitypb.GetInvitationRequest{InvitationId: n.theirInvitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, n.theirInvitation.GetId(), got.GetInvitation().GetId())

		entries := log.recorded()
		must.SliceLen(t, 1, entries)
		test.EqOp(t, "identity_invitation", entries[0].ResourceType)
		test.EqOp(t, n.theirInvitation.GetId(), entries[0].ResourceID)
	})

	T.Run("a row the holder already has standing in is an ordinary call, and is not recorded", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n := newNeighborhood(t,
			identitygrpc.WithGrantsExtractor(func(context.Context) (authorization.Grants, bool) {
				return authorization.AllowAll(), true
			}),
			identitygrpc.WithOperatorRecorder(log))

		_, err := n.h.client.GetAccount(n.ctx(), &identitypb.GetAccountRequest{AccountId: n.mine.Account.ID})
		must.NoError(t, err)
		test.SliceEmpty(t, log.recorded())
	})
}

func TestAnOperatorAdmissionNobodyCanSeeIsNoAdmission(T *testing.T) {
	T.Parallel()

	T.Run("with nowhere to record it, the holder is refused", func(t *testing.T) {
		t.Parallel()

		n, operator := operatorNeighborhood(t, nil,
			[]authorization.Permission{identitygrpc.PermissionOperatorRead, identitygrpc.PermissionOperatorAct})

		refusedAsNotYours(t, getTheirAccount(operator, n))
		refusedAsNotYours(t, renameTheirAccount(operator, n))
	})

	T.Run("a record that could not be written is a failure, not a refusal and not an admission", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{fail: errors.New("the audit log is down")}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{identitygrpc.PermissionOperatorAct})

		err := renameTheirAccount(operator, n)
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))

		account, err := n.h.store.GetAccount(t.Context(), n.h.db.Reader(), testScope, n.theirs.Account.ID)
		must.NoError(t, err)
		test.NotEq(t, "Renamed by an operator", account.Name)
	})

	T.Run("grants that could not be read admit nobody", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n := newNeighborhood(t,
			identitygrpc.WithGrantsExtractor(func(context.Context) (authorization.Grants, bool) {
				return authorization.AllowAll(), false
			}),
			identitygrpc.WithOperatorRecorder(log))

		refusedAsNotYours(t, getTheirAccount(n.ctx(), n))
		test.SliceEmpty(t, log.recorded())
	})
}

func TestTheOperatorPermissionsAreTheDeploymentsToName(T *testing.T) {
	T.Parallel()

	T.Run("a renamed permission admits its holder, and the default name no longer does", func(t *testing.T) {
		t.Parallel()

		const console authorization.Permission = "console.directory.read"

		log := &bypassLog{}
		n, operator := operatorNeighborhood(t, log, []authorization.Permission{console},
			identitygrpc.WithOperatorPermission(console, identitygrpc.PermissionOperatorAct))

		must.NoError(t, getTheirAccount(operator, n))

		entries := log.recorded()
		must.SliceLen(t, 1, entries)
		test.EqOp(t, string(console), entries[0].Metadata[audit.MetadataOperatorPermission])

		stale, staleOperator := operatorNeighborhood(t, &bypassLog{},
			[]authorization.Permission{identitygrpc.PermissionOperatorRead},
			identitygrpc.WithOperatorPermission(console, identitygrpc.PermissionOperatorAct))

		refusedAsNotYours(t, getTheirAccount(staleOperator, stale))
	})

	T.Run("an empty permission closes that half, even to a caller who holds everything", func(t *testing.T) {
		t.Parallel()

		log := &bypassLog{}
		n := newNeighborhood(t,
			identitygrpc.WithGrantsExtractor(func(context.Context) (authorization.Grants, bool) {
				return authorization.AllowAll(), true
			}),
			identitygrpc.WithOperatorRecorder(log),
			identitygrpc.WithOperatorPermission(identitygrpc.PermissionOperatorRead, ""))

		refusedAsNotYours(t, renameTheirAccount(n.ctx(), n))
		must.NoError(t, getTheirAccount(n.ctx(), n))
	})

	T.Run("neither is a method permission, so neither is in the default fragment", func(t *testing.T) {
		t.Parallel()

		for method, perms := range identitygrpc.Permissions() {
			for _, perm := range perms {
				test.NotEq(t, identitygrpc.PermissionOperatorRead, perm, test.Sprintf("%s requires the operator read permission", method))
				test.NotEq(t, identitygrpc.PermissionOperatorAct, perm, test.Sprintf("%s requires the operator act permission", method))
			}
		}
	})
}
