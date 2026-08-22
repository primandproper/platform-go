package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	errorsgrpc "github.com/primandproper/platform-go/v13/errors/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeServerStream is the minimum grpc.ServerStream the enforcer touches: it
// reads Context and nothing else.
type fakeServerStream struct {
	grpc.ServerStream

	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }

// SetHeader, SendHeader, SetTrailer, SendMsg and RecvMsg are inherited as nil
// and never called; the enforcer authorizes before the handler runs.
var _ grpc.ServerStream = (*fakeServerStream)(nil)

func grantsOf(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

func noGrants() authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.Grants{}, false
	}
}

// twoScopeGrants mirrors a real extractor: service-wide authority plus
// tenant-scoped authority, OR'd, with the tenant-scoped set absent.
func twoScopeGrants(service authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		var tenantScoped *authorization.PermissionSet

		return authorization.NewGrants(
			authorization.NewPermissionSet(service),
			tenantScoped,
		), true
	}
}

func testRequirements(t *testing.T) *Requirements {
	t.Helper()

	reqs, err := NewRequirements().
		Require(methodRead, permRead).
		Require(methodWrite, permRead, permWrite).
		Public(methodHealth).
		Build()
	must.NoError(t, err)

	return reqs
}

// enforcementCase is run against both the unary and the stream interceptor.
// That is the point of the table: the two paths cannot drift into subtly
// different rules if one table proves both.
type enforcementCase struct {
	extract     authorization.GrantsExtractor
	name        string
	method      string
	auditOnly   bool
	wantHandler bool
}

func enforcementCases() []enforcementCase {
	return []enforcementCase{
		{
			name:        "declared method with every permission granted",
			method:      methodRead,
			extract:     grantsOf(permRead),
			wantHandler: true,
		},
		{
			name:        "declared method with only a subset granted",
			method:      methodWrite,
			extract:     grantsOf(permRead),
			wantHandler: false,
		},
		{
			name:        "declared method with all of several permissions granted",
			method:      methodWrite,
			extract:     grantsOf(permRead, permWrite),
			wantHandler: true,
		},
		{
			name:        "permission granted through a second scope",
			method:      methodRead,
			extract:     twoScopeGrants(permRead),
			wantHandler: true,
		},
		{
			name:        "undeclared method is denied",
			method:      "/things.Things/Undeclared",
			extract:     grantsOf(permRead, permWrite),
			wantHandler: false,
		},
		{
			name:        "public method with no grants at all",
			method:      methodHealth,
			extract:     noGrants(),
			wantHandler: true,
		},
		{
			name:        "declared method with no grants available",
			method:      methodRead,
			extract:     noGrants(),
			wantHandler: false,
		},
		{
			name:        "audit-only lets a would-be denial through",
			method:      methodWrite,
			extract:     grantsOf(permRead),
			auditOnly:   true,
			wantHandler: true,
		},
		{
			name:        "audit-only still admits an allowed call",
			method:      methodRead,
			extract:     grantsOf(permRead),
			auditOnly:   true,
			wantHandler: true,
		},
		{
			name:        "audit-only lets an undeclared method through",
			method:      "/things.Things/Undeclared",
			extract:     grantsOf(permRead),
			auditOnly:   true,
			wantHandler: true,
		},
	}
}

func newTestEnforcer(t *testing.T, extract authorization.GrantsExtractor, auditOnly bool) *Enforcer {
	t.Helper()

	opts := []Option{}
	if auditOnly {
		opts = append(opts, WithAuditOnly())
	}

	e, err := NewEnforcer(testRequirements(t), extract, opts...)
	must.NoError(t, err)

	return e
}

func TestEnforcer_UnaryServerInterceptor(T *testing.T) {
	T.Parallel()

	for _, tc := range enforcementCases() {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := newTestEnforcer(t, tc.extract, tc.auditOnly)

			var called bool
			_, err := e.UnaryServerInterceptor()(
				t.Context(),
				nil,
				&grpc.UnaryServerInfo{FullMethod: tc.method},
				func(context.Context, any) (any, error) {
					called = true

					return "ok", nil
				},
			)

			test.EqOp(t, tc.wantHandler, called)
			if tc.wantHandler {
				test.NoError(t, err)
			} else {
				test.Error(t, err)
			}
		})
	}
}

