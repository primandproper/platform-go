package privacyadapters_test

import (
	"context"
	"fmt"
	"testing"

	billingprivacy "github.com/primandproper/platform-go/v14/billing/privacy"
	commentsmock "github.com/primandproper/platform-go/v14/comments/mock"
	commentsprivacy "github.com/primandproper/platform-go/v14/comments/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/privacyadapters"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"
	waitlistsprivacy "github.com/primandproper/platform-go/v14/waitlists/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingEraser stands in for a consumer's BeforeErase: it reports an outcome
// of its own and says whether it ran.
type recordingEraser struct {
	err     error
	outcome dataprivacy.ErasureOutcome
	ran     int
}

func (e *recordingEraser) Erase(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	_ dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	e.ran++

	if e.err != nil {
		return dataprivacy.ErasureOutcome{}, e.err
	}

	return e.outcome, nil
}

// commentsWith registers the comments adapter alone, with the given BeforeErase,
// and hands back the eraser that landed under its key.
//
// It goes through Register rather than reaching for the composition directly,
// because what is under test is that the field is wired at all — a seam that
// composes correctly and is never applied is the failure this package exists to
// close, one level up.
func commentsWith(t *testing.T, before dataprivacy.Eraser) dataprivacy.Eraser {
	t.Helper()

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Comments: &privacyadapters.CommentsAdapter{
			Store:       &commentsmock.StoreMock{},
			Resolve:     dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: before,
		},
	})
	must.NoError(t, err)

	eraser, ok := registry.Eraser(commentsprivacy.DefaultKey)
	must.True(t, ok)

	return eraser
}

// TestBeforeEraseRunsAheadOfTheDomain is the ordering the seam exists for: a
// succession rule, a tombstone, a transfer — something that has to be true
// before the domain's own rows go.
func TestBeforeEraseRunsAheadOfTheDomain(T *testing.T) {
	T.Parallel()

	var order []string

	before := dataprivacy.EraserFunc(func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ dataprivacy.Subject,
	) (dataprivacy.ErasureOutcome, error) {
		order = append(order, "before")

		return dataprivacy.ErasureOutcome{Deleted: 2}, nil
	})

	store := &commentsmock.StoreMock{}
	store.DeleteCommentsByAuthorFunc = func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ string,
	) (int64, error) {
		order = append(order, "domain")

		return 5, nil
	}

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Comments: &privacyadapters.CommentsAdapter{
			Store:       store,
			Resolve:     dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: before,
		},
	})
	must.NoError(T, err)

	eraser, ok := registry.Eraser(commentsprivacy.DefaultKey)
	must.True(T, ok)

	outcome, err := eraser.Erase(T.Context(), stubTx{}, tenancy.Global(), dataprivacy.Subject{ID: "somebody"})
	must.NoError(T, err)

	test.Eq(T, []string{"before", "domain"}, order,
		test.Sprint("the consumer's step has to be able to see the rows the domain is about to take"))

	// Both halves are in the record, summed. The consumer reports its own rows
	// and the domain reports its own; neither can restate the other's.
	test.EqOp(T, int64(7), outcome.Deleted,
		test.Sprint("two from the consumer's step and five from the domain's, summed"))
}

// TestBeforeEraseCannotReplaceTheDomain is the whole reason the seam is
// "before" rather than a wrapper.
//
// A consumer holds no handle to the domain's eraser, so there is nothing to
// forget to call, nothing to return in its place, and no count of its to
// rewrite. This pins the consequence rather than the shape: whatever the
// consumer's step does, the domain's own eraser ran.
func TestBeforeEraseCannotReplaceTheDomain(T *testing.T) {
	T.Parallel()

	domainRan := 0

	store := &commentsmock.StoreMock{}
	store.DeleteCommentsByAuthorFunc = func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ string,
	) (int64, error) {
		domainRan++

		return 5, nil
	}

	registry := dataprivacy.NewRegistry()

	// A step that reports a complete erasure it did not perform. Under a
	// wrapper seam this would be the whole answer.
	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Comments: &privacyadapters.CommentsAdapter{
			Store:   store,
			Resolve: dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: dataprivacy.EraserFunc(func(
				_ context.Context, _ database.Tx, _ tenancy.Scope, _ dataprivacy.Subject,
			) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{Deleted: 9999}, nil
			}),
		},
	})
	must.NoError(T, err)

	eraser, ok := registry.Eraser(commentsprivacy.DefaultKey)
	must.True(T, ok)

	outcome, err := eraser.Erase(T.Context(), stubTx{}, tenancy.Global(), dataprivacy.Subject{ID: "somebody"})
	must.NoError(T, err)

	test.EqOp(T, 1, domainRan, test.Sprint("the domain's eraser was skippable from a BeforeErase step"))
	test.EqOp(T, int64(10004), outcome.Deleted,
		test.Sprint("the domain's own count did not survive the consumer's step"))
}

