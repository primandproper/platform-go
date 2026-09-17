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

	// ErrNilStore indicates a nil DefinitionStore where one was required. It is
	// what [DeclareDefinitions] reports, being the one thing here that is handed
	// the store rather than being it.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings definition store")

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

	// ErrDefinitionIDTaken indicates a create whose id another definition in
	// this scope already carries.
	//
	// It is reachable only from a caller that supplied the id — a create that
	// leaves it empty is given a minted one — and it is distinct from
	// ErrDefinitionNameTaken because it names a different mistake: that one is a
	// catalog defining a setting somebody has already defined, which is the
	// ordinary second boot, and this is an application handing out an identifier
	// it has used before.
	//
	// MySQL and SQLite report it here. Postgres raises its own primary key
	// violation instead, because the create's conflict target names the (scope,
	// name) index and a Postgres ON CONFLICT absorbs only the index it names,
	// where the IGNORE the other two spell covers every constraint on the table.
	// The three dialects agree on refusing the row and differ on which error says
	// so; nothing that is not a caller's own bug reaches either.
	ErrDefinitionIDTaken = platformerrors.New("another setting definition in this scope already has that id")

	// ErrDefinitionValueTooLong indicates a definition carrying a string longer
	// than the column that stores it — see MaxDefinitionIDLength and the three
	// bounds beside it, and the wrapped message for which one it was.
	//
	// It wraps errors.ErrUnrecognizedInputValue, so it is answered as a bad
	// request by the platform mapper rather than by a case of this package's
	// own, which is where a value of the wrong kind and a value outside its
	// enumeration are already answered.
	//
	// The limit is enforced in Go rather than left to the column, because the
	// create is an insert-ignore and MySQL's IGNORE downgrades a too-long value
	// to a warning that truncates it. A definition whose name was silently cut
	// to 255 bytes is a different setting from the one the caller declared, and
	// one whose default was cut is a setting that resolves to a value nobody
	// chose — neither of which the write would report. Postgres and SQLite store
	// the columns as TEXT and would take the full value, so the bound is also
	// what keeps one catalog from meaning different things on different
	// dialects.
	ErrDefinitionValueTooLong = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "setting definition value is too long")

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

	// ErrDuplicateDeclaration indicates a catalog handed to
	// [DeclareDefinitions] that names one setting twice.
	//
	// It is refused rather than converged on. Each declaration is reconciled
	// against the transaction the last one wrote in, so the second of a pair
	// would be an edit to what the first had just created — reported as an edit
	// to a setting nobody meant to edit, and where the two disagree, silently
	// taking the later one. Both readings are a typo in the catalog the binary
	// holds, and a boot is the right place to find one.
	ErrDuplicateDeclaration = platformerrors.New("catalog declares one setting twice")

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
