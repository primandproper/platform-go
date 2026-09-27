package conformance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/services"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestReservableMethods keeps the roster honest in the two directions it can be
// wrong without anything else saying so.
//
// An entry naming no RPC is one no deployment can reserve through it: a rename
// would leave a deployment's reservation of the renamed call refused by Run, or
// its operator minted for a name the call no longer has. And an
// entry its surface's suite does not name in its documentation is a call a
// deployment reserving it is not told about, which is the promise the roster
// exists to keep. Each suite lives in the directory named for its surface.
func TestReservableMethods(t *testing.T) {
	t.Parallel()

	methods := conformance.ReservableMethods()
	must.SliceNotEmpty(t, methods)

	all := services.All()
	byFullName := map[protoreflect.FullName]*services.Service{}

	for i := range all {
		descriptor := all[i].Descriptor()
		must.NotNil(t, descriptor, must.Sprintf("no descriptor for %s", all[i].Name))
		byFullName[descriptor.FullName()] = &all[i]
	}

	seen := map[string]struct{}{}

	for _, method := range methods {
		test.MapNotContainsKey(t, seen, method, test.Sprintf("%s is listed twice", method))
		seen[method] = struct{}{}

		service, name, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
		must.True(t, ok, must.Sprintf("%s is not a full method name", method))

		svc, found := byFullName[protoreflect.FullName(service)]
		if !found {
			t.Errorf("%s is on a service no suite covers", method)

			continue
		}

		test.NotNil(t, svc.Descriptor().Methods().ByName(protoreflect.Name(name)),
			test.Sprintf("%s names no RPC on %s", method, service))

		doc, err := os.ReadFile(filepath.Join(svc.Name, "doc.go"))
		must.NoError(t, err, must.Sprintf("reading the %s suite's documentation", svc.Name))
		test.StrContains(t, string(doc), name,
			test.Sprintf("the %s suite's documentation does not name %s, which it routes to an operator where a deployment reserves it", svc.Name, name))
	}
}
