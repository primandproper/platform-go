package shredding

import (
	"context"
	"time"
)

// Record is one subject's row in the keys table.
//
// It is the only copy of the data key. There is no second store, no derivation
// that reproduces it, and no cache that outlives it by more than the TTL — which
// is the entire point, and also the reason this table's backup schedule is a
// policy decision rather than an operational detail. See the package
// documentation.
type Record struct {
	// CreatedAt is when the key was minted.
	CreatedAt time.Time `json:"createdAt"`
	// ShreddedAt is when the key material was destroyed, or nil while it still
	// exists. A row with it set is a tombstone: the destruction is the record,
	// and it is what lets a later read say "destroyed" rather than "no such
	// subject".
	ShreddedAt *time.Time `json:"shreddedAt,omitempty"`
	// Subject is whose key it is.
	Subject Subject `json:"subject"`
	// Wrapped is the data key encrypted under the root key. Empty on a
	// tombstone.
	Wrapped []byte `json:"-"`
}

// Shredded reports whether this record's key material has been destroyed.
func (r *Record) Shredded() bool {
	return r != nil && r.ShreddedAt != nil
}

// Store persists wrapped data keys.
//
// It is a separate seam from Keys because where the keys live is the decision
// this feature most depends on getting right: a keys table backed up alongside
// the data it protects hands back everything a shred destroyed the moment
// anybody restores a snapshot. A Store implementation pointed at a different
// database — with its own, shorter retention — is how a deployment says that
// out loud.
//
// # No executor, on any of the three
//
// Every other store in this module takes the caller's database.Tx on a write
// and a database.SQLQueryExecutor on a read, so that a consumer's row and its
// audit entry and its outbox event commit as the one fact they are. Nothing
// here takes either, and that is a ruling rather than an oversight.
//
// It follows from the paragraph above. A store that may not be on the caller's
// database cannot take the caller's transaction: handed one, it would either
// have to ignore it — a signature promising an atomicity it does not deliver —
// or run against a connection to somewhere else, which is not a thing a
// transaction can do. The seam exists so that a deployment can point the keys
// somewhere with a shorter retention, and the price of that seam is exactly
// this. See shredding/doc.go, "The keys table gets backed up too", for what
// goes wrong when a deployment declines to pay it.
//
// So the coupling this omission gives up is one the feature could not have had
// anyway, and the coupling it keeps is the one that matters: a shred is
// idempotent and a mint is a conditional insert, so a caller whose own
// transaction rolls back after either has a key row it can safely mint against
// or shred again. Neither operation needs to be undone; that is why they are
// shaped the way they are.
//
// # No scope either
//
// A data key belongs to a Subject, and a Subject is a natural person rather
// than a tenant — see shredding/doc.go, "Granularity", for why the type that
// matters is the user and not the account they happen to sit inside. An
// erasure obligation attaches to that person wherever they appear, so a scoped
// shred would destroy the key for one tenant's ciphertext and leave the rest
// readable, which is the failure mode the whole feature exists to close. The
// subject is the address space, and it is global by construction.
type Store interface {
	// Load reads a subject's record, tombstone included. It reports ErrNoKey
	// when the subject has no row at all, which is distinct from a row whose key
	// has been destroyed.
	//
	// It takes no executor: this store may not be on the caller's database, so
	// there is no transaction of theirs it could read inside, and it answers
	// from wherever the keys actually live.
	Load(ctx context.Context, subject Subject) (*Record, error)

	// Insert stores a newly minted record, and reports whether the insert won.
	//
	// A false return is not an error. It means another replica minted a key for
	// this subject first, or the subject has been shredded, and the caller must
	// read the row rather than use the key it just generated — two live keys for
	// one subject is a shred that only destroys half the ciphertext.
	//
	// It takes no executor for Load's reason, and the conditional insert is what
	// makes that safe: the race this loses to is another replica rather than
	// another statement of the caller's, and a caller whose own transaction
	// rolls back afterwards has a key nobody used rather than a key nobody can
	// find.
	Insert(ctx context.Context, record *Record) (bool, error)

	// Shred destroys a subject's key material and stamps the destruction, and
	// writes a tombstone if the subject had no row.
	//
	// It is idempotent: a subject already shredded reports the original
	// timestamp and Destroyed false, because the destruction happened once and
	// the second caller did not do it.
	//
	// It takes no executor, and here the omission is load-bearing rather than
	// merely tolerable. Destroying a key is not undoable, so it must not be
	// inside anything that can roll back — a shred that committed with the
	// erasure it was part of, and then rolled back with it, would be an erasure
	// the caller believes was reversed and a ciphertext that is noise forever.
	// It commits on its own, before the rows its caller is deleting, which is
	// the order that leaves the recoverable half last.
	Shred(ctx context.Context, subject Subject, at time.Time) (Receipt, error)
}
