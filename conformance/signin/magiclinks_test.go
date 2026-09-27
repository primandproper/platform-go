package signin

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// noMagicLinkStore is a deployment that mounts sign-in with no magic-link
// store: it registers whoever it is asked to, and its passwordless doors
// answer Internal, as signin's do when they were never configured. It counts
// every knock on those doors.
type noMagicLinkStore struct {
	knocks atomic.Int64
}

var _ grpc.ClientConnInterface = (*noMagicLinkStore)(nil)

func (d *noMagicLinkStore) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	switch method {
	case signinpb.SignInService_Register_FullMethodName:
		out, ok := reply.(proto.Message)
		if !ok {
			return status.Error(codes.Internal, "not a message")
		}

		proto.Merge(out, &signinpb.RegisterResponse{Registration: &signinpb.Registered{
			User:    &identitypb.User{Id: identifiers.New()},
			Account: &identitypb.Account{Id: identifiers.New()},
		}})

		return nil
	case requestMagicLink, redeemMagicLink:
		d.knocks.Add(1)

		return status.Error(codes.Internal, "internal error")
	default:
		return status.Errorf(codes.Unimplemented, "%s is not part of this deployment", method)
	}
}

func (*noMagicLinkStore) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams")
}

func TestMagicLinks(T *testing.T) {
	T.Parallel()

	T.Run("a deployment that mails no sign-in links skips every magic-link assertion", func(t *testing.T) {
		t.Parallel()

		deployment := &noMagicLinkStore{}

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
			// And no MagicLinkToken, which is what a deployment with no
			// magic-link store supplies.
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
					magicLinks(t, s)
				},
			})
		})

		must.NotNil(t, inner)
		test.True(t, inner.Skipped(), test.Sprint("the magic-link assertions ran against a deployment that mails no sign-in links"))
		test.EqOp(t, int64(0), deployment.knocks.Load())
	})
}
