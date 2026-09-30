package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/callers"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"google.golang.org/grpc"
)

// The resource types an operator's read is filed under: one entry by id, and
// the log as a whole for a page of it.
const (
	auditResourceEntry = "audit_entry"
	auditResourceLog   = "audit_log"
)

// WithGrantsExtractor supplies what the caller may do, which this surface reads
// for one thing: whether they hold [PermissionOperatorRead], and so read past
// the connection's chains. A nil extractor is ignored.
//
// It is the same authorization.GrantsExtractor a consumer already hands
// primitives-go's authorization/grpc enforcer. Absent, nobody reads past their
// chains, and grants that could not be read are the same answer.
func WithGrantsExtractor(grants authorization.GrantsExtractor) Option {
	return func(s *Server) {
		if grants != nil {
			s.grants = grants
		}
	}
}

// WithOperatorPermission renames the permission that lets a caller read past
// the connection's chains. Absent, it is [PermissionOperatorRead]; empty, no
// caller does.
//
// It renames and does not grant. Nothing in this module gives the permission
// to anybody, under this name or the default one.
func WithOperatorPermission(read authorization.Permission) Option {
	return func(s *Server) { s.operatorRead = read }
}

// WithOperatorRecorder sets where an operator's read is recorded, and who is
// recorded as making it. Either nil is ignored.
//
// It is what arms [PermissionOperatorRead] at all. A read of another tenant's
// log is exactly the event an audit log exists to show, so a server with
// nowhere to record one widens nobody's read: absent, a holder reads their own
// chains like everybody else. The principal extractor is here, and nowhere
// else on this surface, because the scope is all a read needs to know about a
// caller and the entry is the one thing that needs to know who they are.
//
// The record is [audit.OperatorBypassEntry], filed in the connection's scope —
// the operator's own chain — in a transaction of its own and before the
// entries are returned. A record that could not be written is the read failing
// with codes.Internal rather than a read nobody can see.
func WithOperatorRecorder(recorder audit.Recorder, principals callers.PrincipalExtractor) Option {
	return func(s *Server) {
		if recorder != nil && principals != nil {
			s.operatorRecorder, s.principals = recorder, principals
		}
	}
}

// operator reports whether this request's caller reads past their chains, and
// the principal to record the read against when they do.
func (s *Server) operator(ctx context.Context) (callers.Principal, bool) {
	if s.operatorRead == "" || s.grants == nil || s.operatorRecorder == nil {
		return nil, false
	}

	grants, ok := s.grants(ctx)
	if !ok || !grants.Has(s.operatorRead) {
		return nil, false
	}

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		return nil, false
	}

	return principal, true
}

// recordOperatorRead files an operator's read in the connection's chain.
func (s *Server) recordOperatorRead(ctx context.Context, req *request, principal callers.Principal, resourceType, resourceID string) error {
	method, _ := grpc.Method(ctx)
	entry := audit.OperatorBypassEntry(principal, s.operatorRead, method, resourceType, resourceID)

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.operatorRecorder.Record(ctx, tx, req.scope, entry)
	}); err != nil {
		return platformerrors.Wrapf(err, "recording an operator's read of %s %q", resourceType, resourceID)
	}

	return nil
}
