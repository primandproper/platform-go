package querygen

import (
	"slices"
	"sync"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ErrNilRegistry indicates WithRegistry was handed no registry. Registering
// nowhere is the failure this registry exists to prevent, so it is rejected
// rather than treated as an absent option.
var ErrNilRegistry = platformerrors.New("registry is nil")

// Registry is the set of table names a generator knows about.
//
// It answers one question, and the question is not about SQL: which tables does
// this application have? A consumer needs that list for the things that are
// per-table but not per-query — the TRUNCATE an integration suite runs between
// tests, a schema inventory, a migration audit — and the list has to be complete
// or the symptom is not a failure where it was made. A table missing from a
// maintenance TRUNCATE is a test somewhere else failing later because the
// previous test's rows are still there.
//
// The reason it lives here rather than in the consumer is that a table's SQL and
// a table's existence are separate facts, and a consumer that derives the second
// from the first loses a table the moment something else starts producing its
// SQL. That is not hypothetical: a query builder per table doubles as a table
// list right up until one table stops needing a builder, and then the list is
// short by one with nothing to say so. Registering the name is what survives the
// thing that emitted the queries going away.
//
// So [Generator.StandardCRUD] registers every table it emits for, and anything
// else that owns a table registers it too — by hand, from a declaration, from
// whatever produces its SQL — into the same registry. Two sources, one list, and
// no rule anybody has to remember.
//
// A Registry is safe for concurrent use. Registering the same table twice is
// how the ordinary case works rather than a mistake to guard against: a
// generator emitting for more than one dialect calls StandardCRUD once per
// dialect for each table, so the second and third registrations are the same
// name arriving again.
type Registry struct {
	tables map[string]struct{}
	mu     sync.Mutex
}

// NewRegistry returns an empty Registry.
//
// Most callers want the package-level [RegisterTable] and [RegisteredTables]
// instead, because one list is the whole point — a registry a consumer has to be
// handed is a registry a caller can fail to be handed. This is for a binary
// generating for genuinely separate schemas, and for tests.
func NewRegistry() *Registry {
	return &Registry{tables: map[string]struct{}{}}
}

// Register adds tables to r, ignoring names it already holds.
//
// It panics on a name that is not a valid identifier, in the manner of the rest
// of this package: the argument is a string literal in a generator binary, so an
// invalid name is a typo a build should stop for. It matters more here than it
// looks, because a registered name is not merely stored — a consumer reads this
// list back and interpolates every entry into statement text, which is a place
// arbitrary strings do not belong. The panic value is an error wrapping
// dialect.ErrInvalidIdentifier.
func (r *Registry) Register(tables ...string) {
	for _, table := range tables {
		mustIdentifier("table name", table)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tables == nil {
		r.tables = make(map[string]struct{}, len(tables))
	}

	for _, table := range tables {
		r.tables[table] = struct{}{}
	}
}

// Has reports whether table is registered.
func (r *Registry) Has(table string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.tables[table]

	return ok
}

// Tables returns the registered table names, sorted, as a copy.
//
// Sorted rather than in registration order, because registration order is not a
// fact about the application: it is the order the generator's calls happen to
// appear in, and a generator emitting for three dialects interleaves three
// passes over the same tables. A consumer that writes this list into a file — or
// asserts the committed one still matches, which is how a generator is usually
// checked in CI — wants the list to move when the schema does and not when
// somebody reorders a switch statement.
//
// It is deliberately not an ordering a caller can delete in. Foreign keys make
// deletion order a fact about the schema, which a set of names cannot express,
// so a consumer truncating these tables wants the dialect's own way of ignoring
// the constraints — TRUNCATE ... CASCADE, a disabled FK check — rather than a
// sequence inferred from this slice.
func (r *Registry) Tables() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	tables := make([]string, 0, len(r.tables))
	for table := range r.tables {
		tables = append(tables, table)
	}

	slices.Sort(tables)

	return tables
}

// defaultRegistry is where StandardCRUD registers unless WithRegistry says
// otherwise, and what the package-level functions below read and write.
//
// It is unexported and reached through functions rather than being an exported
// variable, so that the one list every source feeds cannot be replaced by one
// source out from under the others.
var defaultRegistry = NewRegistry()

// RegisterTable adds tables to the package-level registry [RegisteredTables]
// reads, for a table whose SQL something other than [Generator.StandardCRUD]
// produces — a hand-written store, a resource declaration, a migration that
// carries a table nothing generates queries for.
//
// StandardCRUD calls it for every table it emits, so a generator that emits for
// all of its tables needs no calls of its own. The point of the function is the
// tables it does not emit for: they land in the same list, so a consumer reading
// that list back does not have to know which tables came from where.
//
// It panics on a name that is not a valid identifier — see [Registry.Register].
func RegisterTable(tables ...string) {
	defaultRegistry.Register(tables...)
}

// RegisteredTables returns the sorted contents of the package-level registry.
//
// It is a snapshot: a table registered afterwards is not in the slice already
// returned. A generator binary should read it once every source has run, which
// in practice means after the emitting is done rather than beside it.
func RegisteredTables() []string {
	return defaultRegistry.Tables()
}

// TableRegistered reports whether table is in the package-level registry.
func TableRegistered(table string) bool {
	return defaultRegistry.Has(table)
}

// WithRegistry registers the table in r rather than in the package-level
// registry.
//
// The default is the point — one list, whatever produced a given table's SQL —
// so this is for the two cases where one list is wrong: a binary generating for
// schemas that are genuinely separate databases, and a test that wants to assert
// on exactly what it registered.
//
// It panics on a nil registry, wrapping [ErrNilRegistry], rather than reading it
// as "do not register". Dropping a table quietly out of the list is the failure
// this whole mechanism exists to prevent, so it is not something an option can
// ask for.
func WithRegistry(r *Registry) Option {
	if r == nil {
		panic(platformerrors.Wrap(ErrNilRegistry, "querygen"))
	}

	return func(s *settings) {
		s.registry = r
	}
}
