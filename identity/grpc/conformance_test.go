package grpc_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The schema and the struct are two descriptions of one type, and this is what
// keeps them the same description — the check filtering/grpc already makes,
// applied to the four nouns.
//
// A field added to identity.User and not to the .proto is a field a gRPC client
// cannot see; a field added to the .proto and not to the struct is one the
// server can never fill. Both are silent: everything compiles, the converters
// convert, and the value simply does not arrive.
//
// They are matched on JSON names because that is a name both descriptions
// already carry for their own reasons — protobuf derives json_name from the
// field name, and the struct carries a json tag for whatever serves it over
// HTTP. Where the two would have derived different spellings the .proto pins
// json_name explicitly, which is what makes this a check rather than a
// negotiation.

// goOnlyFields are the fields the Go types carry and the schema deliberately
// does not, spelled once with the reason.
//
// It is a closed list, so a field that goes missing from the schema by accident
// fails here rather than joining a category. Every entry is scope: the schema
// has no scope on purpose — a scope a client could send is a cross-tenant read,
// and a scope a client is told is a fact about the deployment it has no use for.
// The credential columns are not listed because they are already `json:"-"` and
// so are invisible to this check from the Go side too.
var goOnlyFields = map[string][]string{
	"User":       {"scope"},
	"Account":    {"scope"},
	"Membership": {"scope"},
	"Invitation": {"scope"},
	"Principal":  {},
}

func jsonNames(t *testing.T, rt reflect.Type) map[string]string {
	t.Helper()

	names := map[string]string{}

	for field := range rt.Fields() {
		if !field.IsExported() {
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}

		names[name] = field.Name
	}

	must.MapNotEmpty(t, names)

	return names
}

func protoJSONNames(t *testing.T, md protoreflect.MessageDescriptor) map[string]string {
	t.Helper()

	names := map[string]string{}

	fields := md.Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		names[field.JSONName()] = string(field.Name())
	}

	must.MapNotEmpty(t, names)

	return names
}

func assertConformance(t *testing.T, name string, rt reflect.Type, md protoreflect.MessageDescriptor) {
	t.Helper()

	goFields := jsonNames(t, rt)
	protoFields := protoJSONNames(t, md)
	allowed := goOnlyFields[name]

	for jsonName, goName := range goFields {
		if slices.Contains(allowed, jsonName) {
			continue
		}

		if _, ok := protoFields[jsonName]; !ok {
			t.Errorf(
				"%s.%s (json %q) has no field in %s; add it to the .proto and regenerate with `make proto format`",
				rt.Name(), goName, jsonName, md.FullName())
		}
	}

	for jsonName, protoName := range protoFields {
		if _, ok := goFields[jsonName]; !ok {
			t.Errorf("%s.%s (json %q) has no field on %s; the schema describes something the struct cannot hold",
				md.FullName(), protoName, jsonName, rt.Name())
		}
	}

	// The allowance is checked in both directions, so an entry that stops being
	// true — a scope the schema grew — fails here rather than silently excusing
	// a field that no longer needs excusing.
	for _, jsonName := range allowed {
		if _, ok := protoFields[jsonName]; ok {
			t.Errorf("%s carries %q, which goOnlyFields says it deliberately does not", md.FullName(), jsonName)
		}
	}
}

func TestSchemaConformance(T *testing.T) {
	T.Parallel()

	T.Run("User", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "User",
			reflect.TypeFor[identity.User](), (&identitypb.User{}).ProtoReflect().Descriptor())
	})

	T.Run("Account", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Account",
			reflect.TypeFor[identity.Account](), (&identitypb.Account{}).ProtoReflect().Descriptor())
	})

	T.Run("BillingAddress", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "BillingAddress",
			reflect.TypeFor[identity.BillingAddress](),
			(&identitypb.BillingAddress{}).ProtoReflect().Descriptor())
	})

	T.Run("Membership", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Membership",
			reflect.TypeFor[identity.Membership](), (&identitypb.Membership{}).ProtoReflect().Descriptor())
	})

	T.Run("Invitation", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Invitation",
			reflect.TypeFor[identity.Invitation](), (&identitypb.Invitation{}).ProtoReflect().Descriptor())
	})

	T.Run("Principal", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Principal",
			reflect.TypeFor[identity.Principal](), (&identitypb.Principal{}).ProtoReflect().Descriptor())
	})

	// MembershipWithUser is deliberately absent. The Go type embeds Membership
	// and the message nests it in a field, because proto3 has no embedding — so
	// the two describe the same roster row with different shapes rather than
	// different fields, and matching them on JSON names would be asserting that
	// protobuf grew a feature it has not.
}

// TestUpdateInputsConform holds the two update inputs to the same field list as
// the nouns above. They are the messages presence lives on, so a field added to
// identity.ProfileUpdate and not to the schema is a field no client can change,
// and one added to the schema and not to the struct is one the converter drops
// on the floor — both the silence this file exists to end.
func TestUpdateInputsConform(T *testing.T) {
	T.Parallel()

	T.Run("ProfileUpdate", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "ProfileUpdate",
			reflect.TypeFor[identity.ProfileUpdate](),
			(&identitypb.ProfileUpdateInput{}).ProtoReflect().Descriptor())
	})

	T.Run("AccountUpdate", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "AccountUpdate",
			reflect.TypeFor[identity.AccountUpdate](),
			(&identitypb.AccountUpdateInput{}).ProtoReflect().Descriptor())
	})
}
