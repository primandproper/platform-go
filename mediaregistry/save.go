package mediaregistry

import (
	"context"
	"io"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
)

// StoreAndRecord writes the bytes and then registers what was written through
// the caller's transaction, and answers with the row that was registered — Size
// included, counted from what actually went past.
//
// It is the convenience, not the contract. Storing and registering stay
// separately callable, and they have to: bytes that arrived through a signed URL
// were stored by the client rather than by this process, and a consumer
// migrating an existing bucket registers objects nothing here ever wrote. So
// this is a free function over the two seams rather than a method on either, and
// nothing in the Store or the UploadManager knows it exists.
//
// The transaction and the scope come first, ahead of the two seams, because they
// are the write's own arguments and this is the module's write shape with the
// receiver spelled out as a parameter: everything a Store write takes, it takes
// here in the same order.
//
// Size comes from the copy rather than from the caller, because the caller's
// number is a claim. A Content-Length header is whatever the client sent, and a
// quota, a bill, or a storage report read off claimed sizes is one that does not
// hold. Counting the bytes as they go past is the only number that is about what
// is in the bucket. It is written onto a copy of the object, not onto the
// caller's, which is [Store.RecordObject]'s rule applied one level up: what this
// call settled is on the row it hands back.
//
// in.Key must be set — it is where the bytes go — and in.ContentType, if set, is
// what the object is stored with, so the row and the stored object agree about
// the type. Everything else is the caller's: the owner, the subject it hangs
// off. The scope is the argument's, and an [ObjectInput] carries none of its own
// to disagree with it.
//
// The order is deliberate and it is the one that fails safe. The bytes go first,
// so a failure to register leaves an object with no row — invisible to every
// read, and exactly what an orphan sweep is later written to find. Registering
// first would leave a row pointing at bytes that are not there, which every read
// reports as an object the caller may have and every fetch then fails to
// deliver. Neither is free; only one of them lies to a reader. A transaction the
// caller later rolls back has the same outcome as a failed registration, for the
// same reason: the bytes are already spent.
//
// That argument holds for everything after the upload and says nothing about the
// upload itself, which is why the bucket is asked first. A provider's writer at
// an occupied path replaces what is there, so an upload that runs before the
// collision is known has already overwritten somebody's object — and the
// registration that then refuses it leaves the victim's row, unchanged and still
// theirs, serving the colliding caller's bytes through whatever guarded route
// serves them. Rolling the transaction back does not bring the bytes it
// displaced back either. [ErrObjectKeyOccupied] is that refusal, and it is the
// bucket's answer rather than the registry's: a key another tenant holds is free
// as far as every row here is concerned, so the registry's own check would clear
// exactly the overwrite that costs somebody the most.
//
// Like the registry's collision check, asking narrows the window rather than
// closing it — two callers racing for one key can both be told it is free, and
// the providers expose no conditional write to settle it. What settles that race
// is the unique index, one step later, and the loser has by then written bytes
// nothing points at: the orphan this order has always been willing to leave.
//
// What the transaction costs here is worth stating, because this function is the
// one place it is paid without the caller writing the upload themselves. The
// transaction is open across the upload, so a large object holds a connection
// and a snapshot for as long as the bytes take. A caller who cannot afford that
// does what this function is a convenience for: uploads.UploadManager.Save
// outside the transaction, then RecordObject inside a short one, which is the
// same two calls in the same order with the boundary drawn tighter.
func StoreAndRecord(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	manager uploads.UploadManager,
	store Store,
	in ObjectInput, //nolint:gocritic // hugeParam: by value on purpose — see ObjectInput, and the call does a round trip
	r io.Reader,
	opts ...uploads.SaveOption,
) (*Object, error) {
	switch {
	case tx == nil:
		return nil, ErrNilExecutor
	case manager == nil:
		return nil, ErrNilUploadManager
	case store == nil:
		return nil, ErrNilStore
	case r == nil:
		return nil, ErrNilReader
	}

	// The scope is checked before the bytes go, not left to the registration
	// that would refuse it afterwards. A write that could never have been filed
	// should not spend an upload first, and this is the one check that can be
	// made without either seam.
	if err := scope.Validate(); err != nil {
		return nil, err
	}

	// The bucket is asked before the bytes go, because an upload at an occupied
	// path replaces what is there and no ordering after that point can undo it.
	// A bucket that would not answer is not a bucket that said the key was free,
	// so the error refuses the upload too — and it is wrapped, where the upload's
	// own error below is passed through, because the caller asked for an upload
	// and did not ask for this read.
	occupied, err := manager.Exists(ctx, in.Key)
	switch {
	case err != nil:
		return nil, platformerrors.Wrap(err, "checking whether the object key is free")
	case occupied:
		return nil, ErrObjectKeyOccupied
	}

	// The content type is stated to the provider as well as recorded, so the
	// stored object and its row agree. An empty one is left alone: the providers
	// sniff it from the content, and naming it explicitly as "" would replace a
	// sniffed answer with none.
	if in.ContentType != "" {
		opts = append(opts, uploads.WithContentType(in.ContentType))
	}

	counted := &countingReader{r: r}

	if err = manager.Save(ctx, in.Key, counted, opts...); err != nil {
		return nil, err
	}

	// Assigned onto the parameter, which is this function's own copy: the input
	// is taken by value, so there is no version of this that reaches back into
	// what the caller still holds. What the call settled is on the row returned.
	in.Size = counted.n

	return store.RecordObject(ctx, tx, scope, in)
}

// countingReader counts the bytes that pass through it.
//
// It wraps the caller's reader rather than tee-ing into a buffer, so an upload
// of any size costs one int64. It is not safe for concurrent use, and does not
// need to be: an UploadManager.Save reads its reader from one goroutine.
type countingReader struct {
	r io.Reader
	n int64
}

var _ io.Reader = (*countingReader)(nil)

func (c *countingReader) Read(p []byte) (int, error) {
	read, err := c.r.Read(p)
	c.n += int64(read)

	return read, err
}
