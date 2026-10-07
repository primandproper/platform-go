package devices

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The construction and argument refusals. Each wraps a platform sentinel, so the
// platform mappers answer them and this package declares no mappers of its own.
// None of them is a refusal a person signing in could cause: every argument the
// hook passes comes from a sign-in signin already completed.
var (
	// ErrNilConfig indicates NewSQLStore was called without a config.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device store config")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the sign-in device store")

	// ErrNilStore indicates NewHooks or NewAnnotator was called without a store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device store")

	// ErrNilExtractor indicates NewHooks was called without an extractor.
	//
	// It is refused rather than defaulted to PeerExtractor, because which parts
	// of a request to believe is the one decision this package leaves to the
	// deployment, and a default would make it without anybody having made it.
	// A deployment that wants PeerExtractor passes it by name.
	ErrNilExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device extractor")

	// ErrNilExecutor indicates NewAnnotator was called without the executor its
	// reads run on.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor for the sign-in device annotator")

	// ErrNilSighting indicates Record was called with nothing to record.
	ErrNilSighting = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device sighting")

	// ErrEmptyFamilyID indicates a sighting naming no login.
	ErrEmptyFamilyID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty family ID for a sign-in device")

	// ErrEmptyUserID indicates an operation naming nobody.
	ErrEmptyUserID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty user ID for a sign-in device")

	// ErrZeroExpiry indicates a sighting with no deadline. A row with none would
	// be one the sweep reaches immediately, so it is refused rather than written
	// and collected.
	ErrZeroExpiry = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "zero expiry for a sign-in device")
)
