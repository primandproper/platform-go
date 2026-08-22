package batching

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNilWriteFunc indicates a nil write function was passed to NewGroupCommit
	// or NewBuffer. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	//
	// There is no default. A batcher with nowhere to write is a batcher that
	// accepts everything and loses it, and every caller of Submit would be told
	// its items had landed.
	ErrNilWriteFunc = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil batch write function")

	// ErrClosed indicates a Submit that arrived after Close. It is returned
	// rather than parking the caller on a batch nothing will ever flush.
	ErrClosed = platformerrors.New("batcher is closed")

	// ErrItemTypeMismatch indicates WithMerge or WithOrder was given functions
	// for a type other than the batcher's. Option carries no type parameter, so
	// the compiler cannot catch this; the constructors report it instead.
	//
	// It is reported rather than ignored because the option that fails to apply
	// is the one carrying the lock ordering: a batcher that silently dropped it
	// would write in map order and deadlock under exactly the load it was added
	// to survive.
	ErrItemTypeMismatch = platformerrors.New("function item type does not match batcher type")
)
