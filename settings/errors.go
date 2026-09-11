package settings

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings database client")

	// ErrNilDefinition indicates a nil *Definition where one was required.
	ErrNilDefinition = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil setting definition")

	// ErrNilExecutor indicates a nil executor. Every method here runs on one the
	// caller supplies — a database.Tx for a write, an executor for a read — so
	// there is no method that can fall back to a connection of the store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings query executor")

	// ErrEmptyDefinitionName indicates a definition with no name. The name is
	// the only handle a value-side call takes, so a definition without one is
	// unreachable rather than merely unlabeled.
	ErrEmptyDefinitionName = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty setting name")

	// ErrEmptySubjectType indicates a Subject with no type.
	ErrEmptySubjectType = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty settings subject type")

	// ErrEmptySubjectID indicates a Subject with no id.
	ErrEmptySubjectID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty settings subject id")

	// ErrEmptyEnumerationValue indicates an enumeration carrying the empty
	// string. It is refused rather than stored because an enumerated setting
	// whose legal values include "" cannot be told apart from one whose caller
	// left a slot blank, and the enumeration is the thing every write is checked
	// against.
	ErrEmptyEnumerationValue = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty enumeration value")

	// ErrDuplicateEnumerationValue indicates an enumeration naming one value
	// twice. The schema stores an enumeration as a set keyed on the value, so a
	// duplicate is a write that would silently collapse rather than a harmless
	// repetition.
	ErrDuplicateEnumerationValue = platformerrors.New("enumeration names a value twice")

	// ErrDefinitionNotFound indicates no live definition by that name or id in
	// this scope. Every value-side call can return it, because a value is only
	// meaningful against a definition.
	ErrDefinitionNotFound = platformerrors.New("setting definition not found")

	// ErrValueNotFound indicates the subject has not set this setting. It is
	// what GetValue and ClearValue report; Resolve does not, because a subject
	// that has not answered is a resolution rather than an absence — see
	// [SourceUnset].
	ErrValueNotFound = platformerrors.New("setting value not found")

	// ErrDefinitionNameTaken indicates a setting name already defined in this
	// scope.
	//
	// It is a distinct error rather than a raw constraint violation because the
	// difference between "your input collides" and "the database is unwell"
	// decides whether the caller reports to a person or retries. The uniqueness
	// covers archived definitions, so a name freed by archiving is a name that
	// stays taken — see settings/migrations.
	ErrDefinitionNameTaken = platformerrors.New("setting name is already defined")

	// ErrUnknownKind indicates a Kind this package cannot parse.
	ErrUnknownKind = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "unknown setting kind")

	// ErrMalformedValue indicates a value that is not of its definition's kind:
	// "yes" for a boolean, "1.5" for an integer.
	ErrMalformedValue = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "value is not of the setting's kind")

	// ErrNotEnumerated indicates a value outside the definition's enumeration.
	ErrNotEnumerated = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "value is not one the setting admits")

	// ErrKindMismatch indicates a typed read of the wrong kind: Resolution.Bool
	// on a setting whose kind is integer.
	//
	// It is an error rather than a coerced answer because it is a mistake in the
	// calling code, and every coercion available is worse: false is a decision
	// the caller did not make, and reporting nothing is how a mistyped read gets
	// deployed.
	ErrKindMismatch = platformerrors.New("setting is not of the kind it was read as")

	// ErrSettingUnset indicates a resolution with neither a value nor a default.
	//
	// It is the third state of a resolved setting, and it is a sentinel rather
	// than a bool parameter on the accessors for the reason [Resolution]
	// describes: a getter taking a fallback answers "unset" with whatever the
	// caller guessed and gives them no way to tell that is what happened.
	ErrSettingUnset = platformerrors.New("setting has no value and no default")

	// ErrCursorStalled indicates a paged read that answered with the cursor it
	// was handed, which would leave a walk over the collection repeating one
	// page forever.
	//
	// It surfaces from the two reads here that walk a collection rather than
	// answer a page — resolving every setting for a subject, and checking every
	// stored value against a definition being edited — and it is an error rather
	// than a stop, for the reason dataprivacy's namesake is: the rows past the
	// stall are the ones the caller asked about, and a check that skipped them
	// would approve an edit that strands values while reporting success.
	ErrCursorStalled = platformerrors.New("settings paged read did not advance")

	// ErrStrandedValues indicates an edit to a definition that some stored value
	// no longer satisfies: a kind that value does not parse as, or an
	// enumeration it is not in.
	//
	// The write is refused rather than applied, which is the whole of what this
	// store owns that a hand-rolled pair does not. Applied, the stored value
	// would still be there and every read of it would fail — a setting that
	// works for most subjects and is broken for the ones who chose the value
	// somebody just made illegal. The wrapped message names the subject and the
	// value, because clearing or migrating them is what the administrator has to
	// do before the edit can succeed.
	ErrStrandedValues = platformerrors.New("edit would strand stored setting values")
)
