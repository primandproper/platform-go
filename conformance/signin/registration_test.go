package signin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// closedSignUp is a deployment built with signingrpc.WithoutOpenRegistration:
// it refuses every Register as that server does, and answers a verification
// link as one that registered nobody must — every link is one it never mailed.
// Nothing else is part of it. It counts the registrations it was asked for.
type closedSignUp struct {
	registrations atomic.Int64
}

var _ grpc.ClientConnInterface = (*closedSignUp)(nil)

func (d *closedSignUp) Invoke(_ context.Context, method string, _, _ any, _ ...grpc.CallOption) error {
	switch method {
	case registerUser:
		d.registrations.Add(1)

		return refusal(codes.Unimplemented, domain.ErrRegistrationClosed, reasonRegistrationClosed)
	case verifyEmailAddress:
		return refusal(codes.Unauthenticated, domain.ErrInvalidCredentials, reasonInvalidCredentials)
	default:
		return status.Errorf(codes.Unimplemented, "%s is not part of this deployment", method)
	}
}

// refusal is a status as signin/grpc answers a client-safe sentinel: its own
// words, and its reason in signin's domain.
func refusal(code codes.Code, sentinel error, reason string) error {
	st, err := status.New(code, sentinel.Error()).WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: domain.ClientReasonDomain,
	})
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	return st.Err()
}

func (*closedSignUp) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams")
}

// passwordRequired is a deployment whose signin.RegistrationPolicy refuses a
// registrant who names no password: it answers that registration as signin/grpc
// answers a policy's refusal, and admits one naming a password unless its
// username is taken — which it is only by somebody it admitted, so a refusal
// that left somebody behind would refuse the registration after it. Nothing
// else is part of it. It counts the registrations of each kind it was asked
// for.
type passwordRequired struct {
	usernames    map[string]bool
	passwordless atomic.Int64
	withPassword atomic.Int64
	mu           sync.Mutex
}

var _ grpc.ClientConnInterface = (*passwordRequired)(nil)

func (d *passwordRequired) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	if method != registerUser {
		return status.Errorf(codes.Unimplemented, "%s is not part of this deployment", method)
	}

	request, ok := args.(*signinpb.RegisterRequest)
	if !ok {
		return status.Errorf(codes.Internal, "a registration arrived as %T", args)
	}

	if request.GetNoPassword() != nil {
		d.passwordless.Add(1)

		return refusal(codes.InvalidArgument, domain.ErrRegistrationRefused, reasonRegistrationRefused)
	}

	d.withPassword.Add(1)

	response, ok := reply.(*signinpb.RegisterResponse)
	if !ok {
		return status.Errorf(codes.Internal, "a registration's answer was asked for as %T", reply)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	username := request.GetUser().GetUsername()
	if d.usernames[username] {
		return status.Error(codes.AlreadyExists, "username is taken")
	}

	d.usernames[username] = true

	response.Registration = &signinpb.Registered{
		User: &identitypb.User{Id: identifiers.New(), Username: username},
	}

	return nil
}

func (*passwordRequired) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams")
}

func TestRegistration(T *testing.T) {
	T.Parallel()

	T.Run("a deployment that closed its sign-up door is held to the refusal, and nothing else registers", func(t *testing.T) {
		t.Parallel()

		deployment := &closedSignUp{}

		seams := conformance.Seams{
			NewSubject: func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
				return &conformance.Subject{
					Scope:    tenancy.Global(),
					Conn:     deployment,
					Surfaces: conformance.Surfaces{SignIn: signinpb.NewSignInServiceClient(deployment)},
				}, nil
			},
			Anonymous: func(context.Context) (grpc.ClientConnInterface, error) {
				return deployment, nil
			},
			RegistrationClosed: true,
		}

		var inner *testing.T

		// Run's suites are parallel subtests, and t.Run returns only once its
		// parallel subtests have finished, which is what lets inner be read after.
		t.Run("run", func(t *testing.T) {
			conformance.Run(t, seams, conformance.Suite{
				Name: surface,
				Run: func(t *testing.T, s *conformance.Session) {
					t.Helper()

					inner = t
					registration(t, s)
				},
			})
		})

		// The closed-door assertion passed against the refusal the server
		// sends, and it is the only one that knocked: every assertion that
		// registers somebody skipped before asking.
		must.NotNil(t, inner)
		test.False(t, inner.Failed(), test.Sprint("the registration assertions failed against a deployment that closed its door as the server does"))
		test.EqOp(t, int64(1), deployment.registrations.Load(),
			test.Sprint("an assertion registered somebody against a deployment that declared its sign-up door closed"))
	})

	T.Run("a deployment refusing passwordless registration is held to the policy's refusal, and nobody else is registered without a password", func(t *testing.T) {
		t.Parallel()

		deployment := &passwordRequired{usernames: map[string]bool{}}

		seams := conformance.Seams{
			NewSubject: func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
				return &conformance.Subject{
					Scope:    tenancy.Global(),
					Conn:     deployment,
					Surfaces: conformance.Surfaces{SignIn: signinpb.NewSignInServiceClient(deployment)},
				}, nil
			},
			Anonymous: func(context.Context) (grpc.ClientConnInterface, error) {
				return deployment, nil
			},
			PasswordlessRegistrationRefused: true,
		}

		var inner *testing.T

		t.Run("run", func(t *testing.T) {
			conformance.Run(t, seams, conformance.Suite{
				Name: surface,
				Run: func(t *testing.T, s *conformance.Session) {
					t.Helper()

					inner = t
					passwordlessRegistration(t, s)
				},
			})
		})

		// The refusal assertion passed against the answer the server gives a
		// policy's refusal, and registered the same person with a password
		// after it. It is the only one that knocked: every assertion about
		// somebody with no password skipped before registering one.
		must.NotNil(t, inner)
		test.False(t, inner.Failed(), test.Sprint("the passwordless assertions failed against a deployment whose policy refuses a passwordless registration as the server does"))
		test.EqOp(t, int64(1), deployment.passwordless.Load(),
			test.Sprint("an assertion registered somebody with no password against a deployment that declared its policy refuses them"))
		test.EqOp(t, int64(1), deployment.withPassword.Load(),
			test.Sprint("the refused registrant was not registered once with a password afterwards"))
	})
}
