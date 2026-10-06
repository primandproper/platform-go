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
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// FileBy names the rule an entry's scope is decided by.
type FileBy string

const (
	// FileByWrite files every entry under the scope the write ran in. It is the
	// default, and the right one for a store whose rows belong to a tenant.
	//
	// The scopes a subject's entries end up on are then the scopes their
	// writes ran in, which the module cannot enumerate, so the privacy
	// adapters' resolvers are the deployment's own. The two shipped ones,
	// privacy.MembershipScopeResolver in audit/privacy and
	// auditerasure.OwnedScopeResolver, answer for FileBySubject and are wrong
	// here: they would read and delete chains this rule never wrote to.
	FileByWrite FileBy = "write"

	// FileBySubject files an entry that names a subject under that subject's
	// scope, and one that names none under the write's. It is for a deployment
	// whose stores are global but whose entries are about a person or an
	// account, where "what happened to this subject" has to be a chain the log
	// can walk after the row no longer says. See recording.ScopeResolver.
	//
	// A deployment that files this way finds a subject's entries again with
	// the two resolvers written for it: privacy.MembershipScopeResolver in
	// audit/privacy for an export, and auditerasure.OwnedScopeResolver for an
	// erasure. Choosing this rule and leaving the audit eraser on its default
	// resolver erases the subject's own chain and none of the accounts they
	// own.
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

// subjectScope is FileBySubject's resolver: the subject's scope where the entry
// names one, and the write's where it does not.
func subjectScope(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
	if entry.SubjectID == "" {
		return scope
	}

	return tenancy.Of(entry.SubjectID)
}
