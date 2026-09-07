package grpc_test

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// serviceDescriptor is the schema's own view of IdentityService, which is what
// the tests in this package assert against rather than against the generated Go.
// The Go is a rendering of this; the schema is the contract a consumer in
// another language sees.
func serviceDescriptor(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()

	file := identitypb.File_primandproper_platform_identity_v1_identity_proto
	must.NotNil(t, file)

	services := file.Services()
	must.EqOp(t, 1, services.Len(), must.Sprint("identity.proto should declare exactly one service"))

	return services.Get(0)
}

// requestDescriptorFor resolves the request message of one full method name.
func requestDescriptorFor(t *testing.T, fullMethod string) protoreflect.MessageDescriptor {
	t.Helper()

	name := fullMethod[strings.LastIndex(fullMethod, "/")+1:]

	method := serviceDescriptor(t).Methods().ByName(protoreflect.Name(name))
	must.NotNil(t, method, must.Sprintf("no method %q on the service descriptor", name))

	return method.Input()
}

// walkFields visits every field of a message and, depth-first, of every message
// it nests, calling fn with the message the field is declared on. A message
// type reached twice is visited once, which is what keeps a recursive schema
// from being an infinite walk.
func walkFields(md protoreflect.MessageDescriptor, fn func(protoreflect.MessageDescriptor, protoreflect.FieldDescriptor)) {
	seen := map[protoreflect.FullName]bool{}

	var walk func(protoreflect.MessageDescriptor)

	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}

		seen[md.FullName()] = true

		fields := md.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			fn(md, fd)

			if fd.Message() != nil {
				walk(fd.Message())
			}
		}
	}

	walk(md)
}