func TestEnforcer_StreamServerInterceptor(T *testing.T) {
	T.Parallel()

	for _, tc := range enforcementCases() {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := newTestEnforcer(t, tc.extract, tc.auditOnly)

			var called bool
			err := e.StreamServerInterceptor()(
				nil,
				&fakeServerStream{ctx: t.Context()},
				&grpc.StreamServerInfo{FullMethod: tc.method},
				func(any, grpc.ServerStream) error {
					called = true

					return nil
				},
			)

			test.EqOp(t, tc.wantHandler, called)
			if tc.wantHandler {
				test.NoError(t, err)
			} else {
				test.Error(t, err)
			}
		})
	}
}

func TestEnforcer_DenialError(T *testing.T) {
	T.Parallel()

	denyOnce := func(t *testing.T) error {
		t.Helper()

		e := newTestEnforcer(t, noGrants(), false)
		_, err := e.UnaryServerInterceptor()(
			t.Context(),
			nil,
			&grpc.UnaryServerInfo{FullMethod: methodRead},
			func(context.Context, any) (any, error) { return nil, nil },
		)
		must.Error(t, err)

		return err
	}

	T.Run("matches the platform sentinel", func(t *testing.T) {
		t.Parallel()

		test.True(t, errors.Is(denyOnce(t), platformerrors.ErrPermissionDenied))
	})

	T.Run("also matches the package alias", func(t *testing.T) {
		t.Parallel()

		test.True(t, errors.Is(denyOnce(t), authorization.ErrPermissionDenied))
	})

	// The denial carries its own status, so the wire code is right even with no
	// error-encoding interceptor installed.
	T.Run("carries PermissionDenied without any other interceptor", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, codes.PermissionDenied, status.Code(denyOnce(t)))
	})

	// The security assertion: a caller that just failed to authorize must not
	// learn which permission it was missing.
	T.Run("does not leak the permission taxonomy", func(t *testing.T) {
		t.Parallel()

		msg := status.Convert(denyOnce(t)).Message()

		test.EqOp(t, "permission denied", msg)
		test.StrNotContains(t, msg, string(permRead))
		test.StrNotContains(t, msg, methodRead)
	})

	// The registered platform mapper is what turns a denial returned from
	// inside a handler into the right code, so it is worth proving directly.
	T.Run("the platform mapper maps the sentinel", func(t *testing.T) {
		t.Parallel()

		code, ok := errorsgrpc.PlatformMapper.Map(platformerrors.ErrPermissionDenied)

		test.True(t, ok)
		test.EqOp(t, codes.PermissionDenied, code)
	})

	T.Run("survives wrapping", func(t *testing.T) {
		t.Parallel()

		wrapped := platformerrors.Wrap(authorization.ErrPermissionDenied, "checking authority")

		test.True(t, errors.Is(wrapped, platformerrors.ErrPermissionDenied))
		test.EqOp(t, codes.PermissionDenied, errorsgrpc.MapToGRPC(wrapped, codes.Unknown))
	})
}

func TestNewEnforcer(T *testing.T) {
	T.Parallel()

	T.Run("rejects nil requirements", func(t *testing.T) {
		t.Parallel()

		_, err := NewEnforcer(nil, grantsOf(permRead))

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("rejects a nil extractor", func(t *testing.T) {
		t.Parallel()

		_, err := NewEnforcer(testRequirements(t), nil)

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(testRequirements(t), grantsOf(permRead), nil)

		test.NoError(t, err)
		test.NotNil(t, e)
	})
}

func TestEnforcer_ContextIsPassedThrough(T *testing.T) {
	T.Parallel()

	// The enforcer must not swallow request metadata on its way to the handler.
	T.Run("unary handler sees the original context", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("k", "v"))
		e := newTestEnforcer(t, grantsOf(permRead), false)

		var seen bool
		_, err := e.UnaryServerInterceptor()(
			ctx,
			nil,
			&grpc.UnaryServerInfo{FullMethod: methodRead},
			func(handlerCtx context.Context, _ any) (any, error) {
				md, ok := metadata.FromIncomingContext(handlerCtx)
				seen = ok && md.Get("k")[0] == "v"

				return nil, nil
			},
		)

		must.NoError(t, err)
		test.True(t, seen)
	})

	T.Run("stream handler sees the original stream", func(t *testing.T) {
		t.Parallel()

		stream := &fakeServerStream{ctx: t.Context()}
		e := newTestEnforcer(t, grantsOf(permRead), false)

		var got grpc.ServerStream
		err := e.StreamServerInterceptor()(
			nil,
			stream,
			&grpc.StreamServerInfo{FullMethod: methodRead},
			func(_ any, ss grpc.ServerStream) error {
				got = ss

				return nil
			},
		)

		must.NoError(t, err)
		test.EqOp(t, grpc.ServerStream(stream), got)
	})
}
