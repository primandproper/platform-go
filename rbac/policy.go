package rbac

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
)

// Policy is a deployment's authorization policy declared as one value: every
// role it defines, with the permissions each grants directly and the roles each
// inherits from.
//
// Seed takes the roles; this is the declaration they came from. The difference
// shows at the call site that has one — a consumer's bootstrap, which already
// holds a PlatformPolicy() of its own and would otherwise spread it back into a
// variadic to hand it over. One value is also one thing to diff across
// releases: marshal the old and the new and the change to the policy is the
// change to the document, rather than something to be reconstructed from the
// call sites that pass pieces of it around.
type Policy struct {
	// Roles are the roles the policy defines. A role names the permissions it
	// grants directly and the roles it inherits from; inheritance is transitive
	// and resolved at read time, so a parent's later grants reach its children
	// without re-seeding them.
	Roles []authorization.Role `json:"roles,omitempty" yaml:"roles,omitempty"`
}

// Validate reports whether the policy is well-formed: every role named, no
// duplicates, every parent defined, and no inheritance cycles.
//
// SeedPolicy validates before it writes anything, so calling this first is
// never required. It is exported for the test a consumer writes over its own
// declaration, where a policy that cannot be seeded is worth failing CI rather
// than the first deploy that tries.
//
// It is authorization.ValidateRoles, which authorization/static also runs, so a
// policy this accepts is a policy either backend accepts.
func (p Policy) Validate() error {
	return authorization.ValidateRoles(p.Roles...)
}

// SeedPolicy writes a declared policy into the policy tables through the
// executor it is handed.
//
// It is the seed step a deployment runs: the declaration in, the rows it means
// out. Run it once, after the DDL, under whatever lock your migrator already
// holds — from a migrate step or from a startup hook, whichever your deployment
// already has. The package documentation shows both.
//
// This package ships no lock of its own and will not. The lock a consumer seeds
// under belongs to its migration runner, which this module does not own, so one
// shipped here would be a second lock racing the one already being held rather
// than the one being held. What that lock buys is quiet rather than
// correctness: Seed converges when several replicas run it at once, and the
// documented cost of running it unlocked is that an engine may refuse one of
// two concurrent writers — SQLite admits one at a time, MySQL's default
// isolation can declare a deadlock — on a transaction that wrote nothing, for
// the caller to retry.
//
// It is idempotent. It upserts each role by name, rewrites that role's direct
// permissions and parents, and leaves roles the policy does not name alone — so
// running it on every deploy converges on the declaration without clobbering
// roles an operator added. A re-run of an unchanged policy writes nothing at
// all.
//
// Like Seed, UpsertRole and ArchiveRole, it takes an executor rather than a
// database.Tx, which is the carve-out from this module's Tx-taking write rule
// that the package documentation records — and it carries that carve-out's
// cost: a role's grants are cleared and then rewritten across several
// statements, so the rewrite is atomic only inside Client.WithTransaction.
// Through a plain Client.Writer() a seed that fails partway has committed what
// it wrote up to that point, and a role whose rewrite did not follow its clear
// grants nothing until the next successful seed. A caller with nothing else to
// join should open a transaction anyway.
//
// A policy declaring no roles writes nothing rather than erroring, which is
// what Seed does with no roles: what "seed this policy" means for a policy with
// nothing in it is that the database already agrees with it.
func (r *Resolver) SeedPolicy(ctx context.Context, q database.SQLQueryExecutor, policy Policy) error {
	// Seed rather than a second implementation of it: one write path means the
	// idempotence, the concurrency behavior and the clear-then-rewrite
	// semantics documented above are the ones actually running, rather than a
	// copy of them that can drift. It also leaves the span named for the work,
	// since a wrapper that opened its own would contribute an empty one.
	return r.Seed(ctx, q, policy.Roles...)
}
