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
type Store interface {
	// Load reads a subject's record, tombstone included. It reports ErrNoKey
	// when the subject has no row at all, which is distinct from a row whose key
	// has been destroyed.
	Load(ctx context.Context, subject Subject) (*Record, error)

	// Insert stores a newly minted record, and reports whether the insert won.
	//
	// A false return is not an error. It means another replica minted a key for
	// this subject first, or the subject has been shredded, and the caller must
	// read the row rather than use the key it just generated — two live keys for
	// one subject is a shred that only destroys half the ciphertext.
	Insert(ctx context.Context, record *Record) (bool, error)

	// Shred destroys a subject's key material and stamps the destruction, and
	// writes a tombstone if the subject had no row.
	//
	// It is idempotent: a subject already shredded reports the original
	// timestamp and Destroyed false, because the destruction happened once and
	// the second caller did not do it.
	Shred(ctx context.Context, subject Subject, at time.Time) (Receipt, error)
}