// TestBeforeEraseErrorStopsTheErasure: a precondition that failed is a
// precondition. The alternative is a subject told they were erased who was not.
func TestBeforeEraseErrorStopsTheErasure(T *testing.T) {
	T.Parallel()

	domainRan := 0

	store := &commentsmock.StoreMock{}
	store.DeleteCommentsByAuthorFunc = func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ string,
	) (int64, error) {
		domainRan++

		return 5, nil
	}

	sentinel := platformerrors.New("the succession rule found nobody to hand the account to")

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Comments: &privacyadapters.CommentsAdapter{
			Store:       store,
			Resolve:     dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: &recordingEraser{err: sentinel},
		},
	})
	must.NoError(T, err)

	eraser, ok := registry.Eraser(commentsprivacy.DefaultKey)
	must.True(T, ok)

	_, err = eraser.Erase(T.Context(), stubTx{}, tenancy.Global(), dataprivacy.Subject{ID: "somebody"})
	must.ErrorIs(T, err, sentinel)

	test.EqOp(T, 0, domainRan,
		test.Sprint("the domain erased rows after its precondition had already failed"))
}

// TestBeforeEraseRefusesARetainedCollision: Retained's values are legal bases,
// so a key claimed twice is two accounts of one thing. Merging silently would
// drop one of them and leave the record looking complete in front of a
// regulator.
//
// waitlists is the domain used here because its eraser genuinely retains
// something — the contact digests it keeps when it withdraws a signup — so the
// collision is between two real claims rather than one invented for the test.
func TestBeforeEraseRefusesARetainedCollision(T *testing.T) {
	T.Parallel()

	store := &waitlistsmock.SignupStoreMock{}
	store.WithdrawSignupsForSubjectFunc = func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ waitlists.Subject,
	) (int64, error) {
		return 3, nil
	}

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Waitlists: &privacyadapters.WaitlistsAdapter{
			Store:   store,
			Resolve: dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: dataprivacy.EraserFunc(func(
				_ context.Context, _ database.Tx, _ tenancy.Scope, _ dataprivacy.Subject,
			) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{
					Retained: map[string]string{
						waitlistsprivacy.RetainedDigests: "kept under our own policy instead",
					},
				}, nil
			}),
		},
	})
	must.NoError(T, err)

	eraser, ok := registry.Eraser(waitlistsprivacy.DefaultKey)
	must.True(T, ok)

	_, err = eraser.Erase(T.Context(), stubTx{}, tenancy.Global(), dataprivacy.Subject{ID: "somebody"})
	must.ErrorIs(T, err, dataprivacy.ErrDuplicateKey)
}

