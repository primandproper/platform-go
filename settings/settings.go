package settings

import (
	"slices"
	"strconv"
	"strings"
	"time"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"
)

const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "settings"

	scopeKey        = serviceName + ".scope"
	definitionKey   = serviceName + ".definition"
	subjectTypeKey  = serviceName + ".subject_type"
	subjectIDKey    = serviceName + ".subject_id"
	countKey        = serviceName + ".count"
	sourceKey       = serviceName + ".source"
	definitionIDKey = serviceName + ".definition_id"
)

// Kind is what sort of value a setting holds.
//
// It is a closed set, unlike [SubjectType], and the two differ for a reason
// worth stating: a subject type names a kind of principal, which is the
// application's to invent, while a kind decides how a stored string is parsed —
// so a kind this package does not implement is a value nothing can read back.
// An application wanting a shape none of these express stores the encoding it
// chose as a [KindString] and parses it itself, which is honest about where the
// interpretation lives.
type Kind string

const (
	// KindString is any text. Every value is legal unless the definition
	// enumerates its own.
	KindString Kind = "string"
	// KindBool is a flag, stored as strconv.FormatBool writes it: "true" or
	// "false".
	KindBool Kind = "boolean"
	// KindInt is a signed 64-bit integer in base ten.
	KindInt Kind = "integer"
	// KindFloat is a 64-bit float, as strconv.ParseFloat reads one.
	KindFloat Kind = "float"
)

// Valid reports whether k is one of the four kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindString, KindBool, KindInt, KindFloat:
		return true
	default:
		return false
	}
}

// String renders the kind as it is stored.
func (k Kind) String() string { return string(k) }

// parses reports whether raw is a value of this kind.
//
// The empty string is a legal [KindString] and nothing else. That is the one
// asymmetry here and it is deliberate: a text setting whose value is "" has been
// answered with nothing, which is different from not having been answered, and
// [Resolution] is where that difference is reported.
func (k Kind) parses(raw string) error {
	var err error

	switch k {
	case KindString:
		return nil
	case KindBool:
		_, err = strconv.ParseBool(raw)
	case KindInt:
		_, err = strconv.ParseInt(raw, 10, 64)
	case KindFloat:
		_, err = strconv.ParseFloat(raw, 64)
	default:
		return platformerrors.Wrapf(ErrUnknownKind, "kind %q", k)
	}

	if err != nil {
		return platformerrors.Wrapf(ErrMalformedValue, "%q is not a %s", raw, k)
	}

	return nil
}

// SubjectType distinguishes the kinds of thing a setting can be about.
//
// Like dataprivacy.SubjectType and audit.ActorType this is a bare string with
// suggested constants rather than a closed set: an application whose settings
// hang off a third kind of principal — a device, a workspace, an API client —
// should say so rather than misfile it as one of these.
type SubjectType string

const (
	// SubjectUser is one person's own settings: their notification preferences,
	// their display choices.
	SubjectUser SubjectType = "user"
	// SubjectAccount is an account's, tenant's, or organization's settings —
	// the ones an administrator sets on everybody's behalf.
	SubjectAccount SubjectType = "account"
)

// String renders the subject type as it is stored.
func (t SubjectType) String() string { return string(t) }

// Subject is whose setting a value is.
//
// It is two fields rather than one composite string for the reason the tenancy
// doctrine gives for the scope: a key spelling "user:abc123" carries two facts
// in a column that can only be indexed, filtered and enumerated as one.
type Subject struct {
	// Type says what kind of principal this is. Required.
	Type SubjectType `json:"type"`
	// ID identifies the principal within that type. Required.
	ID string `json:"id"`
}

// Validate reports whether the subject names anything.
func (s Subject) Validate() error {
	if s.Type == "" {
		return ErrEmptySubjectType
	}

	if s.ID == "" {
		return ErrEmptySubjectID
	}

	return nil
}

