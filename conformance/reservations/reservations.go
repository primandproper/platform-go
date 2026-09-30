package reservations

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/services"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Suite asserts that the subject refuses a member every call it reserves on
// one of this module's surfaces.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "reservations",

		// What is reserved is read from the seams and whether each entry's
		// surface is mounted inside, so the suite as a whole always runs.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	reserved := s.Seams().OperatorMethods
	if len(reserved) == 0 {
		conformance.Skip(t, "conformance: this subject reserves nothing, so there is no reservation to hold it to")
	}

	probe := s.Subject(t)

	for _, full := range reserved {
		t.Run(full, func(t *testing.T) {
			t.Parallel()

			assertReserved(t, s, probe, full)
		})
	}
}

// assertReserved holds the subject to one entry of its reservation: an
// administrator's empty call is not refused for who is asking, and a member's
// is.
//
// The control goes first because without it the refusal proves nothing. A
// method that refuses everybody, or a handler that answers an empty request
// with PermissionDenied, refuses a member too, and neither is a reservation.
func assertReserved(t *testing.T, s *conformance.Session, probe *conformance.Subject, full string) {
	t.Helper()

	surface, method := resolve(t, full)
	if !surface.Mounted(probe.Surfaces) {
		conformance.Skipf(t, "conformance: this subject reserves %s and mounts no %s surface to make it on", full, surface.Name)
	}

	admin := s.Subject(t, conformance.AsAdmin(), conformance.Making(full))

	switch control := call(admin.Context(t.Context()), admin.Conn, full, method); status.Code(control) {
	case codes.PermissionDenied:
		if reason, listed := emptyRequestRefused[full]; listed {
			conformance.Skipf(t, "conformance: %s refuses an administrator's empty request as PermissionDenied too (%s), "+
				"so a reservation cannot be told from it", full, reason)
		}

		t.Fatalf("%s refused an administrator as PermissionDenied (%v); the subject reserves it to its operators "+
			"and refuses them it, and its handler is not one emptyRequestRefused says refuses an empty request", full, control)
	case codes.Unauthenticated:
		t.Fatalf("%s refused an administrator as unauthenticated; the subject's administrator credential is not reaching the service", full)
	default:
	}

	member := s.Subject(t, conformance.Attempting(full))
	err := call(member.Context(t.Context()), member.Conn, full, method)

	switch code := status.Code(err); code {
	case codes.PermissionDenied:
	case codes.OK:
		t.Errorf("%s answered a member; the subject names it in Seams.OperatorMethods, and its interceptor does not refuse it", full)
	case codes.InvalidArgument:
		t.Errorf("%s answered InvalidArgument to a member; a reserved call must be refused before its request is read, "+
			"or its shape is disclosed to non-staff", full)
	case codes.Unauthenticated:
		t.Errorf("%s refused a member as unauthenticated; the subject's member credential is not reaching the service", full)
	default:
		t.Errorf("%s refused a member as %s (%v) rather than PermissionDenied", full, code, err)
	}
}

// resolve finds full among this module's surfaces, skipping where it names a
// service this module does not ship.
//
// What this module promises is about its own surfaces. A deployment may
// reserve a call on a service of its own, and that the deployment refuses it
// to a member is the deployment's test, beside its test that an operator may
// make it: an empty request is no control on a method this module knows
// nothing about, since it may as easily be a valid command as a refused one.
func resolve(t *testing.T, full string) (*services.Service, protoreflect.MethodDescriptor) {
	t.Helper()

	// Run has already checked the spelling.
	serviceName, methodName, _ := strings.Cut(strings.TrimPrefix(full, "/"), "/")

	all := services.All()
	for i := range all {
		descriptor := all[i].Descriptor()
		if descriptor == nil || string(descriptor.FullName()) != serviceName {
			continue
		}

		method := descriptor.Methods().ByName(protoreflect.Name(methodName))
		if method == nil {
			t.Fatalf("conformance: Seams.OperatorMethods names %s, and %s has no method %s; the entry reserves nothing",
				full, serviceName, methodName)
		}

		return &all[i], method
	}

	conformance.Skipf(t, "conformance: %s is not on one of this module's surfaces; that the subject refuses it to a member is "+
		"the subject's own test, beside its test that an operator may make it", full)

	return nil, nil
}

// call makes method with an empty request over conn, as a unary call or a
// stream according to its descriptor, and returns what the call was answered
// with.
//
// A stream is opened, sent the one request a server-streaming method reads or
// nothing at all for one that reads a stream, closed, and read once: a refusal
// in any interceptor arrives on that read, which is where a deployment that
// enforces its streams apart from its unary calls is caught.
func call(ctx context.Context, conn grpc.ClientConnInterface, full string, method protoreflect.MethodDescriptor) error {
	in, out := dynamicpb.NewMessage(method.Input()), dynamicpb.NewMessage(method.Output())

	if !method.IsStreamingClient() && !method.IsStreamingServer() {
		return conn.Invoke(ctx, full, in, out)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{
		StreamName:    string(method.Name()),
		ClientStreams: method.IsStreamingClient(),
		ServerStreams: method.IsStreamingServer(),
	}, full)
	if err != nil {
		return err
	}

	if !method.IsStreamingClient() {
		if err = stream.SendMsg(in); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}

	if err = stream.CloseSend(); err != nil {
		return err
	}

	// A stream that ends without a message was answered, not refused.
	if err = stream.RecvMsg(out); errors.Is(err, io.EOF) {
		return nil
	}

	return err
}
