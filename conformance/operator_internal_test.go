package conformance

import (
	"context"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v15/billing/billingpb"
	"github.com/primandproper/platform-go/v15/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// minting is a session whose factory answers every request with a caller that
// says what it was asked for, reserving reserved and minting an administrator
// only where admins says it can. A tenant named for a surface is that surface's
// alone, so a caller asked into one reads it back only through ScopeFor.
func minting(admins bool, reserved ...string) *Session {
	return &Session{seams: Seams{
		OperatorMethods: reserved,
		NewSubject: func(_ context.Context, opts ...SubjectOption) (*Subject, error) {
			req := NewSubjectRequest(opts...)
			if req.Admin && !admins {
				return nil, ErrSubjectUnsupported
			}

			sub := &Subject{Scope: tenancy.Of("fresh")}
			if req.Scope != nil {
				sub.Scopes = map[string]tenancy.Scope{req.Surface: *req.Scope}
			}

			if req.Admin {
				sub.UserID = "admin"
			}

			return sub, nil
		},
	}}
}

// skipped runs mint in a subtest and reports whether it skipped. Reaching the
// end of mint is an error, since every case asking this expects the mint to
// skip.
func skipped(t *testing.T, mint func(t *testing.T)) bool {
	t.Helper()

	var inner *testing.T

	t.Run("mint", func(t *testing.T) {
		inner = t

		mint(t)
		t.Error("the mint returned where it should have skipped")
	})

	return inner.Skipped()
}

// minted runs mint in a subtest and fails where it skipped, for the cases
// whose assertions would otherwise pass by never being reached.
func minted(t *testing.T, mint func(t *testing.T)) {
	t.Helper()

	var inner *testing.T

	t.Run("mint", func(t *testing.T) {
		inner = t

		mint(t)
	})

	test.False(t, inner.Skipped(), test.Sprint("the mint skipped where it should have minted"))
}

func TestSession_Subject_routesByReservation(T *testing.T) {
	T.Parallel()

	create, list := billingpb.BillingService_CreateProduct_FullMethodName, billingpb.BillingService_ListProducts_FullMethodName

	T.Run("a subject that reserves none of the methods answers an ordinary caller", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", minting(true).Subject(t, Making(create, list)).UserID)
	})

	T.Run("a subject that reserves one of the methods answers an administrator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "admin", minting(true, list).Subject(t, Making(create, list)).UserID)
	})

	T.Run("any method may be reserved", func(t *testing.T) {
		t.Parallel()

		mark := notificationspb.NotificationsService_ListNotifications_FullMethodName

		test.EqOp(t, "admin", minting(true, mark).Subject(t, Making(mark)).UserID)
	})

	T.Run("either caller is put in the tenant named on the surface named", func(t *testing.T) {
		t.Parallel()

		account := tenancy.Of("account")

		test.EqOp(t, account, minting(true).Subject(t, Making(create), InTenant("billing", account)).ScopeFor("billing"))
		test.EqOp(t, account, minting(true, create).Subject(t, Making(create), InTenant("billing", account)).ScopeFor("billing"))
	})

	T.Run("a member making nothing reserved is an ordinary caller", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", minting(true, list).Subject(t, Making(create), AsMember()).UserID)
	})

	T.Run("a subject that reserves a method and mints no administrator skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(false, create).Subject(t, Making(create))
		}))
	})

	T.Run("a member asked to make a reserved method skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(true, create).Subject(t, Making(create), AsMember())
		}))
	})
}

func TestAttempting(T *testing.T) {
	T.Parallel()

	create, list := billingpb.BillingService_CreateProduct_FullMethodName, billingpb.BillingService_ListProducts_FullMethodName

	T.Run("a caller attempting a reserved call is a member", func(t *testing.T) {
		t.Parallel()

		minted(t, func(t *testing.T) {
			t.Helper()

			test.EqOp(t, "", minting(true, create).Subject(t, Attempting(create)).UserID)
		})
	})

	T.Run("the factory is handed the attempted call among the methods, and asked for no administrator", func(t *testing.T) {
		t.Parallel()

		var asked *SubjectRequest

		s := &Session{seams: Seams{
			OperatorMethods: []string{create},
			NewSubject: func(_ context.Context, opts ...SubjectOption) (*Subject, error) {
				asked = NewSubjectRequest(opts...)

				return &Subject{}, nil
			},
		}}

		minted(t, func(t *testing.T) {
			t.Helper()

			s.Subject(t, Attempting(create), Making(list))
		})

		must.NotNil(t, asked)
		test.SliceContainsAll(t, []string{create, list}, asked.Methods)
		test.False(t, asked.Admin)
	})

	T.Run("the attempted call is admitted on the caller's connection", func(t *testing.T) {
		t.Parallel()

		inner := &answering{}
		s := &Session{seams: Seams{
			OperatorMethods: []string{create},
			NewSubject: func(context.Context, ...SubjectOption) (*Subject, error) {
				return &Subject{Conn: inner, Surfaces: Surfaces{Billing: billingpb.NewBillingServiceClient(inner)}}, nil
			},
		}}

		minted(t, func(t *testing.T) {
			t.Helper()

			sub := s.Subject(t, Attempting(create))

			test.NoError(t, sub.Conn.Invoke(t.Context(), create, nil, nil))
			test.Eq(t, []string{create}, inner.calls)
		})
	})

	T.Run("a member attempting one reserved call and making another skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(true, create, list).Subject(t, Attempting(create), Making(list))
		}))
	})
}

