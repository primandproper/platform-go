package syncsource

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNilFetchFunc indicates a Source built without a way to read one row.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilFetchFunc = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync fetch func")

	// ErrNilScanFunc indicates a Source built without a way to page over IDs.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilScanFunc = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync scan func")

	// ErrNilConvertFunc indicates a Source built without a row-to-document
	// transform. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	//
	// Refused rather than defaulted to the identity, because there is no
	// identity available: the row type and the document type are different type
	// parameters precisely so that what gets indexed is a deliberate subset of
	// what the row holds.
	ErrNilConvertFunc = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync convert func")

	// ErrNilDocumentBody indicates a ConvertFunc returned nil for a row that
	// exists.
	//
	// It is not the same event as a missing row and must not be conflated with
	// one: a missing row is the source saying the document is gone, and this is
	// the transform saying nothing at all about a row that is still there.
	// Indexing it would store a null body under a live ID; omitting it would
	// quietly drop the document from a rebuild.
	ErrNilDocumentBody = platformerrors.New("search sync convert func returned no document body")
)
