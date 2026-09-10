/*
Package shredding provides per-subject data keys that can be destroyed, so that
erasure reaches media nothing can write to.

Deleting a row erases it from the live database and from nowhere else. With any
real retention window, "erased" means "erased from the live database, and still
present in every snapshot for the next N days". No amount of DELETE reaches a
snapshot taken yesterday — the media is not writable, and that is the entire
point of a backup.

Crypto-shredding closes that. A subject's sensitive columns are encrypted under
a key held for that subject alone. Destroying the key turns every ciphertext it
protected into noise at once: in the live database, and in every backup that has
already shipped.

# Shape

Envelope encryption, not a key per customer in a KMS. A cloud KMS charges per
key and quotas them in the low thousands, so a million subjects is neither
affordable nor permitted.

  - One root key, in the KMS, behind an encryption.KeyWrapper. It never leaves
    the KMS and it is the only thing carrying a per-key price.
  - One data key per subject: 32 bytes from crypto/rand, generated in this
    process, wrapped under the root key and stored as a row. A subject's columns
    are encrypted locally, under their own key, with AES-256-GCM.

The stored row holds the only copy of that key. Deleting the key material is
therefore not a soft delete, and nothing can reconstruct it.

The cost at a million subjects is one KMS key, one table, and whatever unwrap
volume survives the cache. The line item is calls, not keys.

# Why the keys are stored rather than derived

HKDF(root, subjectID) would give per-subject keys with no table, no cache and no
backups to think about. It also cannot shred: derivation is deterministic, so
the key always comes back.

Storage is not an implementation detail of per-subject keys. It is the
mechanism, and every operational cost below is the price of a key being a thing
that can be destroyed rather than recomputed.

# The keys table gets backed up too

This is the one way the feature inverts into its opposite, and it cannot be
fixed later by a code change.

Shred a key today, restore last week's snapshot of the table it lived in, and
the wrapped key returns along with the ability to read everything it protected.
The keys must therefore have a shorter backup retention than the data they
protect, or live outside the database that is backed up alongside that data.

NewSQLStore takes its own database.Client for exactly that reason: pointing it
at a separate database is a constructor argument rather than a fork. Nothing in
this package can verify the retention policy, and a deployment that gets it wrong
has a feature that reports success and delivers nothing.

The same applies to whatever else keeps old versions of a row. Shredding is an
UPDATE, and an UPDATE does not overwrite bytes on disk: the pre-shred tuple
survives in Postgres's WAL and old heap pages until vacuum, in MySQL's binary
log, in SQLite's WAL until a checkpoint. A DELETE would be no better. So the
claim is not that the key is scrubbed from the media at the instant of the call —
it is that the key is unrecoverable once this database's own retention has
passed, and that is why that retention has to be the shorter one.

# Assembly

The keys, and the erasure path that destroys them:

	store, err := shredding.NewSQLStore(keysClient)
	// ...
	keys, err := shredding.NewKeys(store, wrapper)
	// ...
	worker, err := dataprivacy.NewWorker(ctx, &dataprivacy.WorkerConfig{}, requestStore, registry,
	    dataprivacy.WithWorkerShredder(keys),
	)

keysClient is deliberately spelled separately from the client the erasers delete
rows through. The shred is not a substitute for those erasers: they delete rows,
and this makes whatever was encrypted per subject unreadable everywhere,
including where deletion cannot reach.

The invalidation broadcast, which is two halves and needs both:

	broadcaster, err := shredding.NewQueueBroadcaster(publisher)
	// ...
	keys, err := shredding.NewKeys(store, wrapper, shredding.WithBroadcaster(broadcaster))
	// ...
	handler, err := shredding.NewInvalidationHandler(keys)
	// ...
	consumer, err := consumers.NewConsumer(ctx, shredding.DefaultInvalidationTopic, handler)
	// ...
	go consumer.Consume(ctx, errs)

A deployment that wires the publisher and forgets the subscriber gets the worst
of both: shreds announced to nobody, erasure quietly completing on the TTL
across the fleet, and a publisher-side counter that says invalidations are being
sent. Both halves are instrumented so that the pair can be compared —
shredding_invalidations_broadcast against shredding_invalidations_received, and
shredding_invalidations_applied for the ones that reached a key that was still
cached. A received count of zero against a nonzero broadcast count is the
misconfiguration above; an applied count whose dropped=true share is zero is a
broadcast that never arrives before the TTL, which is a slower bus than the
guarantee assumes rather than a broken one.

# Cached keys outlive the shred

Unwrapping on every read is a KMS round trip per query, so plaintext data keys
are cached in this process. A cached key survives the row being destroyed, which
means erasure completes on eviction rather than on the call.

That bound is a stated guarantee rather than an accident of configuration:
WithKeyTTL sets it, DefaultKeyTTL is five minutes, and a deployment that
promises a subject "erasure completes within N minutes" is promising this
number. WithBroadcaster shortens it in the common case by announcing a shred to
the other replicas; the TTL remains the guarantee, because no bus this can be
wired to delivers to a replica that was restarting at the time.

The cache is deliberately in-process and deliberately not the cache package. A
shared cache holding plaintext data keys would put them on another host's disk
via its own persistence, where this package could neither bound nor destroy
them — a second copy of the key, with none of the properties that make the first
one shreddable.

Eviction drops the reference and nothing more. The expanded key schedule inside
crypto/aes is not reachable to overwrite, and a garbage collector may have
copied the key material anyway, so the honest bound on a cached key is the TTL
rather than memory hygiene.

# Granularity

A key belongs to a Subject, which is a type and an ID. Erasure obligations
attach to a natural person, so in a product with users inside tenants the type
that matters is the user: a per-tenant key does not satisfy one user's request,
and shredding it answers a request nobody made on behalf of everybody else in
the tenant.

The two are mechanically identical — both are rows — but they differ in the
table's cardinality and in the cache hit rate, and the choice is fixed by what
the application passes to Encrypt long before anyone asks to be erased.

# Shredding is irreversible, and says so afterwards

A shredded subject keeps a row: the key material is gone, the timestamp of its
destruction remains. That tombstone is what makes the destruction a record
rather than an absence, and it is what lets a later read report
ErrSubjectShredded instead of silently minting a fresh key and reporting that
the subject has no data.

Encrypt refuses a shredded subject for the same reason. A system still writing
about someone it was told to forget is a bug, and quietly minting them a new key
is how that bug goes unnoticed. Reviving a subject requires deleting the
tombstone by hand, which is an administrative act with a person attached to it.

# Where the SQL comes from

Every statement this package executes is generated. The schema's facts — the
table name, the columns in projection order, the natural key each statement
addresses a row by — are spelled once, in
shredding/internal/queries. `make unison` renders them through
database/querygen into the canonical .sql files beside that package, in sqlc's
spelling; sqlc compiles every one of those statements against the DDL
shredding/migrations produces, on all three dialects; and
sqlc-gen-unison emits shredding/internal/shreddingdb from them,
typed params and methods over driver placeholders, which is what the store
executes. A renamed column is a build failure with no database running, where it
used to be a scan error at run time.

Both artifacts are committed, and the duplication is deliberate — see identity's
package comment, which carries the long form of why. The short of it: the .sql
is the reviewable form of the contract, it anchors a drift gate that pins the
committed text byte for byte against the renderer, and it is exactly what the
analyzer was handed when the analyzer rejects something.

What that buys here is narrower than it is for a schema with seven tables, and
it is the part this table could least afford to get wrong. The primary key is
(subject_type, subject_id) rather than an id, and that pair is not a stylistic
preference: it is the constraint enforcing one live key per subject, which is
the difference between a shred that works and one that leaves half the
ciphertext readable. It appears in the read's predicates, in the shred's, and in
the conflict target of the insert — where Postgres matches it against a unique
index the table actually has — and it is now spelled once, in one Go slice, for
all three.

# What this is not

This is not a confidentiality feature and does not rest on the threat model
column encryption is usually sold on. An attacker who owns the application owns
the key material too, because the application decrypts on every read.

Crypto-shredding survives that attacker, because it is not defending against
one. It makes erasure reach media you can no longer write to, which nothing else
here can do.
*/
package shredding

//go:generate go run ./internal/queriesgen
