package privacyadapters

import (
	"github.com/primandproper/platform-go/v15/audit"
	auditprivacy "github.com/primandproper/platform-go/v15/audit/privacy"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/dataprivacy/auditerasure"
	"github.com/primandproper/platform-go/v15/identity"
	recordingcfg "github.com/primandproper/platform-go/v15/recording/config"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// ErrNoShippedScopeResolvers indicates [AuditScopeResolvers] asked of a filing
// rule whose chains this module cannot enumerate.
var ErrNoShippedScopeResolvers = platformerrors.New("no shipped audit scope resolvers for this filing rule")

// AuditScopeResolvers hands out the audit adapters' resolvers for the rule a
// deployment's recorder files by: collect for [AuditAdapter], bound to the
// collector's executor with On, and erase for [AuditErasureAdapter] or
// dataprivacycfg.RegisterAuditEraser.
//
//	collect, erase, err := privacyadapters.AuditScopeResolvers(recordingCfg.FileBy, directory, log)
//	adapters.Audit = &privacyadapters.AuditAdapter{Log: log, Resolve: collect.On(q)}
//	adapters.AuditErasure = &privacyadapters.AuditErasureAdapter{Dialect: d, Resolve: erase}
//
// The rule that files an entry is the one that knows where to find it again,
// so the pair is read off the rule rather than chosen beside it: a deployment
// that files by subject and picked its resolvers separately could export one
// rule's chains and erase another's, and nothing at any layer would say so.
// The rule is still recordingcfg's to state; this reads it.
//
// It lives here rather than on recordingcfg.Config because every store's
// config package imports recordingcfg, and the resolvers would bring the
// privacy pipeline — dataprivacy, and operations and workqueue beneath it —
// into all of them. This package links every adapter already.
//
// Under recordingcfg.FileBySubject that is auditprivacy.MembershipScopeResolver
// and auditerasure.OwnedScopeResolver: an export reads the subject's own chain,
// every account they belong to, and every chain holding an entry they acted
// in; an erasure deletes their own chain and those of the accounts they own
// that nobody else belongs to.
// Under recordingcfg.FileByWrite, which an empty FileBy defaults to, an entry
// is on whatever scope its write ran in, which this module cannot enumerate,
// so it returns [ErrNoShippedScopeResolvers] and the deployment passes
// resolvers of its own.
func AuditScopeResolvers(
	fileBy recordingcfg.FileBy,
	directory identity.Store,
	log audit.Reader,
) (collect, erase dataprivacy.ExecutorScopeResolver, err error) {
	if fileBy != recordingcfg.FileBySubject {
		if fileBy == "" {
			fileBy = recordingcfg.FileByWrite
		}

		return nil, nil, platformerrors.Wrapf(ErrNoShippedScopeResolvers, "filing by %q", fileBy)
	}

	// Refused here, at wiring, rather than by the resolvers on the first
	// privacy request, which is the first time anybody would notice.
	if directory == nil {
		return nil, nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity directory for the audit scope resolvers")
	}

	if log == nil {
		return nil, nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit reader for the audit scope resolvers")
	}

	return auditprivacy.MembershipScopeResolver(directory, log), auditerasure.OwnedScopeResolver(directory), nil
}
