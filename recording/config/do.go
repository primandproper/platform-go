package recordingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/config/injection"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// Register registers a *recording.Recorder with the injector.
//
// Prerequisites: an audit.Recorder (auditcfg.RegisterRecorder), a
// *webhooks.Emitter (webhookscfg.RegisterEmitter) and a
// callers.PrincipalExtractor must be registered before the Recorder is invoked.
// *Config is optional, and absent means its defaults.
//
// The extractor is the application's, and the one a deployment is most likely
// to have left out: a deployment that mounts no gRPC surface has had no reason
// to register one. Its absence is an error naming the type rather than a
// Recorder that records every write as unattributed. Register it with
// do.ProvideValue, keyed on the named type:
//
//	do.ProvideValue[callers.PrincipalExtractor](i, principalFromContext)
//
// Registering a Recorder is what turns recording on for every store a config
// package here builds; see InvokeHooks.
func Register(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*recording.Recorder, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := injection.InvokeOptional[*Config](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking the recording config")
		}

		if cfg == nil {
			cfg = &Config{}
		}

		entries, err := do.Invoke[audit.Recorder](i)
		if err != nil {
			return nil, err
		}

		events, err := do.Invoke[*webhooks.Emitter](i)
		if err != nil {
			return nil, err
		}

		principals, err := do.Invoke[callers.PrincipalExtractor](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"a recording.Recorder attributes every write to the caller a %s reads off its context, and none is registered",
				do.NameOf[callers.PrincipalExtractor]())
		}

		return NewRecorder(ctx, cfg, entries, events, principals, WithPillars(pillars))
	})
}

// InvokeHooks resolves the Hooks a config package's Register builds its store
// or service with, in the one order every package here reads it:
//
//  1. a Hooks the application registered under H, which is how a deployment
//     records differently, or names a package's NoopHooks to record nothing;
//  2. the package's RecordingHooks, built by recordingHooks over the
//     registered *recording.Recorder, when there is one;
//  3. the zero H, which the caller passes on as no option at all, so the
//     constructor's own NoopHooks default applies.
//
// Absence is the only thing either lookup absorbs. A Hooks or a Recorder that
// is registered and fails to build is returned as the error it is, rather than
// falling through to the next step: a store that quietly ran the noop in place
// of the hooks somebody registered would commit every write with none of the
// companions they registered them for.
//
// recordingHooks is a closure over the package's NewRecordingHooks rather than
// the constructor itself, because each returns its own concrete type and Go
// will not convert a function's result type. Whatever it returns beside an
// error is discarded here, so a nil *RecordingHooks never reaches the caller
// wrapped in a non-nil H.
func InvokeHooks[H any](i do.Injector, recordingHooks func(*recording.Recorder) (H, error)) (H, error) {
	var zero H

	registered, err := injection.InvokeOptional[H](i)
	if err != nil {
		return zero, platformerrors.Wrapf(err, "invoking %s", do.NameOf[H]())
	}

	if any(registered) != nil {
		return registered, nil
	}

	recorder, err := injection.InvokeOptional[*recording.Recorder](i)
	if err != nil {
		return zero, platformerrors.Wrap(err, "invoking the recording recorder")
	}

	if recorder == nil {
		return zero, nil
	}

	hooks, err := recordingHooks(recorder)
	if err != nil {
		return zero, platformerrors.Wrapf(err, "building the recording %s", do.NameOf[H]())
	}

	return hooks, nil
}
