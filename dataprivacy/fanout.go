package dataprivacy

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ScopeResolver names the scopes a subject's rows may be in.
//
// Every read and write in a domain that stores consumer data is scoped, and a
// subject access request may arrive without a scope — the request's confinement
// names nobody for the plain "give me my data". So every privacy adapter this
// module ships takes one of these: the mapping from a subject to the scopes
// their rows may be in, which is a question about the consumer's tenancy model
// rather than about any one table.
//
// It is a constructor argument in every adapter rather than an option with a
// default, because there is no default that is right twice. A deployment with
// one tenant wants [FixedScopes] over tenancy.Global(); one whose requests
// always arrive scoped wants [RequestScopeOr]; one that resolves a person to
// their accounts has to ask its own directory. A resolver that silently
// answered "the global scope" for the third of those would export nothing and
// erase nothing, and report success for both.
//
// Returning no scopes is legitimate and means the subject has nothing there:
// the collector reports the domain as holding nothing, and the eraser destroys
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's rows, so it is worth being exact.
//
// requestScope is the confinement the privacy request named, which the
// fulfiller hands over beside the subject. The zero Scope is the request that
// named none, and what a resolver makes of that is the whole of the decision
// this seam exists for.
//
// Each adapter aliases this type rather than declaring its own. The alias is
// load-bearing: a value of one defined type is assignable to another defined
// type only through a conversion, so ten separate declarations of this shape
// would mean a deployment with one resolver function writing nine conversions
// to hand it to nine adapters. A plain function literal, whose type is
// unnamed, is assignable to all of them at once.
type ScopeResolver func(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject Subject,
) ([]tenancy.Scope, error)

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
func FixedScopes(scopes ...tenancy.Scope) ScopeResolver {
	fixed := make([]tenancy.Scope, len(scopes))
	copy(fixed, scopes)

	return func(context.Context, tenancy.Scope, Subject) ([]tenancy.Scope, error) {
		return fixed, nil
	}
}

// RequestScopeOr resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped, and refuses with unscoped when
// one does not.
//
// A request that names no scope is refused rather than read as the global one.
// The confinement arrives as a tenancy.Scope, so "confined to nobody" and "the
// global scope" are already distinct values here and nothing has to reconstruct
// the difference; what a resolver still cannot do is invent the scope a request
// declined to name. The difference is not recoverable later: an export that
// quietly covered only the global scope would be well-formed, would have a
// section, and would be missing every row the subject actually has.
//
// The sentinel is an argument rather than one of this package's own, because
// each adapter words its refusal for the table it is about — a consumer
// matching that package's ErrUnscopedRequest is matching something it can read
// in a log — and a shared sentinel would take that wording away from all ten at
// once. A nil sentinel resolves to [ErrUnscopedRequest], so a caller that has
// no wording of its own still gets a refusal rather than a nil error beside an
// empty slice.
func RequestScopeOr(unscoped error) ScopeResolver {
	if unscoped == nil {
		unscoped = ErrUnscopedRequest
	}

	return func(_ context.Context, requestScope tenancy.Scope, subject Subject) ([]tenancy.Scope, error) {
		if requestScope.Validate() != nil {
			return nil, platformerrors.Wrapf(unscoped, "subject %q", subject.ID)
		}

		return []tenancy.Scope{requestScope}, nil
	}
}

// ForEachOwner resolves the owners a subject's rows sit under and runs fn once
// per owner. It is the one home for the per-owner fan-out every privacy adapter
// in this module makes.
//
// Owner is a type parameter because the axis is not always a scope. Ten
// adapters fan out over tenancy.Scope and billing/privacy fans out over its own
// Account, which carries a scope and an account id and whose halves are not
// inferable from one another. A helper fixed to tenancy.Scope would have left
// that one copy of the loop behind, which is the copy most likely to drift,
// being the only one.
//
// resolve is spelled as an unnamed function type rather than as [ScopeResolver]
// for the reason that type documents: it is what lets both an adapter's aliased
// ScopeResolver and billing/privacy's own AccountResolver arrive here with no
// conversion at the call site.
//
// Owners are neither sorted nor de-duplicated. A resolver that answers with the
// same scope twice has a bug, and an export that shows its rows twice or an
// outcome that reports them deleted twice is how somebody finds out; collapsing
// it here would make the bug invisible in exactly the artifact it is most
// expensive to be wrong in.
//
// The context is checked before each owner, for the reason CollectAll checks it
// between pages: an erasure whose deadline expired stops here rather than on
// the next scope's write.
//
// An error from fn passes through unwrapped. The caller's closure is where that
// domain's own words live — which table, which verb, and whether it calls one
// of these a scope or a registry — and a second wrap here would name the same
// failure twice in the one message an operator reads.
func ForEachOwner[Owner any](
	ctx context.Context,
	resolve func(ctx context.Context, requestScope tenancy.Scope, subject Subject) ([]Owner, error),
	requestScope tenancy.Scope,
	subject Subject,
	fn func(ctx context.Context, owner Owner) error,
) error {
	if resolve == nil {
		return ErrNilResolver
	}

	if fn == nil {
		return ErrNilFanOut
	}

	owners, err := resolve(ctx, requestScope, subject)
	if err != nil {
		return platformerrors.Wrapf(err, "resolving the scopes of subject %q", subject.ID)
	}

	for _, owner := range owners {
		if err = ctx.Err(); err != nil {
			return err
		}

		if err = fn(ctx, owner); err != nil {
			return err
		}
	}

	return nil
}

