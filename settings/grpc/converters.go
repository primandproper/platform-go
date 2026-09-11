package grpc

import (
	"strconv"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between settings' types and the generated messages.
//
// The ToProto half is exported, because a consumer composing a settings screen
// into a larger response — an account page that carries preferences beside
// everything else — otherwise writes the same assignments and gets one of them
// wrong. The FromProto half is not: reading a request is this surface's job,
// and every one of those functions is paired with a refusal only a handler can
// answer with.
//
// Where a value is typed and where it is a string is settings.proto's ruling
// and is not re-litigated here. What this file owns is the two directions of
// the parse it implies: [TypedValueToProto] reads a resolution through the
// accessors settings already ships, and rawFromTypedValue formats a case back
// into the text the store stores — the one conversion in the module that turns
// a wire type into a row.

// KindToProto renders a kind for the wire.
//
// A kind this package does not implement renders as unspecified rather than as
// one of the four. It cannot arrive from a request — kindFromProto maps the
// unspecified case to the empty Kind, which the store refuses — so the only way
// to reach it is a row written by something else, and answering "one of these
// four" for a value none of them parses is the coercion settings.Kind exists to
// refuse.
func KindToProto(kind settings.Kind) settingspb.SettingKind {
	switch kind {
	case settings.KindString:
		return settingspb.SettingKind_SETTING_KIND_STRING
	case settings.KindBool:
		return settingspb.SettingKind_SETTING_KIND_BOOLEAN
	case settings.KindInt:
		return settingspb.SettingKind_SETTING_KIND_INTEGER
	case settings.KindFloat:
		return settingspb.SettingKind_SETTING_KIND_FLOAT
	default:
		return settingspb.SettingKind_SETTING_KIND_UNSPECIFIED
	}
}

// kindFromProto reads a kind off a request.
//
// The unspecified case becomes the empty settings.Kind, which is not a kind and
// which the store refuses with settings.ErrUnknownKind naming it. That is
// deliberately not a refusal written here: a definition with no kind is
// malformed for the same reason whichever transport sent it, and the store is
// where that is already decided.
func kindFromProto(kind settingspb.SettingKind) settings.Kind {
	switch kind {
	case settingspb.SettingKind_SETTING_KIND_STRING:
		return settings.KindString
	case settingspb.SettingKind_SETTING_KIND_BOOLEAN:
		return settings.KindBool
	case settingspb.SettingKind_SETTING_KIND_INTEGER:
		return settings.KindInt
	case settingspb.SettingKind_SETTING_KIND_FLOAT:
		return settings.KindFloat
	case settingspb.SettingKind_SETTING_KIND_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

// SourceToProto renders where a resolved value came from.
//
// The three cases are the whole of settings.Source, and a fourth would be a
// resolution this package did not produce, so it renders as unspecified rather
// than as one of the three — a client switching on the source must not be told
// "the subject chose it" about a state nobody has named.
func SourceToProto(source settings.Source) settingspb.ValueSource {
	switch source {
	case settings.SourceSubject:
		return settingspb.ValueSource_VALUE_SOURCE_SUBJECT
	case settings.SourceDefault:
		return settingspb.ValueSource_VALUE_SOURCE_DEFAULT
	case settings.SourceUnset:
		return settingspb.ValueSource_VALUE_SOURCE_UNSET
	default:
		return settingspb.ValueSource_VALUE_SOURCE_UNSPECIFIED
	}
}

// SubjectToProto renders whose setting a value is.
func SubjectToProto(subject settings.Subject) *settingspb.SettingSubject {
	return &settingspb.SettingSubject{
		Type: subject.Type.String(),
		Id:   subject.ID,
	}
}

// subjectFromProto reads a subject off a request.
//
// A message that is absent reads as the zero subject rather than as an error,
// because the zero subject is one settings.Subject.Validate already refuses by
// name — an empty type and an empty id are two different refusals, and this
// would collapse them into one about a missing message.
func subjectFromProto(in *settingspb.SettingSubject) settings.Subject {
	if in == nil {
		return settings.Subject{}
	}

	return settings.Subject{
		Type: settings.SubjectType(in.GetType()),
		ID:   in.GetId(),
	}
}

// DefinitionToProto renders one setting's definition.
//
// The default keeps its presence: a definition with no default has no
// default_value at all, where one defaulting to "" carries an empty string. A
// proto3 string could not tell those apart, which is why the field is optional
// and why settings.Definition.Default is a *string.
func DefinitionToProto(d *settings.Definition) *settingspb.SettingDefinition {
	if d == nil {
		return nil
	}

	out := &settingspb.SettingDefinition{
		CreatedAt:   timestamppb.New(d.CreatedAt),
		Id:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Kind:        KindToProto(d.Kind),
		Enumeration: d.Enumeration,
		AdminOnly:   d.AdminOnly,
	}

	// The two nullable times stay unset rather than becoming the zero
	// timestamp: a console rendering "last edited" wants to know there was no
	// edit, and 1970 is not that answer.
	if d.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*d.LastUpdatedAt)
	}

	if d.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*d.ArchivedAt)
	}

	if d.Default != nil {
		out.DefaultValue = pointer.To(*d.Default)
	}

	return out
}

