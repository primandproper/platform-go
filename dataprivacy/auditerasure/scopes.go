package auditerasure

import (
	"context"

	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

var (
	// ErrNilDirectory indicates a nil identity.Store handed to
	// [OwnedScopeResolver].
	ErrNilDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit erasure identity directory")

	// ErrAnonymousSubject indicates a subject with no ID handed to
	// [SubjectScope] or [OwnedScopeResolver]. tenancy.Of("") is the global scope, which
	// audit.Erasure.DeleteScopes refuses anyway; refusing it here names the
	// cause rather than the symptom.
	ErrAnonymousSubject = platformerrors.Wrap(platformerrors.ErrInvalidIDProvided, "audit erasure subject names no ID")
)

// ExecutorScopeResolver is a [ScopeResolver] that reads, and takes the
// executor its reads run on when it is called rather than when it is built.
// [New] hands it the erasure's own transaction, so it reads the directory as
// that transaction sees it — an ownership another eraser transferred earlier in
// the same request included.
//
// It is [dataprivacy.ExecutorScopeResolver] under this package's name, for the
// reason [ScopeResolver] is an alias too.
type ExecutorScopeResolver = dataprivacy.ExecutorScopeResolver

// SubjectScope resolves the subject's own ID as their one deletable scope,
// which is right when audit entries are scoped per user — the arrangement the
// audit package's Scope field is designed for — and is what an Eraser did by
// default before it took its resolver as an argument.
//
// It misses the chains of accounts the subject owns, which
// recordingcfg.FileBySubject files account entries on; [OwnedScopeResolver] is
// that rule's answer. requestScope is ignored, because a subject's own chain is
// theirs whichever tenant asked.
func SubjectScope(
	_ context.Context,
	_ database.SQLQueryExecutor,
	_ tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error) {
	if subject.ID == "" {
		return nil, ErrAnonymousSubject
	}

	return []tenancy.Scope{tenancy.Of(subject.ID)}, nil
}

// OwnedScopeResolver resolves the chains an erasure may delete under
// recordingcfg.FileBySubject: the subject's own, and those of the accounts
// they own.
//
// It is the FileBySubject answer and no other rule's. That rule files an entry
// naming a person on the person's chain and one naming an account on the
// account's, so the subject's chain is theirs to lose whole, and so is an
// account's whose owner they are. Under FileByWrite the chains are the scopes
// the writes ran in, which this module cannot enumerate, and a deployment
// filing that way supplies its own resolver.
//
// Ownership is the test, and membership is deliberately not: a member's
// erasure must not delete the history of an account other people still
// belong to. The account's own entries go with its chain when its owner is
// erased, which is the point — they are the account's history, and the
// account is the owner's. Whether an erasure may also reach an account the
// subject merely administers is a policy this resolver does not take a side
// on; the ownership test is the conservative one, and a deployment that wants
// more passes its own. A deployment that hands an account to another member so
// that it outlives its owner, and does so earlier in the erasure's own
// transaction, finds the account no longer the subject's here, because this
// resolver reads in that transaction.
//
// Accounts are read from tenancy.Global(), because the identity directory is
// global even where the chains it names are not. They are the accounts the
// subject belongs to, filtered to those whose OwnerUserID is the subject, and
// archived ones are included: an account archived before its owner asked to
// be forgotten still holds the owner's chain.
//
// requestScope is ignored, for the reason [SubjectScope] ignores it: a
// subject's chain is theirs whichever tenant asked.
func OwnedScopeResolver(directory identity.Store) ExecutorScopeResolver {
	return func(
		ctx context.Context,
		q database.SQLQueryExecutor,
		_ tenancy.Scope,
		subject dataprivacy.Subject,
	) ([]tenancy.Scope, error) {
		if directory == nil {
			return nil, ErrNilDirectory
		}

		if q == nil {
			return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")
		}

		if subject.ID == "" {
			return nil, ErrAnonymousSubject
		}

		accounts, err := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[identity.Account], error) {
				everything := *filter
				everything.IncludeArchived = new(true)

				return directory.ListAccountsForUser(ctx, q, tenancy.Global(), subject.ID, &everything)
			})
		if err != nil {
			return nil, platformerrors.Wrap(err, "listing the accounts the subject belongs to")
		}

		scopes := []tenancy.Scope{tenancy.Of(subject.ID)}

		for i := range accounts {
			if accounts[i].OwnerUserID == subject.ID {
				scopes = append(scopes, tenancy.Of(accounts[i].ID))
			}
		}

		return scopes, nil
	}
}
