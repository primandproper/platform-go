package anonymous

import (
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// servicePrefix is what every service this module ships is named under.
const servicePrefix = "primandproper.platform."

// TestRosterCoversEveryService is what keeps the roster from being a list that
// slowly stops describing the module.
//
// It does not read a second list. Every protobuf package linked into this
// binary registers its descriptors globally, and the roster imports all twelve,
// so the registry's own account of what exists is available here for free — and
// a thirteenth surface added later arrives in it the moment its sample message
// joins the roster's imports, or fails here if nobody added one.
//
// That is the direction that matters. An entry here for a service that no
// longer exists fails to compile, because its sample message would be gone. An
// entry *missing* for a service that does exist compiles perfectly and quietly
// asserts nothing about it, which is the failure this test is for: a new
// surface's every RPC would go unchecked and the suite would still report a
// pass.
func TestRosterCoversEveryService(t *testing.T) {
	t.Parallel()

	covered := map[protoreflect.FullName]struct{}{}

	for _, surf := range roster() {
		file := surf.def.Sample.ProtoReflect().Descriptor().ParentFile()

		service := file.Services().ByName(surf.def.Service)
		must.NotNil(t, service, must.Sprintf("roster entry %q names no service %q", surf.def.Name, surf.def.Service))

		covered[service.FullName()] = struct{}{}
	}

	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		services := file.Services()

		for i := range services.Len() {
			name := services.Get(i).FullName()

			if !strings.HasPrefix(string(name), servicePrefix) {
				continue
			}

			test.MapContainsKey(t, covered, name,
				test.Sprintf("%s is mounted by this module and no roster entry covers it, so none of its RPCs is asserted against an anonymous caller", name))
		}

		return true
	})
}

// TestEveryDeclaredAnonymousMethodExists checks the three exception lists
// against the descriptors they are exceptions to.
//
// A method renamed in a .proto leaves its entry in one of those lists pointing
// at nothing, and the suite would then assert that a method which requires a
// caller is reachable without one — reading a stale name as a missing
// exception, which is the direction that fails open.
func TestEveryDeclaredAnonymousMethodExists(t *testing.T) {
	t.Parallel()

	for _, surf := range roster() {
		if surf.anonymous == nil {
			continue
		}

		t.Run(surf.def.Name, func(t *testing.T) {
			t.Parallel()

			file := surf.def.Sample.ProtoReflect().Descriptor().ParentFile()
			service := file.Services().ByName(surf.def.Service)
			must.NotNil(t, service)

			declared := map[string]struct{}{}

			methods := service.Methods()
			for i := range methods.Len() {
				declared["/"+string(service.FullName())+"/"+string(methods.Get(i).Name())] = struct{}{}
			}

			for _, full := range surf.anonymous {
				test.MapContainsKey(t, declared, full,
					test.Sprintf("%s declares %s reachable without a caller, and the service has no such method", surf.def.Name, full))
			}

			// An exception list that named every method would make the suite
			// assert nothing at all for that surface, which is worth catching
			// separately from a stale name. passwordreset is the one place it
			// is legitimate, and it says why in the roster.
			if surf.def.Name != "passwordreset" {
				test.Less(t, methods.Len(), len(surf.anonymous),
					test.Sprintf("%s declares every one of its methods anonymous; nothing is asserted for it", surf.def.Name))
			}
		})
	}
}