func TestSession_Reserves(t *testing.T) {
	t.Parallel()

	s := minting(true, billingpb.BillingService_ListProducts_FullMethodName)

	test.True(t, s.Reserves(billingpb.BillingService_ListProducts_FullMethodName))
	test.False(t, s.Reserves(billingpb.BillingService_GetProduct_FullMethodName))
}

func TestIsFullMethodName(t *testing.T) {
	t.Parallel()

	test.True(t, isFullMethodName(billingpb.BillingService_ListProducts_FullMethodName))
	test.True(t, isFullMethodName("/consumer.articles.v1.ArticlesService/DeleteArticle"),
		test.Sprint("a method on a service no suite covers is the deployment's own to reserve"))

	for _, spelled := range []string{
		"",
		"ListProducts",
		"billing.v1.BillingService/ListProducts",
		"/BillingService/ListProducts",
		"/billing.v1.BillingService/",
		"/billing.v1.BillingService/List/Products",
	} {
		test.False(t, isFullMethodName(spelled), test.Sprintf("%q", spelled))
	}
}

// recorder is the half of testing.T a declaredConn reports through, keeping
// what it was told.
type recorder struct{ errors []string }

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// answering is a connection that answers every call, so what a declaredConn in
// front of it refuses is the declaredConn's doing.
type answering struct{ calls []string }

func (a *answering) Invoke(_ context.Context, method string, _, _ any, _ ...grpc.CallOption) error {
	a.calls = append(a.calls, method)

	return nil
}

func (a *answering) NewStream(_ context.Context, _ *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	a.calls = append(a.calls, method)

	return nil, nil
}

func TestDeclaredConn(T *testing.T) {
	T.Parallel()

	create, list := billingpb.BillingService_CreateProduct_FullMethodName, billingpb.BillingService_ListProducts_FullMethodName

	T.Run("a declared call reaches the connection", func(t *testing.T) {
		t.Parallel()

		inner, told := &answering{}, &recorder{}
		conn := &declaredConn{ClientConnInterface: inner, t: told, declared: []string{create}}

		test.NoError(t, conn.Invoke(t.Context(), create, nil, nil))
		test.Eq(t, []string{create}, inner.calls)
		test.SliceEmpty(t, told.errors)
	})

	T.Run("an undeclared call fails the test, names the call and the fix, and reaches nothing", func(t *testing.T) {
		t.Parallel()

		inner, told := &answering{}, &recorder{}
		conn := &declaredConn{ClientConnInterface: inner, t: told, declared: []string{create}}

		err := conn.Invoke(t.Context(), list, nil, nil)
		test.Error(t, err)
		test.SliceEmpty(t, inner.calls)
		must.SliceLen(t, 1, told.errors)
		test.StrContains(t, told.errors[0], list)
		test.StrContains(t, told.errors[0], "conformance.Making")
	})

	T.Run("a stream is held to the same declaration", func(t *testing.T) {
		t.Parallel()

		inner, told := &answering{}, &recorder{}
		conn := &declaredConn{ClientConnInterface: inner, t: told}

		_, err := conn.NewStream(t.Context(), &grpc.StreamDesc{}, list)
		test.Error(t, err)
		test.SliceEmpty(t, inner.calls)
		must.SliceLen(t, 1, told.errors)
		test.StrContains(t, told.errors[0], "declared nothing")
	})
}

func TestDeclare(T *testing.T) {
	T.Parallel()

	T.Run("the surfaces a subject mounts are rebuilt over its checked connection", func(t *testing.T) {
		t.Parallel()

		inner := &answering{}
		sub := &Subject{Conn: inner, Surfaces: Surfaces{Billing: billingpb.NewBillingServiceClient(inner)}}

		checked := declare(t, sub, []string{billingpb.BillingService_ListProducts_FullMethodName})

		_, err := checked.Surfaces.Billing.ListProducts(t.Context(), &billingpb.ListProductsRequest{})
		test.NoError(t, err)
		test.Nil(t, checked.Surfaces.Audit, test.Sprint("a surface the subject did not mount was mounted"))
		test.Eq(t, []string{billingpb.BillingService_ListProducts_FullMethodName}, inner.calls)
	})

	T.Run("a subject mounting no gRPC surface needs no connection", func(t *testing.T) {
		t.Parallel()

		sub := &Subject{}
		test.EqOp(t, sub, declare(t, sub, nil))
	})
}

func TestSession_NeedsPublic(T *testing.T) {
	T.Parallel()

	door := notificationspb.NotificationsService_ListNotifications_FullMethodName

	T.Run("a door the subject leaves open is reached", func(t *testing.T) {
		t.Parallel()

		minting(true).NeedsPublic(t, door)
	})

	T.Run("a door the subject reserves skips", func(t *testing.T) {
		t.Parallel()

		test.True(t, skipped(t, func(t *testing.T) {
			t.Helper()

			minting(true, door).NeedsPublic(t, door)
		}))
	})
}