// Definition is what a setting is: the name application code asks for, the kind
// of value it holds, what it falls back to, and which values it admits.
//
// Definitions are administrative rows. Nothing on a request path creates one —
// the catalog is a deployment's decision, in the same sense that a database
// column is — and what a request path does is read one and store an answer
// against it.
type Definition struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the definition was added. It is the database's clock
	// rather than the application's, read back by the write — see
	// settings/migrations.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the definition last changed, or nil for one that
	// has not been edited.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the definition was retired. An archived definition is
	// excluded from every read that does not ask for archived rows; the values
	// stored against it are left alone, because archiving is not erasure and
	// the name stays claimed.
	ArchivedAt *time.Time `json:"archivedAt"`

	// Default is what [SQLStore.Resolve] answers with for a subject that has
	// not set this setting, or nil for a definition with no default at all.
	//
	// A pointer rather than a string, and that is the whole of what "absence is
	// distinguishable from zero" means here. A text setting defaulting to ""
	// answers every subject that has not chosen; a text setting with no default
	// answers none of them, and the caller is told so — see [SourceUnset].
	Default *string `json:"default"`

	// ID identifies the definition. Minted on write when empty.
	ID string `json:"id"`

	// Name is what application code asks for, unique within Scope. It is the
	// only handle a value-side call takes: an application holds the name of the
	// setting it wants, not the id of a row it would have to look up first.
	Name string `json:"name"`

	// Description is prose for whoever administers the setting.
	Description string `json:"description"`

	// Kind is how a stored value is parsed.
	Kind Kind `json:"kind"`

	// Scope is whose catalog this definition is in. See the package
	// documentation on why a definition and the values against it share one.
	Scope tenancy.Scope `json:"scope"`

	// Enumeration is the values this setting admits, or empty for a setting that
	// admits any value of its kind.
	//
	// A set rather than a sequence: it comes back sorted, and what it decides is
	// whether a write is legal. Membership has no order, and a rendering order
	// is the caller's — see settings/migrations for why the schema does not
	// carry one.
	//
	// Every read that returns a Definition fills this in. A field populated on
	// some reads and not others would be indistinguishable from a setting that
	// enumerates nothing, and that reading fails open: every value would be
	// legal.
	Enumeration []string `json:"enumeration"`

	// AdminOnly marks a setting only an administrator may write. It is recorded
	// rather than enforced — this package has no notion of who is calling, and
	// a store that pretended to would be an authorization check in the wrong
	// layer. What it is for is the caller's own check, and the admin UI that
	// needs to know which settings to hide from a self-service page.
	AdminOnly bool `json:"adminOnly"`
}

// admits reports whether raw is a legal value for this definition: of the right
// kind, and in the enumeration where there is one.
func (d *Definition) admits(raw string) error {
	if err := d.Kind.parses(raw); err != nil {
		return err
	}

	if len(d.Enumeration) == 0 {
		return nil
	}

	if slices.Contains(d.Enumeration, raw) {
		return nil
	}

	return platformerrors.Wrapf(ErrNotEnumerated, "%q is not one of [%s]", raw, strings.Join(d.Enumeration, ", "))
}

// validate reports whether the definition is one this package can store.
//
// The default is held to the same rule a value is, which is what stops a
// definition from carrying a fallback no subject could have chosen: a default
// outside its own enumeration answers every subject that has not set the
// setting with a value the setting does not admit.
func (d *Definition) validate() error {
	if d == nil {
		return platformerrors.ErrNilInputParameter
	}

	if d.Name == "" {
		return ErrEmptyDefinitionName
	}

	if !d.Kind.Valid() {
		return platformerrors.Wrapf(ErrUnknownKind, "kind %q", d.Kind)
	}

	seen := make(map[string]struct{}, len(d.Enumeration))

	for _, option := range d.Enumeration {
		if option == "" {
			return platformerrors.Wrap(ErrEmptyEnumerationValue, "enumeration")
		}

		if _, duplicate := seen[option]; duplicate {
			return platformerrors.Wrapf(ErrDuplicateEnumerationValue, "%q", option)
		}

		seen[option] = struct{}{}

		if err := d.Kind.parses(option); err != nil {
			return err
		}
	}

	if d.Default == nil {
		return nil
	}

	return d.admits(*d.Default)
}

// Value is what one subject answered for one definition.
type Value struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the subject first answered. A value that was cleared and
	// set again keeps it, because the write converges on the row rather than
	// adding a second one.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the answer last changed, or nil for one set once.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the answer was cleared. A cleared value is excluded
	// from every read that does not ask for archived rows, and resolution falls
	// back to the definition's default as though it had never been set.
	ArchivedAt *time.Time `json:"archivedAt"`

	// Subject is whose answer it is.
	Subject Subject `json:"subject"`

	// ID identifies the row. It is not how the row is addressed — every
	// single-row statement keys on the scope, the subject and the definition —
	// and what it is for is the cursor a page walks.
	ID string `json:"id"`

	// DefinitionID is the definition this answers.
	DefinitionID string `json:"definitionID"`

	// Raw is the answer as it is stored. It is a string on purpose: one column
	// holds every kind, and [Resolution] is where it becomes a typed value.
	Raw string `json:"value"`

	// Scope is whose settings these are.
	Scope tenancy.Scope `json:"scope"`
}

