/*
Package recordingcfg assembles a recording.Recorder from environment
configuration, and is where every other config package here asks whether one
exists.

The Recorder is built from three things a deployment already has: the
audit.Recorder its Audit block registers, the *webhooks.Emitter its Webhooks and
Outbox blocks register between them, and the callers.PrincipalExtractor the
application registers for its gRPC surfaces. The one thing left to configure is
where an entry is filed, which is FileBy.

# The default it makes

A Recorder in the container changes what every store registered beside it
writes. Each config package's Register resolves its store's Hooks through
InvokeHooks, in one order: a Hooks the application registered, then the
package's RecordingHooks if a *recording.Recorder is registered, then nothing,
which the store's constructor reads as its NoopHooks. A deployment that keeps an
audit log and publishes events therefore records every platform write without
writing a hook, and one that wants a package silent registers that package's
NoopHooks by name, which wins because it is registered.

The extractor is required. A deployment that registered a Recorder and no
extractor has asked for every write to be recorded as unattributed without
saying so, and the container refuses to build the Recorder rather than doing
that quietly.

# Which tier this is

The domain's, inherited from recording.
*/
package recordingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/audit"
	auditprivacy "github.com/primandproper/platform-go/v15/audit/privacy"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/dataprivacy/auditerasure"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// ErrNoShippedScopeResolvers indicates [Config.ScopeResolvers] asked of a
// filing rule whose chains this module cannot enumerate.
var ErrNoShippedScopeResolvers = errors.New("no shipped audit scope resolvers for this filing rule")

// FileBy names the rule an entry's scope is decided by.
type FileBy string

const (
	// FileByWrite files every entry under the scope the write ran in. It is the
	// default, and the right one for a store whose rows belong to a tenant.
	//
	// The scopes a subject's entries end up on are then the scopes their
	// writes ran in, which the module cannot enumerate, so the privacy
	// adapters' resolvers are the deployment's own and
	// [Config.ScopeResolvers] refuses this rule.
	FileByWrite FileBy = "write"

	// FileBySubject files an entry that names a subject under that subject's
	// scope, and one that names none under the write's. It is for a deployment
	// whose stores are global but whose entries are about a person or an
	// account, where "what happened to this subject" has to be a chain the log
	// can walk after the row no longer says. See recording.ScopeResolver.
	//
	// A deployment that files this way finds a subject's entries again with
	// the two resolvers written for it, which [Config.ScopeResolvers] hands out
	// together.
	FileBySubject FileBy = "subject"
)

// Config assembles a recording.Recorder.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// FileBy decides which scope an entry is filed under. Defaults to
	// FileByWrite.
	FileBy FileBy `env:"FILE_BY" json:"fileBy,omitempty" yaml:"fileBy,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.FileBy == "" {
		cfg.FileBy = FileByWrite
	}
}

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.FileBy, validation.In(FileByWrite, FileBySubject)),
	)
}

// NewRecorder builds a Recorder from configuration over the three things it
// records through. Each is required, and recording.New says why.
//
// Explicit recorder options run after the config-derived ones, so a caller can
// still override anything, the scope resolver included.
func NewRecorder(
	ctx context.Context,
	cfg *Config,
	entries audit.Recorder,
	events *webhooks.Emitter,
	principals callers.PrincipalExtractor,
	opts ...Option,
) (*recording.Recorder, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating recording config")
	}

	o := newOptions(opts)

	base := []recording.Option{
		recording.WithLogger(o.logger),
		recording.WithTracerProvider(o.tracerProvider),
	}
	if cfg.FileBy == FileBySubject {
		base = append(base, recording.WithScopeResolver(subjectScope))
	}

	recorder, err := recording.New(entries, events, principals, append(base, o.recorder...)...)
	if err != nil {
		return nil, err
	}

	return recorder, nil
}

// ScopeResolvers hands out the audit privacy adapters' resolvers for the rule
// this Config files by: collect for audit/privacy's collector, bound to its
// executor with On, and erase for dataprivacy/auditerasure's eraser.
//
//	collect, erase, err := cfg.ScopeResolvers(directory, log)
//	collector, err := auditprivacy.NewCollector(log, q, collect.On(q))
//	registered, err := dataprivacycfg.RegisterAuditEraser(ctx, privacyCfg, registry, erase)
//
// The rule that files an entry is the one that knows where to find it again,
// so the pair is read off the rule rather than chosen beside it: a deployment
// that files by subject and picked its resolvers separately could export one
// rule's chains and erase another's, and nothing at any layer would say so.
//
// Under FileBySubject that is audit/privacy's MembershipScopeResolver and
// auditerasure.OwnedScopeResolver: an export reads the subject's own chain,
// every account they belong to, and every chain holding an entry they acted
// in; an erasure deletes their own chain and those of the accounts they own.
// Under FileByWrite, the default, an entry is on whatever scope its write ran
// in, which this module cannot enumerate, so it returns
// [ErrNoShippedScopeResolvers] and the deployment passes resolvers of its own.
func (cfg *Config) ScopeResolvers(
	directory identity.Store,
	log audit.Reader,
) (collect, erase dataprivacy.ExecutorScopeResolver, err error) {
	if cfg == nil {
		return nil, nil, errors.ErrNilInputParameter
	}

	if cfg.FileBy != FileBySubject {
		fileBy := cfg.FileBy
		if fileBy == "" {
			fileBy = FileByWrite
		}

		return nil, nil, errors.Wrapf(ErrNoShippedScopeResolvers, "filing by %q", fileBy)
	}

	// Refused here, at wiring, rather than by the resolvers on the first
	// privacy request, which is the first time anybody would notice.
	if directory == nil {
		return nil, nil, errors.Wrap(errors.ErrNilInputParameter, "nil identity directory for the audit scope resolvers")
	}

	if log == nil {
		return nil, nil, errors.Wrap(errors.ErrNilInputParameter, "nil audit reader for the audit scope resolvers")
	}

	return auditprivacy.MembershipScopeResolver(directory, log), auditerasure.OwnedScopeResolver(directory), nil
}

// subjectScope is FileBySubject's resolver: the subject's scope where the entry
// names one, and the write's where it does not.
func subjectScope(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
	if entry.SubjectID == "" {
		return scope
	}

	return tenancy.Of(entry.SubjectID)
}