// CollectByScope fans one paged read out over the scopes a resolver names and
// returns every row the subject has in any of them.
//
// It is [ForEachOwner] and [CollectAll] composed, which is what eight of this
// module's collectors were before this existed. read is handed one scope and
// one filter and returns that scope's page; everything else — resolving,
// paging each scope to its end, concatenating, and stopping on a cancelled
// context — happens here.
//
// held names what the domain holds, for the error a failed read is wrapped in:
// "comments", "issue reports", "setting values". It is the domain's own noun
// rather than this package's, because the adapter that reads it in a log wants
// to know which table refused, and the registry key that would otherwise answer
// that is a section name rather than a sentence.
//
// The filter reaches read untouched. An adapter that wants archived rows copies
// the filter and sets IncludeArchived in its own closure, because the one
// collector in this module that must not do that — notifications' device
// registry, whose table has no archived_at — is the reader's whole reason for
// opening the file.
//
// An empty result is nil rather than an empty slice, so it composes with
// [Fragment]'s held flag without a length check at the call site.
func CollectByScope[T any](
	ctx context.Context,
	resolve ScopeResolver,
	requestScope tenancy.Scope,
	subject Subject,
	held string,
	read func(
		ctx context.Context,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[T], error),
) ([]T, error) {
	if read == nil {
		return nil, ErrNilFetch
	}

	var collected []T

	err := ForEachOwner(ctx, resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			page, collectErr := CollectAll(ctx,
				func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error) {
					return read(ctx, scope, filter)
				})
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting %s in scope %q", held, scope)
			}

			collected = append(collected, page...)

			return nil
		})
	if err != nil {
		return nil, err
	}

	return collected, nil
}

// EraseByScope fans one write out over the scopes a resolver names and sums
// what each of them reported.
//
// Deleted and Anonymized are summed. Retained is deliberately not carried: an
// adapter that retains something composes one sentence for the whole request
// out of the total — waitlists/privacy and mediaregistry/privacy are the two
// worked examples, and both do it after the fan-out — and N copies of the same
// paragraph, one per tenant, is not a better answer in front of a regulator. An
// adapter whose retention genuinely differs per scope calls [ForEachOwner] and
// composes its own.
//
// The first failing scope aborts, and the outcome is discarded rather than
// returned partial. Every eraser in one request shares one transaction, so
// there is nothing to report counts about: the write that already happened is
// about to roll back with the rest.
//
// held names what the domain holds, as it does for [CollectByScope].
func EraseByScope(
	ctx context.Context,
	tx database.Tx,
	resolve ScopeResolver,
	requestScope tenancy.Scope,
	subject Subject,
	held string,
	erase func(ctx context.Context, tx database.Tx, scope tenancy.Scope) (ErasureOutcome, error),
) (ErasureOutcome, error) {
	if tx == nil {
		return ErasureOutcome{}, ErrNilExecutor
	}

	if erase == nil {
		return ErasureOutcome{}, ErrNilErase
	}

	var outcome ErasureOutcome

	err := ForEachOwner(ctx, resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			scoped, eraseErr := erase(ctx, tx, scope)
			if eraseErr != nil {
				return platformerrors.Wrapf(eraseErr, "erasing %s in scope %q", held, scope)
			}

			outcome.Deleted += scoped.Deleted
			outcome.Anonymized += scoped.Anonymized

			return nil
		})
	if err != nil {
		return ErasureOutcome{}, err
	}

	return outcome, nil
}