// Source says where a resolved setting's value came from.
type Source string

const (
	// SourceSubject is a value the subject set.
	SourceSubject Source = "subject"
	// SourceDefault is the definition's default, for a subject that has not set
	// the setting.
	SourceDefault Source = "default"
	// SourceUnset is neither: the setting exists, the subject has not answered
	// it, and it has no default. Reading it as a typed value reports
	// [ErrSettingUnset] rather than the kind's zero.
	SourceUnset Source = "unset"
)

// String renders the source as it is reported.
func (s Source) String() string { return string(s) }

// Resolution is a setting resolved for a subject: the value, and where it came
// from.
//
// It is what a typed read hands back, and the reason it is a struct rather than
// four accessors on the store is the tri-state. A resolved setting is answered
// by the subject, answered by the default, or not answered at all, and a getter
// taking a fallback value cannot express the third — it would answer "unset"
// with whatever the caller guessed, and a caller that wanted to know would have
// no way to ask. So the third state is a value here and a sentinel from the
// accessors, which a caller matches with errors.Is.
type Resolution struct {
	_ struct{} `json:"-"`

	// Definition is the setting that was resolved. Never nil: resolution begins
	// by reading it, and a setting that does not exist is an error rather than
	// an unset resolution.
	Definition *Definition `json:"definition"`

	// Value is the row the subject set, or nil when the default answered or
	// nothing did.
	Value *Value `json:"value"`

	// Raw is the answer as stored, empty when Source is [SourceUnset].
	Raw string `json:"raw"`

	// Source says which of the three cases this is.
	Source Source `json:"source"`
}

// Set reports whether the setting was answered at all, by the subject or by the
// definition's default.
func (r *Resolution) Set() bool { return r != nil && r.Source != SourceUnset }

// String returns the resolved value of a [KindString] setting.
//
// It is deliberately not fmt.Stringer, despite the name: a resolution can fail
// to answer — the setting is of another kind, or nobody has set it — and a
// Stringer has nowhere to say so. The name is the one that pairs with [Kind],
// which is what a caller is choosing between when they reach for it, and the
// two-value signature is what keeps `fmt.Sprintf("%s", resolution)` from
// silently calling it.
func (r *Resolution) String() (string, error) {
	if err := r.readable(KindString); err != nil {
		return "", err
	}

	return r.Raw, nil
}

// Bool returns the resolved value of a [KindBool] setting.
func (r *Resolution) Bool() (bool, error) {
	if err := r.readable(KindBool); err != nil {
		return false, err
	}

	value, err := strconv.ParseBool(r.Raw)
	if err != nil {
		return false, platformerrors.Wrapf(ErrMalformedValue, "%q is not a %s", r.Raw, KindBool)
	}

	return value, nil
}

// Int returns the resolved value of a [KindInt] setting.
func (r *Resolution) Int() (int64, error) {
	if err := r.readable(KindInt); err != nil {
		return 0, err
	}

	value, err := strconv.ParseInt(r.Raw, 10, 64)
	if err != nil {
		return 0, platformerrors.Wrapf(ErrMalformedValue, "%q is not an %s", r.Raw, KindInt)
	}

	return value, nil
}

// Float returns the resolved value of a [KindFloat] setting.
func (r *Resolution) Float() (float64, error) {
	if err := r.readable(KindFloat); err != nil {
		return 0, err
	}

	value, err := strconv.ParseFloat(r.Raw, 64)
	if err != nil {
		return 0, platformerrors.Wrapf(ErrMalformedValue, "%q is not a %s", r.Raw, KindFloat)
	}

	return value, nil
}

// readable is the guard every accessor shares: the resolution is one this
// package produced, the setting is of the kind being asked for, and it was
// answered.
//
// The kind check is what makes these typed reads rather than four spellings of
// the same string. Calling Bool on a setting whose kind is integer is a mistake
// in the caller's code, and "false" is the worst possible answer to it.
func (r *Resolution) readable(kind Kind) error {
	if r == nil || r.Definition == nil {
		return platformerrors.ErrNilInputParameter
	}

	if r.Definition.Kind != kind {
		return platformerrors.Wrapf(ErrKindMismatch, "setting %q is a %s, read as a %s",
			r.Definition.Name, r.Definition.Kind, kind)
	}

	if r.Source == SourceUnset {
		return platformerrors.Wrapf(ErrSettingUnset, "setting %q", r.Definition.Name)
	}

	return nil
}
