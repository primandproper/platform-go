package privacy

import (
	"context"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

var (
	// ErrNilDirectory indicates a nil identity.Store handed to
	// [MembershipScopeResolver].
	ErrNilDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit privacy identity directory")

	// ErrAnonymousSubject indicates a subject with no ID handed to a resolver
	// that files by it. tenancy.Of("") is the global scope, and a resolver that
	// answered with it would hand the collector the platform's own chain as
	// though it were the subject's.
	ErrAnonymousSubject = platformerrors.Wrap(platformerrors.ErrInvalidIDProvided, "audit privacy subject names no ID")
)

// ExecutorScopeResolver is a [ScopeResolver] that reads, and takes the
// executor its reads run on when it is called rather than when it is built. Its
// On method binds it to one executor, which is the shape [NewCollector] takes:
//
//	privacy.NewCollector(log, q, privacy.MembershipScopeResolver(directory, log).On(q))
//
// It is [dataprivacy.ExecutorScopeResolver] under this package's name, for the
// reason [ScopeResolver] is an alias too.
type ExecutorScopeResolver = dataprivacy.ExecutorScopeResolver

// MembershipScopeResolver resolves a subject's collectable chains under
// recordingcfg.FileBySubject: their own, every account they belong to, and
// every chain holding an entry they acted in.
//
// It is the FileBySubject answer and no other rule's. That rule files an entry
// naming a person on the person's chain, one naming an account on the
// account's, and one naming nobody on the write's scope, so a person's history
// is on their chain, on the chains of the accounts they belong to, and on
// whatever chain an entry they acted in was filed to. Under FileByWrite the
// entries are on the scopes the writes ran in, which this module cannot
// enumerate, and a deployment filing that way supplies its own resolver.
//
// The third set is the one a reimplementation misses, and it is what makes an
// export complete: an administrator who changed a colleague's email acted in
// an entry filed on the colleague's chain, and that chain is neither theirs nor
// an account's. It is read with audit.Reader.ListAcrossScopes, paged to its end
// — a resolver that read one page would export the chains on one page. The
// scope is not a narrowing the reader can apply, so this is the read across
// every tenant, confined by the actor column matching the subject's own ID;
// it is subject-access machinery, the predicate Collect itself reads by.
//
// Accounts are listed from tenancy.Global(), because the identity directory is
// global even where the chains it names are not, and archived ones are
// included: an account archived after the subject acted in it still holds
// their entries. An account's own entries on its chain — the ones naming the
// account rather than the subject — are not exported by including the chain:
// the collector reads each scope by the subject's ID as actor and as resource,
// so the chain contributes only what names them.
//
// requestScope is ignored. Under FileBySubject a person's chain is theirs
// whichever tenant asked, and the accounts they belong to are read from the
// directory rather than from the request.
//
// Scopes come back once each, the subject's own first, then accounts in the
// directory's order, then the chains they acted in that neither named.
func MembershipScopeResolver(directory identity.Store, log audit.Reader) ExecutorScopeResolver {
	return func(
		ctx context.Context,
		q database.SQLQueryExecutor,
		_ tenancy.Scope,
		subject dataprivacy.Subject,
	) ([]tenancy.Scope, error) {
		if directory == nil {
			return nil, ErrNilDirectory
		}

		if log == nil {
			return nil, ErrNilReader
		}

		if q == nil {
			return nil, ErrNilExecutor
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

		acted, err := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
				return log.ListAcrossScopes(ctx, q, &audit.Query{ActorID: subject.ID}, filter)
			})
		if err != nil {
			return nil, platformerrors.Wrap(err, "reading the chains the subject acted in")
		}

		scopes := newScopeSet(tenancy.Of(subject.ID))
		for i := range accounts {
			scopes.add(tenancy.Of(accounts[i].ID))
		}

		for i := range acted {
			scopes.add(acted[i].Scope)
		}

		return scopes.ordered, nil
	}
}

// scopeSet collects scopes once each, in the order they were first added.
// [dataprivacy.ForEachOwner] does not de-duplicate, deliberately, so a scope
// named by both an account and an entry has to collapse here.
type scopeSet struct {
	seen    map[tenancy.Scope]bool
	ordered []tenancy.Scope
}

func newScopeSet(first tenancy.Scope) *scopeSet {
	s := &scopeSet{seen: map[tenancy.Scope]bool{}}
	s.add(first)

	return s
}

func (s *scopeSet) add(scope tenancy.Scope) {
	if s.seen[scope] {
		return
	}

	s.seen[scope] = true
	s.ordered = append(s.ordered, scope)
}
