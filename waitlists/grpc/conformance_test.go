package grpc_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The schema and the struct are two descriptions of one type, and this is what
// keeps them the same description — identity/grpc's check, applied to the two
// nouns and the subject.
//
// A field added to waitlists.Signup and not to the .proto is a field a gRPC
// client cannot see; a field added to the .proto and not to the struct is one
// the server can never fill. Both are silent: everything compiles, the
// converters convert, and the value simply does not arrive.
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
// fails here rather than joining a category. Every entry is one of two
// decisions, and both are also enforced structurally by a reserved name in the
// .proto — this list is where the reason lives.
//
// scope: a scope a client could send is a cross-tenant write, and a scope a
// client is told is a fact about the deployment it has no use for. It is
// resolved off the caller or the connection.
//
// contactDigest: it is unsalted over a fast hash, deliberately, so a client
// holding one could test any address it liked against it offline. It stays in
// process, where waitlists.SQLStore.Digest exports it to the one caller that
// already holds the addresses.
var goOnlyFields = map[string][]string{
	"List":    {"scope"},
	"Signup":  {"scope", "contactDigest"},
	"Subject": {},
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

	// The Go type is List and the message is Waitlist, because List is a word
	// every language a consumer generates into has already spent. The two are
	// matched here explicitly rather than by name.
	T.Run("List", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "List",
			reflect.TypeFor[waitlists.List](), (&waitlistspb.Waitlist{}).ProtoReflect().Descriptor())
	})

	T.Run("Signup", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Signup",
			reflect.TypeFor[waitlists.Signup](), (&waitlistspb.Signup{}).ProtoReflect().Descriptor())
	})

	T.Run("Subject", func(t *testing.T) {
		t.Parallel()

		assertConformance(t, "Subject",
			reflect.TypeFor[waitlists.Subject](), (&waitlistspb.SignupSubject{}).ProtoReflect().Descriptor())
	})
}

// TestTheStatusEnumCoversEveryStoredStatus holds the one generated enum in this
// schema to the closed set waitlists owns.
//
// It is an enum where a consumer's catalog would be a string, because the four
// statuses decide which transitions the store will make and what a withdrawal
// means — a fifth is not a word an application adds, it is a row nothing can
// move. What follows from that is that the two sets have to stay the same size:
// a status added to waitlists.Status and not here is one no client can render,
// and one added here and not there is a value no row can hold.
func TestTheStatusEnumCoversEveryStoredStatus(T *testing.T) {
	T.Parallel()

	stored := []waitlists.Status{
		waitlists.StatusWaiting,
		waitlists.StatusInvited,
		waitlists.StatusConverted,
		waitlists.StatusWithdrawn,
	}

	values := waitlistspb.SignupStatus_name

	// The unspecified zero value is the one the schema has and the store does
	// not: no stored signup carries it, and it is how a client that did not set
	// the field is told so rather than told the wrong status.
	must.MapLen(T, len(stored)+1, values)

	for _, status := range stored {
		T.Run(status.String(), func(t *testing.T) {
			t.Parallel()

			want := "SIGNUP_STATUS_" + strings.ToUpper(status.String())

			var found bool

			for _, name := range values {
				if name == want {
					found = true
				}
			}

			must.True(t, found, must.Sprintf(
				"waitlists.Status %q has no %s in the schema, so no client can render it", status, want))
		})
	}
}
