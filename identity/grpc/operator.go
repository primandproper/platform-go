package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/callers"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"google.golang.org/grpc"
)

// The resource types an operator's admission is filed under, one per question
// [TargetAuthorizer] asks.
const (
	auditResourceAccount    = "identity_account"
	auditResourceUser       = "identity_user"
	auditResourceInvitation = "identity_invitation"
)

// access is which of the two operator permissions a row check falls back on.
type access int

const (
	// reading is a call that discloses the row and changes nothing.
	reading access = iota

	// acting is a call that changes the row, or something hanging off it.
	acting
)

// WithGrantsExtractor supplies what the caller may do, which this surface reads
// for two things: whether a caller [TargetAuthorizer] refused holds an operator
// permission that lets them through anyway, and whether a paged read's
// include_archived is honored or cleared. A nil extractor is ignored.
//
// It is the same authorization.GrantsExtractor a consumer already hands
// primitives-go's authorization/grpc enforcer, so the interceptor that decided
// the caller may make this call and the handler deciding which rows they may
// make it against read one authority and cannot disagree. It is not
// [WithPermissionResolver]'s seam: that one answers what a principal may do in
// an account GetPrincipal names, and this one what the request's own session
// may do.
//
// Absent, both answers are the safe one. Nobody is an operator here, so every
// refusal the row rule gives stands, and include_archived is cleared on every
// read, so a deployment that has not wired it serves live rows to everybody
// rather than archived ones to anybody. A caller's grants that could not be
// read are the same answer. See filterFromProto.
func WithGrantsExtractor(grants authorization.GrantsExtractor) Option {
	return func(s *Server) {
		if grants != nil {
			s.grants = grants
		}
	}
}

// WithOperatorPermission renames the two permissions that let a caller past the
// row rule: read for the reads that consult it and act for the writes. Absent,
// they are [PermissionOperatorRead] and [PermissionOperatorAct].
//
// An empty permission closes that half, so a deployment whose operators may
// read somebody's roster and never change it writes
// WithOperatorPermission(PermissionOperatorRead, "").
//
// It renames and does not grant. Nothing in this module gives either
// permission to anybody, under this name or the default one.
func WithOperatorPermission(read, act authorization.Permission) Option {
	return func(s *Server) {
		s.operatorRead, s.operatorAct = read, act
	}
}

// WithOperatorRecorder sets where an operator's admission is recorded. A nil
// recorder is ignored.
//
// It is what arms the operator permissions at all. An operator let into
// somebody else's row is exactly the event an audit log exists to show, so a
// server with nowhere to record one admits nobody on the strength of a
// permission: absent, every caller the row rule refused is refused, whatever
// they hold. The record is [audit.OperatorBypassEntry], filed in the caller's
// scope — the directory both the operator and the row are in.
//
// It is written in a transaction of its own, before the call proceeds, and a
// record that could not be written is the call refused with codes.Internal
// rather than an admission nobody can see. The entry records the admission and
// not the change: a write the operator goes on to make is recorded by the
// consumer's identity.Hooks inside that write's transaction, like every other,
// and a write that then fails leaves the admission on the log, which is still
// true — the operator was let in.
func WithOperatorRecorder(recorder audit.Recorder) Option {
	return func(s *Server) {
		if recorder != nil {
			s.operatorRecorder = recorder
		}
	}
}

// operatorPermission is the permission a row check falls back on for a call of
// the given kind.
func (s *Server) operatorPermission(kind access) authorization.Permission {
	if kind == acting {
		return s.operatorAct
	}

	return s.operatorRead
}

// admitOperator answers what [TargetAuthorizer] said, letting through a caller
// it refused who holds the operator permission for this kind of call.
//
// Anything but a refusal is handed back untouched: a permitted caller was not
// bypassing anything and is not recorded, and an authorizer that could not
// decide is an outage the permission does not paper over. The authorizer is
// asked first, rather than the grants, so that an operator reaching a row they
// hold standing in is an ordinary call and the log shows only the admissions
// the permission made.
func (s *Server) admitOperator(
	ctx context.Context,
	caller callers.Principal,
	kind access,
	resourceType, resourceID string,
	refusal error,
) error {
	if !errors.Is(refusal, callers.ErrTargetNotPermitted) {
		return refusal
	}

	permission := s.operatorPermission(kind)
	if permission == "" || s.grants == nil || s.operatorRecorder == nil || caller == nil {
		return refusal
	}

	grants, ok := s.grants(ctx)
	if !ok || !grants.Has(permission) {
		return refusal
	}

	method, _ := grpc.Method(ctx)
	entry := audit.OperatorBypassEntry(caller, permission, method, resourceType, resourceID)

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.operatorRecorder.Record(ctx, tx, caller.Scope(), entry)
	}); err != nil {
		return platformerrors.Wrapf(err, "recording an operator's admission to %s %q", resourceType, resourceID)
	}

	return nil
}
