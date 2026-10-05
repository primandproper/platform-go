package recordingcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/recording"

	"github.com/primandproper/primitives-go/v2/errors"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recorderInjector is an injector holding everything Register resolves.
func recorderInjector(t *testing.T, withExtractor bool) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[audit.Recorder](i, &auditmock.RecorderMock{})
	do.ProvideValue(i, testEmitter(t))

	if withExtractor {
		do.ProvideValue[callers.PrincipalExtractor](i, nobody)
	}

	return i
}

func TestRegister(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := recorderInjector(t, true)
		do.ProvideValue(i, &Config{FileBy: FileBySubject})

		Register(i)

		recorder, err := do.Invoke[*recording.Recorder](i)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("with no config registered", func(t *testing.T) {
		t.Parallel()

		i := recorderInjector(t, true)

		Register(i)

		recorder, err := do.Invoke[*recording.Recorder](i)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})

	T.Run("a missing extractor is an error naming it", func(t *testing.T) {
		t.Parallel()

		i := recorderInjector(t, false)

		Register(i)

		_, err := do.Invoke[*recording.Recorder](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[callers.PrincipalExtractor]())
	})

	T.Run("a missing emitter is an error", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[audit.Recorder](i, &auditmock.RecorderMock{})
		do.ProvideValue[callers.PrincipalExtractor](i, nobody)

		Register(i)

		_, err := do.Invoke[*recording.Recorder](i)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})
}

// testHooks stands in for a package's Hooks interface.
type testHooks interface{ name() string }

type namedHooks string

func (h namedHooks) name() string { return string(h) }

// buildRecordingHooks stands in for a package's NewRecordingHooks.
func buildRecordingHooks(r *recording.Recorder) (testHooks, error) {
	if r == nil {
		return nil, recording.ErrNilAuditRecorder
	}

	return namedHooks("recording"), nil
}

func TestInvokeHooks(T *testing.T) {
	T.Parallel()

	T.Run("registered hooks win over a recorder", func(t *testing.T) {
		t.Parallel()

		i := recorderInjector(t, true)
		Register(i)
		do.ProvideValue[testHooks](i, namedHooks("registered"))

		hooks, err := InvokeHooks(i, buildRecordingHooks)
		must.NoError(t, err)
		test.EqOp(t, "registered", hooks.name())
	})

	T.Run("a recorder and no registered hooks builds the recording hooks", func(t *testing.T) {
		t.Parallel()

		i := recorderInjector(t, true)
		Register(i)

		hooks, err := InvokeHooks(i, buildRecordingHooks)
		must.NoError(t, err)
		must.NotNil(t, hooks)
		test.EqOp(t, "recording", hooks.name())
	})

	T.Run("neither is nothing, and not an error", func(t *testing.T) {
		t.Parallel()

		hooks, err := InvokeHooks(do.New(), buildRecordingHooks)
		must.NoError(t, err)
		test.Nil(t, hooks)
	})

	T.Run("registered hooks that fail to build are returned, not replaced", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the hooks")

		i := recorderInjector(t, true)
		Register(i)
		do.Provide(i, func(do.Injector) (testHooks, error) { return nil, errBuild })

		hooks, err := InvokeHooks(i, buildRecordingHooks)
		test.ErrorIs(t, err, errBuild)
		test.Nil(t, hooks)
	})

	T.Run("a recorder that fails to build is returned, not skipped", func(t *testing.T) {
		t.Parallel()

		// The extractor is missing, so the registered Recorder cannot be built,
		// and falling through to no hooks would be recording nothing quietly.
		i := recorderInjector(t, false)
		Register(i)

		hooks, err := InvokeHooks(i, buildRecordingHooks)
		test.Error(t, err)
		test.Nil(t, hooks)
	})

	T.Run("recording hooks that fail to build are an error and no hooks", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the recording hooks")

		i := recorderInjector(t, true)
		Register(i)

		hooks, err := InvokeHooks(i, func(*recording.Recorder) (testHooks, error) {
			return namedHooks("half-built"), errBuild
		})
		test.ErrorIs(t, err, errBuild)
		test.Nil(t, hooks)
	})
}