// TestBeforeEraseMergesDistinctRetainedKeys is the other half: two bases that
// name different things both survive, because that is a consumer saying more
// rather than a consumer contradicting the domain.
func TestBeforeEraseMergesDistinctRetainedKeys(T *testing.T) {
	T.Parallel()

	store := &waitlistsmock.SignupStoreMock{}
	store.WithdrawSignupsForSubjectFunc = func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ waitlists.Subject,
	) (int64, error) {
		return 3, nil
	}

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
		Reader: stubReader{},
		Waitlists: &privacyadapters.WaitlistsAdapter{
			Store:   store,
			Resolve: dataprivacy.FixedScopes(tenancy.Global()),
			BeforeErase: dataprivacy.EraserFunc(func(
				_ context.Context, _ database.Tx, _ tenancy.Scope, _ dataprivacy.Subject,
			) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{
					Retained: map[string]string{"referrals": "kept 7 years under [statute]"},
				}, nil
			}),
		},
	})
	must.NoError(T, err)

	eraser, ok := registry.Eraser(waitlistsprivacy.DefaultKey)
	must.True(T, ok)

	outcome, err := eraser.Erase(T.Context(), stubTx{}, tenancy.Global(), dataprivacy.Subject{ID: "somebody"})
	must.NoError(T, err)

	must.MapLen(T, 2, outcome.Retained)
	test.MapContainsKey(T, outcome.Retained, "referrals")
	test.MapContainsKey(T, outcome.Retained, waitlistsprivacy.RetainedDigests)
}

// TestNilBeforeEraseChangesNothing: the field is nil in ordinary wiring, and a
// deployment that names none gets the eraser the adapter has always registered.
func TestNilBeforeEraseChangesNothing(T *testing.T) {
	T.Parallel()

	eraser := commentsWith(T, nil)
	must.NotNil(T, eraser)

	// Not the composition: with no step to precede there is nothing to compose,
	// and a wrapper around one eraser is a layer that can only go wrong.
	test.EqOp(T, "*privacy.Eraser", typeName(eraser))
}

// typeName is the concrete type behind an interface value, for the one
// assertion that is about which value was registered rather than what it does.
func typeName(v any) string {
	return fmt.Sprintf("%T", v)
}

// stubTx is a non-nil database.Tx that is never called. The adapters' erasers
// refuse a nil one before they reach a statement, and nothing here executes SQL.
type stubTx struct{ database.Tx }

// TestEveryEraserHonorsBeforeErase is the guard on the guard, and the reason it
// is driven off the registry rather than off a list here: a twelfth adapter that
// registers an eraser and forgets to apply its BeforeErase would compose
// correctly, register cleanly, and silently never run the consumer's step.
//
// That is this package's own failure mode one level down — a seam that is
// present, well-formed, and not wired — so it is checked the way the roster is,
// against what Register actually produced.
func TestEveryEraserHonorsBeforeErase(T *testing.T) {
	T.Parallel()

	step := dataprivacy.EraserFunc(func(
		_ context.Context, _ database.Tx, _ tenancy.Scope, _ dataprivacy.Subject,
	) (dataprivacy.ErasureOutcome, error) {
		return dataprivacy.ErasureOutcome{}, nil
	})

	adapters := everything()
	adapters.Comments.BeforeErase = step
	adapters.IssueReports.BeforeErase = step
	adapters.Settings.BeforeErase = step
	adapters.Waitlists.BeforeErase = step
	adapters.MediaRegistry.BeforeErase = step
	adapters.OAuth2Clients.BeforeErase = step
	adapters.Grants.BeforeErase = step
	adapters.Passkeys.BeforeErase = step
	adapters.PasswordReset.BeforeErase = step
	adapters.RecoveryCodes.BeforeErase = step
	adapters.Identity.BeforeErase = step
	adapters.Notifications.BeforeEraseInbox = step
	adapters.Notifications.BeforeEraseDevices = step
	adapters.AuditErasure.BeforeErase = step

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, adapters)
	must.NoError(T, err)

	keys := registry.EraserKeys()
	must.SliceNotEmpty(T, keys)

	for _, key := range keys {
		eraser, ok := registry.Eraser(key)
		must.True(T, ok, must.Sprintf("no eraser registered under %q", key))

		// The composition rather than the domain's own eraser, which is what
		// the field being applied looks like from outside this package.
		test.EqOp(T, "*privacyadapters.precedingEraser", typeName(eraser),
			test.Sprintf("the %s adapter registered an eraser that ignores its BeforeErase", key))
	}

	// billing is the one adapter with no BeforeErase to set, because it ships no
	// eraser for one to precede. It must still be here as a collector.
	test.SliceNotContains(T, keys, billingprivacy.DefaultKey)
	test.SliceContains(T, registry.CollectorKeys(), billingprivacy.DefaultKey)
}