// DefinitionsToProto renders a page of definitions.
func DefinitionsToProto(definitions []*settings.Definition) []*settingspb.SettingDefinition {
	out := make([]*settingspb.SettingDefinition, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, DefinitionToProto(d))
	}

	return out
}

// definitionFromProto reads a definition off a request, under the id the
// request named.
//
// The id is an argument rather than a field of the input message, so a create
// cannot name one and an update cannot name two. The scope is neither: it comes
// off the caller's principal and is bound by the store from the argument it is
// handed, which overwrites whatever the entity carries.
//
// A nil message is nil rather than an empty definition, so a request that named
// no definition is refused as malformed instead of being reported as a
// definition with no name.
func definitionFromProto(in *settingspb.SettingDefinitionInput, id string) *settings.Definition {
	if in == nil {
		return nil
	}

	out := &settings.Definition{
		ID:          id,
		Name:        in.GetName(),
		Description: in.GetDescription(),
		Kind:        kindFromProto(in.GetKind()),
		Enumeration: in.GetEnumeration(),
		AdminOnly:   in.GetAdminOnly(),
	}

	if in.DefaultValue != nil {
		out.Default = pointer.To(in.GetDefaultValue())
	}

	return out
}

// ValueToProto renders one stored answer.
//
// It carries the raw text and not a [settingspb.TypedValue]. This is the row,
// and the row is where this package keeps a string; a value becomes typed in a
// resolution, which is the read that has the definition in hand.
func ValueToProto(v *settings.Value) *settingspb.SettingValue {
	if v == nil {
		return nil
	}

	out := &settingspb.SettingValue{
		CreatedAt:    timestamppb.New(v.CreatedAt),
		Subject:      SubjectToProto(v.Subject),
		Id:           v.ID,
		DefinitionId: v.DefinitionID,
		Raw:          v.Raw,
	}

	if v.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*v.LastUpdatedAt)
	}

	if v.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*v.ArchivedAt)
	}

	return out
}

// ValuesToProto renders a page of stored answers.
func ValuesToProto(values []*settings.Value) []*settingspb.SettingValue {
	out := make([]*settingspb.SettingValue, 0, len(values))
	for _, v := range values {
		out = append(out, ValueToProto(v))
	}

	return out
}

// TypedValueToProto reads a resolution through the accessors settings ships and
// renders the answer as the kind the definition declares.
//
// It is the parse this package exists to have made once, and it is made here
// rather than in every consumer's generated client. A resolution nobody has
// answered — [settings.SourceUnset] — is (nil, nil): the state is carried by
// the source rather than by an error, which is what
// settings.Resolution.readable reports as settings.ErrSettingUnset to a Go
// caller and what no RPC on this surface raises.
//
// A stored value that is not of its definition's kind is
// settings.ErrMalformedValue, and it fails the read it was found in rather than
// being dropped from it. A settings page that silently omitted the one setting
// whose row will not parse is a page showing a default nobody chose.
func TypedValueToProto(r *settings.Resolution) (*settingspb.TypedValue, error) {
	if r == nil || r.Definition == nil {
		return nil, platformerrors.ErrNilInputParameter
	}

	if !r.Set() {
		//nolint:nilnil // The unset state is carried by the source rather than by an error; the sentinel this would otherwise be is the one settings.Resolution refuses to make an ordinary settings page's most common row.
		return nil, nil
	}

	switch r.Definition.Kind {
	case settings.KindString:
		value, err := r.String()
		if err != nil {
			return nil, err
		}

		return &settingspb.TypedValue{Value: &settingspb.TypedValue_StringValue{StringValue: value}}, nil
	case settings.KindBool:
		value, err := r.Bool()
		if err != nil {
			return nil, err
		}

		return &settingspb.TypedValue{Value: &settingspb.TypedValue_BoolValue{BoolValue: value}}, nil
	case settings.KindInt:
		value, err := r.Int()
		if err != nil {
			return nil, err
		}

		return &settingspb.TypedValue{Value: &settingspb.TypedValue_IntValue{IntValue: value}}, nil
	case settings.KindFloat:
		value, err := r.Float()
		if err != nil {
			return nil, err
		}

		return &settingspb.TypedValue{Value: &settingspb.TypedValue_FloatValue{FloatValue: value}}, nil
	default:
		return nil, platformerrors.Wrapf(settings.ErrUnknownKind, "setting %q is a %q", r.Definition.Name, r.Definition.Kind)
	}
}

