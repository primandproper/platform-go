package grpc

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v14/audit"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ChainsResolver answers which chains one request may read, for a deployment
// that files entries under more than one scope.
//
// # Why a deployment has more than one chain
//
// The scope is the hash chain's partition as well as the row's label, and a
// chain is a serialization point: every entry in it takes the previous entry's
// hash, so two writers appending to one chain contend on one row. A deployment
// that files every login under its tenant's scope has made that row the busiest
// in the database. The usual answer is to file an entry under its account where
// it has one and under its actor otherwise, which spreads the contention and
// leaves a signed-in person legitimately belonging to two chains: their
// account's, and their own.
//
// [ScopeResolver] answers with the one scope a connection is placed in, and
// every write and the verification walk want exactly that. A read of what
// somebody did is the one question whose honest answer spans chains.
//
// # Absent means the connection's scope, alone
//
// A server built without one reads the single chain [ScopeResolver] named,
// which is what every consumer had before this existed and remains right for a
// deployment with one chain per tenant. Supplying one is a deployment saying it
// has more.
//
// # What it must not do
//
// Return a scope the caller may not read. This is the whole of the tenancy
// check on a multi-chain read: the scopes it answers with are bound into the
// queries directly, so a resolver that returns a scope on the strength of a
// request field has handed the caller a cross-tenant read. Answer from the
// principal, the connection, or a membership the server already trusts — never
// from the request.
type ChainsResolver func(ctx context.Context) ([]tenancy.Scope, error)

// chainsFor answers the scopes a read should span, which is the resolver's
// answer or the connection's single scope.
func (s *Server) chainsFor(ctx context.Context, scope tenancy.Scope) ([]tenancy.Scope, error) {
	if s.chains == nil {
		return []tenancy.Scope{scope}, nil
	}

	resolved, err := s.chains(ctx)
	if err != nil {
		return nil, err
	}

	if len(resolved) == 0 {
		return []tenancy.Scope{scope}, nil
	}

	return resolved, nil
}

// listAcrossChains pages every chain with one cursor and merges what they
// answer into the page a single-chain read would have returned.
//
// # Why this is here rather than in the store
//
// A paged read cannot bind a set of scopes on two of the three dialects audit
// serves: SQLite numbers the bare markers a sqlc.slice expansion produces one
// past the highest it has seen, so the cursor and the page size bound after it
// collide with the set's own elements — silently, on the two arguments that
// decide which rows come back. comments.Store states the same limit and reaches
// the same answer for an operator paging several tenants.
//
// What lets this package answer where comments does not is that the set is
// derivable here. Which tenants an operator administers is a product decision
// comments cannot know; which chains a caller's own entries are in is a fact
// about the principal, which this surface already resolves.
//
// # Why the merge is correct
//
// The list is ordered and cursored by id, not by seq — the chain-ordered walk
// behind Verify is a different statement and is untouched — so one cursor is
// meaningful in every chain at once. Each chain is asked for a whole page from
// that cursor, and the merged result takes the first page-worth. A row left
// unfetched in some chain is larger than every row fetched from it, and at most
// a page is emitted, so nothing skipped could have belonged on this page. The
// next cursor is the last row emitted, and the following call asks every chain
// again from there — which is what makes a row that lost the merge this time
// arrive next time rather than never.
//
// Advancing a cursor per chain instead would drop rows, and concatenating
// rather than merging would order the page by chain. Neither announces itself,
// which is why this is one function with a test rather than a paragraph telling
// a consumer how to write it.
func (s *Server) listAcrossChains(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scopes []tenancy.Scope,
	query *audit.Query,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[audit.Entry], error) {
	if len(scopes) == 1 {
		query.Scope = pointer.To(scopes[0])

		return s.reader.List(ctx, q, query, filter)
	}

	merged := &filtering.QueryFilteredResult[audit.Entry]{}

	for _, scope := range scopes {
		// A copy per chain: Scope is the only field that differs, and the
		// reader is entitled to the same query otherwise.
		scoped := *query
		scoped.Scope = pointer.To(scope)

		page, err := s.reader.List(ctx, q, &scoped, filter)
		if err != nil {
			return nil, err
		}

		merged.Data = append(merged.Data, page.Data...)
		merged.Pagination = page.Pagination
	}

	descending := filter != nil && filter.SortBy != nil && *filter.SortBy == *filtering.SortDescending

	slices.SortFunc(merged.Data, func(a, b *audit.Entry) int {
		if descending {
			return compareIDs(b.ID, a.ID)
		}

		return compareIDs(a.ID, b.ID)
	})

	limit := int(filtering.DefaultQueryFilterLimit)
	if filter != nil && filter.MaxResponseSize != nil {
		limit = int(*filter.MaxResponseSize)
	}

	if len(merged.Data) > limit {
		merged.Data = merged.Data[:limit]
	}

	// The cursor is the last row emitted rather than the last row any chain
	// returned, so the next call resumes where this page ended and not past
	// rows this page dropped.
	merged.Cursor = ""
	if len(merged.Data) > 0 {
		merged.Cursor = merged.Data[len(merged.Data)-1].ID
	}

	return merged, nil
}

// compareIDs orders two entry identifiers the way the statement's ORDER BY
// does.
func compareIDs(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
