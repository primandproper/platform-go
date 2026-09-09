package grpc_test

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeNameIsReserved is what makes "the tenant comes off the
// connection" a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope"` is a schema protoc refuses the field into, here
// and in a consumer's fork of the file alike. It is audit/grpc's pattern,
// adopted rather than re-derived.
//
// The four messages a response is built from reserve it too. Every row a
// response returns belongs to the scope the connection resolved, so a scope
// field would tell a client something it supplied — and its absence is what
// makes a converter unable to read one back out of a request.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	messages := []protoreflect.FullName{
		"primandproper.platform.settings.v1.CreateDefinitionRequest",
		"primandproper.platform.settings.v1.GetDefinitionRequest",
		"primandproper.platform.settings.v1.GetDefinitionByNameRequest",
		"primandproper.platform.settings.v1.ListDefinitionsRequest",
		"primandproper.platform.settings.v1.UpdateDefinitionRequest",
		"primandproper.platform.settings.v1.ArchiveDefinitionRequest",
		"primandproper.platform.settings.v1.ListValuesForDefinitionRequest",
		"primandproper.platform.settings.v1.SetValueRequest",
		"primandproper.platform.settings.v1.GetValueRequest",
		"primandproper.platform.settings.v1.ClearValueRequest",
		"primandproper.platform.settings.v1.ListValuesForSubjectRequest",
		"primandproper.platform.settings.v1.ResolveRequest",
		"primandproper.platform.settings.v1.ResolveAllRequest",
		"primandproper.platform.settings.v1.SettingDefinitionInput",
		"primandproper.platform.settings.v1.SettingDefinition",
		"primandproper.platform.settings.v1.SettingValue",
		"primandproper.platform.settings.v1.SettingSubject",
		"primandproper.platform.settings.v1.ResolvedSetting",
	}

	for _, name := range messages {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			reserved := message.ReservedNames()

			var found bool

			for i := range reserved.Len() {
				if reserved.Get(i) == "scope" {
					found = true
				}
			}

			test.True(t, found, test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", name))
		})
	}
}

// TestNoMessageCarriesAScope is the same property read from the other side, and
// it walks every message in the file rather than the ones somebody remembered.
//
// A message added in a later revision is covered by this and not by the list
// above, which is why both exist.
func TestNoMessageCarriesAScope(T *testing.T) {
	T.Parallel()

	messages := settingspb.File_primandproper_platform_settings_v1_settings_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		fields := message.Fields()
		for j := range fields.Len() {
			name := string(fields.Get(j).Name())

			test.False(T, name == "scope" || strings.HasSuffix(name, "_scope"), test.Sprintf(
				"%s.%s puts a scope in a message, which is a tenant a client could name",
				message.Name(), name))
		}
	}
}

// TestTheServiceIsThirteenMethods pins the count the .proto's service comment
// argues for, so that a fourteenth arrives with a failing test naming the
// argument rather than as a diff nobody weighed against it.
//
// The fourteenth is DeleteValuesForSubject and the roster beside this file is
// where it is ruled out by name.
func TestTheServiceIsThirteenMethods(T *testing.T) {
	T.Parallel()

	methods := settingspb.File_primandproper_platform_settings_v1_settings_proto.
		Services().ByName("SettingsService").Methods()

	test.EqOp(T, 13, methods.Len())
}

// TestTheKindsAreTheClosedSetTheGoTypeDeclares is the other half of "a kind is
// an enum and a definition's name is not".
//
// settings.Kind is a closed set this package owns, so the enum is generated and
// a fifth kind is a change to both. A definition's name and a subject's type
// are the consumer's vocabulary and are strings, which this asserts by their
// absence: there is no enum here but these two.
func TestTheKindsAreTheClosedSetTheGoTypeDeclares(T *testing.T) {
	T.Parallel()

	enums := settingspb.File_primandproper_platform_settings_v1_settings_proto.Enums()

	names := make([]string, 0, enums.Len())
	for i := range enums.Len() {
		names = append(names, string(enums.Get(i).Name()))
	}

	test.SliceContainsAll(T, []string{"SettingKind", "ValueSource"}, names)

	// Five values: the four kinds and the unspecified case a request naming it
	// is refused for rather than defaulted into one of the four.
	test.EqOp(T, 5, enums.ByName("SettingKind").Values().Len())

	// Four: the three resolution cases and the unspecified one no resolution
	// this service returns carries.
	test.EqOp(T, 4, enums.ByName("ValueSource").Values().Len())
}

// TestTheTypedValueIsAOneofOfFourKinds is the design decision this surface's
// documentation argues for, pinned as a property of the schema.
//
// A oneof rather than a string plus a kind is what carries the parse, and it is
// also what carries presence: a string_value of "" sets the case and is a value
// somebody chose, where naming no case is a request with no value in it. A
// proto3 string field could hold neither.
func TestTheTypedValueIsAOneofOfFourKinds(T *testing.T) {
	T.Parallel()

	message := messageNamed(T, "primandproper.platform.settings.v1.TypedValue")

	oneofs := message.Oneofs()
	must.EqOp(T, 1, oneofs.Len())

	fields := oneofs.Get(0).Fields()
	test.EqOp(T, 4, fields.Len())

	kinds := map[string]protoreflect.Kind{}
	for i := range fields.Len() {
		field := fields.Get(i)
		kinds[string(field.Name())] = field.Kind()
	}

	test.EqOp(T, protoreflect.StringKind, kinds["string_value"])
	test.EqOp(T, protoreflect.BoolKind, kinds["bool_value"])
	test.EqOp(T, protoreflect.Int64Kind, kinds["int_value"])
	test.EqOp(T, protoreflect.DoubleKind, kinds["float_value"])
}

// TestADefinitionsDefaultCarriesPresence pins the other half of the same
// doctrine: absence is distinguishable from zero, on the wire as in the Go
// type, which is why the field is optional rather than a bare string.
func TestADefinitionsDefaultCarriesPresence(T *testing.T) {
	T.Parallel()

	for _, name := range []protoreflect.FullName{
		"primandproper.platform.settings.v1.SettingDefinition",
		"primandproper.platform.settings.v1.SettingDefinitionInput",
	} {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			field := messageNamed(t, name).Fields().ByName("default_value")
			must.NotNil(t, field)

			test.True(t, field.HasPresence(), test.Sprintf(
				"%s.default_value cannot tell a default of \"\" from no default at all", name))
		})
	}
}

func messageNamed(tb testing.TB, name protoreflect.FullName) protoreflect.MessageDescriptor {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	return message
}
