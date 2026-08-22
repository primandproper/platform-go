package shredding

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// serviceName scopes this package's spans, logger, and instrument names.
const serviceName = "shredding"

// Span and log attribute keys. Only identifiers, counts, and timestamps ever
// reach telemetry — never key material, and never a plaintext this package was
// handed.
const (
	subjectIDKey    = "shredding.subject_id"
	subjectTypeKey  = "shredding.subject_type"
	shreddedAtKey   = "shredding.shredded_at"
	destroyedKey    = "shredding.destroyed"
	cacheHitKey     = "shredding.cache_hit"
	mintedKey       = "shredding.minted"
	rowsAffectedKey = "shredding.rows_affected"
	// droppedKey says whether an invalidation reached a cached key or arrived
	// after the TTL had already taken it. Both are ordinary outcomes; the ratio
	// between them is what says whether the broadcast is doing anything.
	droppedKey = "shredding.dropped"
)

var (
	// ErrNilStore indicates a nil Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil shredding store")

	// ErrNilKeyWrapper indicates a nil encryption.KeyWrapper. There is no
	// default: wrapping with nothing would store data keys in the clear, which
	// is the one configuration in which this package's guarantee is a lie.
	ErrNilKeyWrapper = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil shredding key wrapper")

	// ErrNilDatabaseClient indicates a nil database.Client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrEmptySubjectID indicates a Subject with no ID. A key belongs to
	// somebody; there is no anonymous one.
	ErrEmptySubjectID = platformerrors.New("empty shredding subject ID")

	// ErrSubjectShredded indicates a subject whose data key has been destroyed.
	//
	// It is reported by Decrypt, where it is the feature working — the
	// ciphertext is unrecoverable and always will be — and by Encrypt, where it
	// means something is still writing about a person the system was told to
	// forget.
	ErrSubjectShredded = platformerrors.New("shredding subject's data key has been destroyed")

	// ErrNoKey indicates a subject that has never had a data key. Decrypting a
	// ciphertext for such a subject means the ciphertext and the keys table
	// disagree — usually a restore of one without the other.
	ErrNoKey = platformerrors.New("shredding subject has no data key")

	// ErrKeyMaterialMissing indicates a live row whose wrapped key is empty.
	// Nothing this package writes produces one, so it is evidence of the row
	// having been edited outside it.
	ErrKeyMaterialMissing = platformerrors.New("shredding key row holds no wrapped key")
)

type (
	// Subject names whose data a key protects.
	//
	// Identity is the pair, not the ID alone: a user and an account that happen
	// to share an identifier are two subjects with two keys, and shredding one
	// leaves the other readable. An application that sets Type on some writes
	// and not others has, by the same rule, made two subjects out of one person
	// — and will shred only one of them.
	Subject struct {
		// Type says what kind of principal this is — "user", "account", or
		// whatever an application's third kind of principal is called. It may be
		// empty, which is its own consistent namespace, but it has to be
		// consistent.
		Type string `json:"type,omitempty"`
		// ID identifies the subject within its type. Required.
		ID string `json:"id"`
	}

	// Receipt is what a destruction leaves behind.
	Receipt struct {
		// ShreddedAt is when the key material was destroyed. Shredding is
		// idempotent, so a second call reports the first call's timestamp rather
		// than the second call's.
		ShreddedAt time.Time `json:"shreddedAt"`
		// Subject is whose key it was.
		Subject Subject `json:"subject"`
		// Destroyed reports whether there was key material to destroy.
		//
		// False is a legitimate outcome, not a failure: it means the subject
		// never had a key, and the tombstone now written forecloses one. It is
		// worth recording, because "we destroyed the key" and "there was nothing
		// to destroy" are different sentences to put in front of a regulator.
		Destroyed bool `json:"destroyed"`
	}

	// Shredder destroys a subject's data key.
	//
	// This is the operation the package exists for, and it is the one that
	// cannot be undone. Every ciphertext written under that key becomes
	// permanently unreadable — in the live database, and in every backup that
	// has already shipped, which is the half nothing else here can reach.
	//
	// It is idempotent, because the caller is a job that retries.
	Shredder interface {
		Shred(ctx context.Context, subject Subject) (Receipt, error)
	}

	// Invalidator drops a subject's key from this process's cache.
	//
	// Shred already does this locally. This is the seam for the other replicas,
	// which learn about a shred from a Broadcaster rather than from the call.
	//
	// It takes a context and returns nothing, which is the right way round.
	// Deleting from an in-process map cannot fail, so there is no error to
	// report; but whether the drop found a live key is how a deployment learns
	// that the broadcast is beating the TTL, and recording that needs somewhere
	// to hang a span and a counter.
	Invalidator interface {
		Invalidate(ctx context.Context, subject Subject)
	}

	// Keys is the whole surface: encrypt and decrypt under a subject's data
	// key, destroy that key, and drop a cached copy of it on somebody else's
	// say-so.
	//
	// Encrypt and Decrypt are spelled out here rather than reusing
	// encryption.Encryptor and encryption.Decryptor, because every operation
	// names a subject and those signatures have nowhere to put one. The way to
	// make them fit is a For(ctx, subject) returning an
	// encryption.EncryptorDecryptor bound to one subject — which is a plaintext
	// data key with no expiry, held somewhere this package cannot reach when the
	// shred arrives. The TTL can only be the bound on a cached key's life if the
	// key never leaves.
	Keys interface {
		// Encrypt encrypts under the subject's data key, minting one if the
		// subject has none.
		//
		// associatedData behaves as it does throughout cryptography/encryption:
		// authenticated, not encrypted, not recoverable from the ciphertext, and
		// required to match byte for byte on the way back. Passing the row's
		// primary key and column name binds the ciphertext to where it lives.
		//
		// The subject is not part of the ciphertext. It does not need to be —
		// the row already names the subject, and the subject names the key — and
		// putting it there would write a subject identifier in the clear into
		// every encrypted column.
		Encrypt(ctx context.Context, subject Subject, plaintext, associatedData []byte) ([]byte, error)

		// Decrypt decrypts under the subject's data key, and reports
		// ErrSubjectShredded once that key is gone.
		Decrypt(ctx context.Context, subject Subject, ciphertext, associatedData []byte) ([]byte, error)

		Shredder
		Invalidator
	}

	// Broadcaster announces a shred to the replicas that did not perform it, so
	// their cached copies of the key go at the same time rather than at their
	// own expiry.
	//
	// It is an improvement on the TTL and not a replacement for it. No bus this
	// can be wired to delivers to a replica that was restarting at the time, and
	// the redis provider is at-most-once by construction, so the TTL remains the
	// bound a deployment can promise.
	Broadcaster interface {
		Broadcast(ctx context.Context, subject Subject) error
	}
)

// validate reports whether the subject names anybody.
func (s Subject) validate() error {
	if s.ID == "" {
		return ErrEmptySubjectID
	}

	return nil
}
