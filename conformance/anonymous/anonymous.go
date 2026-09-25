package anonymous

import (
	"slices"
	"testing"

	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/services"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// surface is one service and which of its methods are deliberately reachable
// without a caller.
type surface struct {
	def services.Service

	// why says what the exception is for, on the three entries that have one.
	why string

	// anonymous are the full method names that require no caller, taken from
	// the surface's own declaration so that this roster cannot disagree with
	// it.
	//
	// nil means every method requires a caller.
	anonymous []string
}

// exceptions are the three surfaces with methods reachable without a caller,
// each read from the surface's own declaration.
func exceptions() map[string]surface {
	return map[string]surface{
		"passwordreset": {
			anonymous: passwordresetgrpc.AnonymousMethods(),
			why:       "every RPC on it is for somebody who cannot sign in, so it reads no caller at all",
		},
		"signin": {
			anonymous: signingrpc.AnonymousMethods(),
			why:       "sign-in itself, and the doors that finish a registration or end a session",
		},
		"waitlists": {
			anonymous: waitlistsgrpc.PublicMethods(),
			why:       "the signup page, the form it submits, and the unsubscribe link in the mail that follows",
		},
	}
}

// roster is every gRPC surface this module mounts, with its exceptions.
//
// The services are conformance/internal/services' list, which the pagination
// suites read too. The completeness of it is asserted rather than trusted:
// TestRosterCoversEveryService walks the module's own account of what it
// mounts and fails on a service nobody put there.
func roster() []surface {
	all := services.All()
	except := exceptions()

	out := make([]surface, 0, len(all))

	for i := range all {
		surf := except[all[i].Name]
		surf.def = all[i]

		out = append(out, surf)
	}

	return out
}

// Suite asserts what every RPC does with a request carrying no caller.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "anonymous",

		// Every subject mounts something, and each surface's own presence is
		// read inside. Reporting "not mounted" for the whole suite would need
		// a subject that mounted nothing at all.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)

	// The HTTP half first, and in a subtest of its own, so that a subject
	// serving only HTTP is not skipped for want of a callerless gRPC
	// connection it has no use for.
	t.Run("http", func(t *testing.T) {
		t.Parallel()

		runHTTP(t, s, probe)
	})

	anonymous := s.Seams().Anonymous
	if anonymous == nil {
		t.Skip("conformance: this subject supplies no callerless connection, and one cannot be synthesized from an authenticated one")
	}

	conn, err := anonymous(t.Context())
	must.NoError(t, err, must.Sprint("opening a connection carrying no caller"))
	must.NotNil(t, conn, must.Sprint("the subject returned no connection and no error"))

	surfaces := roster()

	for i := range surfaces {
		surf := &surfaces[i]

		if !surf.def.Mounted(probe.Surfaces) {
			continue
		}

		t.Run(surf.def.Name, func(t *testing.T) {
			t.Parallel()

			service := descriptorFor(t, surf)
			methods := service.Methods()

			for i := range methods.Len() {
				method := methods.Get(i)
				full := "/" + string(service.FullName()) + "/" + string(method.Name())
				open := slices.Contains(surf.anonymous, full)

				t.Run(string(method.Name()), func(t *testing.T) {
					t.Parallel()

					callErr := conn.Invoke(t.Context(), full,
						dynamicpb.NewMessage(method.Input()),
						dynamicpb.NewMessage(method.Output()))

					if open {
						// Reachable, not successful. An empty request will
						// usually fail on its input, and that is fine — what is
						// asserted is that it did not fail on who was asking.
						test.NotEqOp(t, codes.Unauthenticated, status.Code(callErr),
							test.Sprintf("%s is declared reachable without a caller (%s) and was refused for want of one", full, surf.why))

						return
					}

					must.Error(t, callErr,
						must.Sprintf("%s answered a request carrying no caller", full))
					test.EqOp(t, codes.Unauthenticated, status.Code(callErr),
						test.Sprintf("%s refused a callerless request as something other than unauthenticated", full))
				})
			}
		})
	}
}

// descriptorFor is the schema's own account of a service, which is what makes
// the loop above enumerate RPCs rather than list them.
func descriptorFor(t *testing.T, surf *surface) protoreflect.ServiceDescriptor {
	t.Helper()

	file := surf.def.Sample.ProtoReflect().Descriptor().ParentFile()

	service := file.Services().ByName(surf.def.Service)
	must.NotNil(t, service, must.Sprintf("the generated file for %s describes no %s", surf.def.Name, surf.def.Service))
	must.Positive(t, service.Methods().Len(), must.Sprintf("%s describes no methods", surf.def.Service))

	return service
}
