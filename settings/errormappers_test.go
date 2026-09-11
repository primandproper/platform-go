package settings_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/settings"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix already checks that every
// exported sentinel here is decided about; what this file adds is what each one
// was decided to be, which is the part a reader of an API changes their client
// over.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpMsg  string
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"no such setting": {
			err:      settings.ErrDefinitionNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such setting",
			grpcCode: codes.NotFound,
		},
		"nobody has set it": {
			err:      settings.ErrValueNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "that setting has not been set",
			grpcCode: codes.NotFound,
		},
		"no value and no default": {
			err:      settings.ErrSettingUnset,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "that setting has no value and no default",
			grpcCode: codes.NotFound,
		},
		"read or written as the wrong kind": {
			err:      settings.ErrKindMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "that setting is not of the kind it was used as",
			grpcCode: codes.InvalidArgument,
		},
		"an enumeration naming one value twice": {
			err:      settings.ErrDuplicateEnumerationValue,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the setting's allowed values name one value twice",
			grpcCode: codes.InvalidArgument,
		},
		"a name already defined": {
			err:      settings.ErrDefinitionNameTaken,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a setting by that name is already defined",
			grpcCode: codes.AlreadyExists,
		},
		"an edit that would strand values": {
			err:      settings.ErrStrandedValues,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that edit would strand values subjects have already set",
			grpcCode: codes.FailedPrecondition,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := settings.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := settings.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "setting \"notifications.digest\"")

			_, _, ok = settings.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = settings.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheThreeNotFoundsSayDifferentThings is why the wording is registered as
// client-safe rather than left to the code.
//
// A setting that does not exist, a value nobody has stored, and a resolution
// with neither a value nor a default are three different things to tell
// somebody, and codes.NotFound says the same word about all three.
func TestTheThreeNotFoundsSayDifferentThings(T *testing.T) {
	T.Parallel()

	messages := map[string]struct{}{}

	for _, err := range []error{
		settings.ErrDefinitionNotFound,
		settings.ErrValueNotFound,
		settings.ErrSettingUnset,
	} {
		code, msg, ok := settings.HTTPMapper.Map(err)
		must.True(T, ok)
		must.EqOp(T, httperrors.ErrDataNotFound, code)

		messages[msg] = struct{}{}
	}

	test.MapLen(T, 3, messages, test.Sprint("two of the three not-found answers say the same thing"))
}

// TestTheStrandedValuesMessageSaysLessThanTheSentinel is the one place the
// HTTP message is deliberately narrower than the error it maps.
//
// The sentinel's wrapped form names the subject and the value that stopped the
// edit, which is exactly what an administrator needs and is a row out of a page
// that is behind its own grant. The status a gRPC client gets carries it; the
// body a browser renders does not.
func TestTheStrandedValuesMessageSaysLessThanTheSentinel(T *testing.T) {
	T.Parallel()

	wrapped := platformerrors.Wrapf(settings.ErrStrandedValues,
		"user %q has set %q", "user-1", "daily")

	_, msg, ok := settings.HTTPMapper.Map(wrapped)
	must.True(T, ok)

	test.StrNotContains(T, msg, "user-1")
	test.StrNotContains(T, msg, "daily")
}

// TestTheTwoMappersCoverTheSameSentinels is why a service exposing both
// transports answers one refusal the same way on either.
//
// Without it a sentinel added to one switch and not the other would come back
// considered on the transport somebody happened to test and codes.Unknown on
// the other, and which one a client got would depend on how it connected.
func TestTheTwoMappersCoverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, err := range []error{
		settings.ErrDefinitionNotFound,
		settings.ErrValueNotFound,
		settings.ErrSettingUnset,
		settings.ErrKindMismatch,
		settings.ErrDuplicateEnumerationValue,
		settings.ErrDefinitionNameTaken,
		settings.ErrStrandedValues,
	} {
		_, _, claimedByHTTP := settings.HTTPMapper.Map(err)
		_, claimedByGRPC := settings.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := settings.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := settings.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these and are asked first, so a
		// case here would be unreachable as well as a second copy of the
		// decision — and the second copy is the one that can drift.
		//
		// The last three are the ones worth naming, because they are refusals a
		// client reads and are still not this package's to map: a value that is
		// not of its setting's kind, a value outside the enumeration, and a kind
		// nothing implements all wrap errors.ErrUnrecognizedInputValue.
		for _, err := range []error{
			settings.ErrNilDatabaseClient,
			settings.ErrNilDefinition,
			settings.ErrNilExecutor,
			settings.ErrEmptyDefinitionName,
			settings.ErrEmptySubjectType,
			settings.ErrEmptySubjectID,
			settings.ErrEmptyEnumerationValue,
			settings.ErrMalformedValue,
			settings.ErrNotEnumerated,
			settings.ErrUnknownKind,
		} {
			_, _, ok := settings.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = settings.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			// And the platform's answer is a real one rather than a fallthrough,
			// which is what makes leaving it there a decision.
			_, _, byPlatform := httperrors.PlatformMapper.Map(err)
			test.True(t, byPlatform, test.Sprintf("%v is claimed by nobody at all", err))
		}
	})

	T.Run("a store misbehaving toward its own caller", func(t *testing.T) {
		t.Parallel()

		// A paged read that answered with the cursor it was handed reaches a
		// handler only through a service that shipped broken, and a 500 is the
		// honest reply.
		_, _, ok := settings.HTTPMapper.Map(settings.ErrCursorStalled)
		test.False(t, ok)

		_, ok = settings.GRPCMapper.Map(settings.ErrCursorStalled)
		test.False(t, ok)
	})
}

// TestClientSafeSentinelsAreTheSixRefusalsAPersonReads, and nothing that
// describes the system to whoever wired it up.
//
// A nil executor, a stalled cursor and a kind nothing implements are sentences
// for an operator's log; a client reading the code's name instead loses
// nothing. The six here are the ones where the code alone does not say what
// happened.
func TestClientSafeSentinelsAreTheSixRefusalsAPersonReads(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 6, settings.ClientSafeSentinels)

	for _, err := range []error{
		settings.ErrDefinitionNameTaken,
		settings.ErrKindMismatch,
		settings.ErrSettingUnset,
		settings.ErrStrandedValues,
		settings.ErrMalformedValue,
		settings.ErrNotEnumerated,
	} {
		test.True(T, slices.Contains(settings.ClientSafeSentinels, err),
			test.Sprintf("%v is not client-safe, so gRPC sends the code's name instead", err))
	}

	for _, err := range []error{
		settings.ErrCursorStalled,
		settings.ErrNilExecutor,
		settings.ErrUnknownKind,
	} {
		test.False(T, slices.Contains(settings.ClientSafeSentinels, err),
			test.Sprintf("%v describes the system rather than the caller", err))
	}
}
