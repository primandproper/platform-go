package encryption

import (
	"context"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const keyringName = "encryption_keyring"

// keyIDAttribute labels the rotation metrics. It is the whole reason key IDs
// are worth carrying: "how many reads are still landing on the key we retired"
// is a countable backlog rather than a hope, and it is the signal that says
// whether a rotation has finished.
const keyIDAttribute = "encryption.key.id"

// MaxKeyIDLength bounds a KeyID.
//
// The limit comes from the frame: a ciphertext stores its key ID length in one
// byte, because the alternative is a variable-length integer in a format that
// has to be parsed correctly by every future version of this package. 255 is
// far past any sane ID, and an ID approaching it is a sign that something
// other than an identifier is being stored.
const MaxKeyIDLength = 255

type (
	// RingKey is one key's identity paired with the Cipher that uses it.
	RingKey struct {
		// Cipher performs the actual encryption under this key.
		Cipher Cipher
		// ID names the key, and is written into every ciphertext the Cipher
		// produces through the ring.
		ID KeyID
	}

	// ringEntry is a key's Cipher alongside everything derived from its ID.
	//
	// Both derived values are built once per key rather than per operation. A
	// key ID never changes after construction, so building the attribute set
	// inline cost more allocations than the encryption it was measuring, and
	// re-encoding the frame header on every Encrypt did the same.
	//
	// Precomputing the header also removes an error return from Encrypt. A
	// header can only fail to encode for an ID that is empty or over-long, and
	// NewKeyring rejects both — so the branch was unreachable and existed only
	// to be forwarded.
	ringEntry struct {
		cipher Cipher
		attrs  metric.MeasurementOption
		header []byte
	}

	// Keyring is an EncryptorDecryptor over several keys at once: it encrypts
	// with the current one and decrypts with whichever key a ciphertext names,
	// which is what makes rotation something other than a flag day.
	//
	// Rotation is deliberately lazy and the ring does not perform it. Naming a
	// new current key means new writes use it and old ciphertexts keep opening
	// under the keys they name; moving the old rows over is a re-encrypt on
	// next write plus a sweep for rows that are never written again. A ring
	// that re-encrypted eagerly would turn a configuration change into an
	// unbounded write amplification against the database.
	//
	// Retiring a key is therefore the dangerous operation, not adding one.
	// Drop a key from the ring while ciphertexts still name it and those rows
	// stop being readable — permanently, if the material is gone. The
	// decryption metrics exist to make that backlog visible before it becomes
	// that.
	Keyring struct {
		o11y            observability.Observer
		encryptCounter  metrics.Int64Counter
		decryptCounter  metrics.Int64Counter
		unknownKeyCount metrics.Int64Counter
		byID            map[KeyID]ringEntry
		currentID       KeyID
		current         ringEntry
	}
)

var _ EncryptorDecryptor = (*Keyring)(nil)

// NewKeyring builds a Keyring that encrypts under current and decrypts under
// any key in keys.
//
// current has to name one of keys. There is no default and no "first one
// wins": which key new data is written under is the single most consequential
// thing about this object, and inferring it from ordering would make a
// reordered config file silently change what encrypts production.
func NewKeyring(current KeyID, ringKeys []RingKey, opts ...Option) (*Keyring, error) {
	if len(ringKeys) == 0 {
		return nil, ErrEmptyKeyring
	}

	o := newOptions(opts)

	byID := make(map[KeyID]ringEntry, len(ringKeys))

	for i := range ringKeys {
		k := &ringKeys[i]

		if k.Cipher == nil {
			return nil, errors.Wrapf(ErrNilCipher, "key %q", k.ID)
		}

		if _, seen := byID[k.ID]; seen {
			return nil, errors.Wrapf(ErrDuplicateKeyID, "key %q", k.ID)
		}

		// encodeHeader is the only place that decides what makes a key ID
		// usable — empty and over-long are both its calls to make. Repeating
		// those checks here would be two definitions of one constraint, free
		// to drift, with the copy that runs first winning silently.
		header, err := encodeHeader(k.ID)
		if err != nil {
			return nil, err
		}

		byID[k.ID] = ringEntry{
			cipher: k.Cipher,
			attrs:  metric.WithAttributes(attribute.String(keyIDAttribute, string(k.ID))),
			header: header,
		}
	}

	currentEntry, ok := byID[current]
	if !ok {
		return nil, errors.Wrapf(ErrNoCurrentKey, "%q is not among the ring's keys", current)
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	encryptCounter, err := mp.NewInt64Counter(keyringName + "_encryptions")
	if err != nil {
		return nil, errors.Wrap(err, "creating encryption counter")
	}

	decryptCounter, err := mp.NewInt64Counter(keyringName + "_decryptions")
	if err != nil {
		return nil, errors.Wrap(err, "creating decryption counter")
	}

	unknownKeyCount, err := mp.NewInt64Counter(keyringName + "_unknown_key_ids")
	if err != nil {
		return nil, errors.Wrap(err, "creating unknown key counter")
	}

	return &Keyring{
		o11y:            observability.NewObserver(keyringName, o.logger, o.tracerProvider),
		byID:            byID,
		current:         currentEntry,
		currentID:       current,
		encryptCounter:  encryptCounter,
		decryptCounter:  decryptCounter,
		unknownKeyCount: unknownKeyCount,
	}, nil
}

// CurrentKeyID reports the key new ciphertexts are written under. A sweep that
// re-encrypts stale rows needs it to know what "stale" means.
func (r *Keyring) CurrentKeyID() KeyID {
	return r.currentID
}

// KeyIDs reports every key the ring can decrypt with, current included, in no
// particular order.
func (r *Keyring) KeyIDs() []KeyID {
	ids := make([]KeyID, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}

	return ids
}

func (r *Keyring) Encrypt(ctx context.Context, plaintext, associatedData []byte) ([]byte, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(plaintext)))
	defer op.End()

	op.Set(keyIDAttribute, string(r.currentID))

	header := r.current.header

	// The header is authenticated, not just prepended. Without this an
	// attacker could rewrite the key ID on a stored ciphertext and steer
	// decryption at a different key; the frame would still parse and the only
	// thing standing in the way would be that the wrong key happens to fail.
	sealed, err := r.current.cipher.Seal(ctx, plaintext, bindHeader(header, associatedData))
	if err != nil {
		return nil, op.Error(err, "sealing plaintext")
	}

	r.encryptCounter.Add(ctx, 1, r.current.attrs)

	// Assembled into a fresh slice rather than appended onto the shared
	// header, which is now owned by the ring and reused by every call.
	out := make([]byte, 0, len(header)+len(sealed))
	out = append(out, header...)

	return append(out, sealed...), nil
}

func (r *Keyring) Decrypt(ctx context.Context, ciphertext, associatedData []byte) ([]byte, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(ciphertext)))
	defer op.End()

	keyID, header, body, err := decodeHeader(ciphertext)
	if err != nil {
		return nil, op.Error(err, "decoding ciphertext header")
	}

	op.Set(keyIDAttribute, string(keyID))

	entry, ok := r.byID[keyID]
	if !ok {
		// Built inline rather than precomputed: an unknown key ID is by
		// definition not one of the ring's, and it is an error path.
		r.unknownKeyCount.Add(ctx, 1, metric.WithAttributes(attribute.String(keyIDAttribute, string(keyID))))

		return nil, op.Error(errors.Wrapf(ErrUnknownKeyID, "key %q", keyID), "resolving ciphertext key")
	}

	plaintext, err := entry.cipher.Open(ctx, body, bindHeader(header, associatedData))
	if err != nil {
		return nil, op.Error(err, "opening ciphertext")
	}

	// Counted per key rather than in aggregate, so a rotation's tail is
	// visible: decryptions still attributed to a retired key are exactly the
	// rows a sweep has not reached yet.
	r.decryptCounter.Add(ctx, 1, entry.attrs)

	return plaintext, nil
}