// ResolutionToProto renders a resolved setting: the definition, the row where
// there is one, the typed answer, and which of the three cases it is.
func ResolutionToProto(r *settings.Resolution) (*settingspb.ResolvedSetting, error) {
	if r == nil {
		return nil, platformerrors.ErrNilInputParameter
	}

	typed, err := TypedValueToProto(r)
	if err != nil {
		return nil, err
	}

	return &settingspb.ResolvedSetting{
		Definition: DefinitionToProto(r.Definition),
		Value:      ValueToProto(r.Value),
		TypedValue: typed,
		Source:     SourceToProto(r.Source),
	}, nil
}

// ResolutionsToProto renders every resolution in a subject's catalog, stopping
// at the first one that will not parse.
func ResolutionsToProto(resolutions []*settings.Resolution) ([]*settingspb.ResolvedSetting, error) {
	out := make([]*settingspb.ResolvedSetting, 0, len(resolutions))

	for _, r := range resolutions {
		resolved, err := ResolutionToProto(r)
		if err != nil {
			return nil, err
		}

		out = append(out, resolved)
	}

	return out, nil
}

// rawFromTypedValue formats a typed value into the text the store stores,
// refusing a case that is not the setting's kind.
//
// The kind check is the whole reason a write carries a typed value rather than
// a string. Without it an int_value of 1 written to a text setting is stored as
// "1" and nothing complains — settings.KindString admits any text — and a
// string_value of "true" written to a boolean setting parses and is accepted,
// so a client using the wrong case would be told nothing on either. With it,
// each is settings.ErrKindMismatch naming both kinds, which is a sentence
// somebody can act on.
//
// The formats are strconv's, which is what settings.Kind.parses reads back:
// "true"/"false" for a boolean, base ten for an integer, and the shortest
// representation that round-trips for a float. A value formatted here and
// parsed by the store is the same value, and that is the property the store's
// own check re-establishes rather than assumes.
func rawFromTypedValue(kind settings.Kind, in *settingspb.TypedValue) (string, error) {
	if in == nil || in.GetValue() == nil {
		return "", ErrNoValueNamed
	}

	switch value := in.GetValue().(type) {
	case *settingspb.TypedValue_StringValue:
		if kind != settings.KindString {
			return "", mismatch(kind, settings.KindString)
		}

		return value.StringValue, nil
	case *settingspb.TypedValue_BoolValue:
		if kind != settings.KindBool {
			return "", mismatch(kind, settings.KindBool)
		}

		return strconv.FormatBool(value.BoolValue), nil
	case *settingspb.TypedValue_IntValue:
		if kind != settings.KindInt {
			return "", mismatch(kind, settings.KindInt)
		}

		return strconv.FormatInt(value.IntValue, 10), nil
	case *settingspb.TypedValue_FloatValue:
		if kind != settings.KindFloat {
			return "", mismatch(kind, settings.KindFloat)
		}

		return strconv.FormatFloat(value.FloatValue, 'g', -1, 64), nil
	default:
		// A case this package does not know, which means a schema newer than
		// this binary. It is not a kind mismatch — nothing has been compared —
		// and it is not a value either.
		return "", ErrNoValueNamed
	}
}

// mismatch is the refusal a wrong case earns, in the wording
// settings.Resolution gives the same mistake read from the other direction.
func mismatch(declared, sent settings.Kind) error {
	return platformerrors.Wrapf(settings.ErrKindMismatch, "the setting is a %s, set as a %s", declared, sent)
}
