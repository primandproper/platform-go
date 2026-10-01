package grpc_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// nobody is an extractor that finds no caller on any request.
func nobody(context.Context) (callers.Principal, bool) { return nil, false }

// always is a PasswordChangeRequired with a fixed answer.
func always(owed bool, err error) signingrpc.PasswordChangeRequired {
	return func(context.Context, callers.Principal) (bool, error) { return owed, err }
}

// somebody is an extractor that finds the same caller on every request.
func somebody(context.Context) (callers.Principal, bool) {
	return &testPrincipal{userID: "someone", scope: testScope}, true
}

func TestNewPasswordChangeGate(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil extractor", func(t *testing.T) {
		t.Parallel()

		_, err := signingrpc.NewPasswordChangeGate(nil, always(false, nil))
		test.ErrorIs(t, err, signingrpc.ErrNilGateExtractor)
	})

	T.Run("refuses a nil reading", func(t *testing.T) {
		t.Parallel()

		_, err := signingrpc.NewPasswordChangeGate(nobody, nil)
		test.ErrorIs(t, err, signingrpc.ErrNilPasswordChangeRequired)
	})

	T.Run("allows the platform's methods, and the deployment's on top", func(t *testing.T) {
		t.Parallel()

		const own = "/consumer.v1.Profile/GetAvatar"

		gate, err := signingrpc.NewPasswordChangeGate(nobody, always(true, nil), signingrpc.WithAllowedMethods(own))
		must.NoError(t, err)

		for _, method := range signingrpc.PasswordChangeMethods() {
			test.True(t, gate.Allows(method), test.Sprintf("%s is not allowed", method))
		}

		test.True(t, gate.Allows(own))
		test.False(t, gate.Allows(signinpb.SignInService_RefreshTOTPSecret_FullMethodName))
	})
}

func TestPasswordChangeMethods(T *testing.T) {
	T.Parallel()

	methods := signingrpc.PasswordChangeMethods()

	// The ones the ruling named, each for the reason on the list: how a client
	// is told, the change itself, and the remedy for a credential somebody else
	// holds.
	for _, method := range []string{
		signinpb.SignInService_UpdatePassword_FullMethodName,
		signinpb.SignInService_GetAuthStatus_FullMethodName,
		signinpb.SignInService_GetSelf_FullMethodName,
		signinpb.SignInService_SignOut_FullMethodName,
		signinpb.SignInService_SignOutEverywhere_FullMethodName,
		identitypb.IdentityService_GetPrincipal_FullMethodName,
		passwordresetpb.PasswordResetService_CompletePasswordReset_FullMethodName,
	} {
		test.SliceContains(T, methods, method)
	}

	// Every door but the sign-up one, since what it presents is its own
	// authority.
	for _, method := range signingrpc.AnonymousMethods() {
		if method != signinpb.SignInService_Register_FullMethodName {
			test.SliceContains(T, methods, method)
		}
	}

	// And not the changes a stolen password is used for first.
	for _, method := range []string{
		signinpb.SignInService_RefreshTOTPSecret_FullMethodName,
		signinpb.SignInService_VerifyTOTPSecret_FullMethodName,
		signinpb.SignInService_Register_FullMethodName,
	} {
		test.False(T, slices.Contains(methods, method), test.Sprintf("%s is allowed", method))
	}
}

func TestDirectoryPasswordChange(T *testing.T) {
	T.Parallel()

	T.Run("refuses nil dependencies", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)

		_, err := signingrpc.DirectoryPasswordChange(nil, h.store)
		test.ErrorIs(t, err, signingrpc.ErrNilPasswordChangeClient)

		_, err = signingrpc.DirectoryPasswordChange(h.db, nil)
		test.ErrorIs(t, err, signingrpc.ErrNilPasswordChangeDirectory)
	})

	T.Run("reads a principal that carries no flag from the directory", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		required, err := signingrpc.DirectoryPasswordChange(h.db, h.store)
		must.NoError(t, err)

		principal := &testPrincipal{userID: h.member.User.ID, scope: testScope}

		owed, err := required(t.Context(), principal)
		must.NoError(t, err)
		test.False(t, owed)

		flag(t, h, true)

		owed, err = required(t.Context(), principal)
		must.NoError(t, err)
		test.True(t, owed)
	})

	T.Run("a principal the directory does not hold owes nothing", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		required, err := signingrpc.DirectoryPasswordChange(h.db, h.store)
		must.NoError(t, err)

		owed, err := required(t.Context(), &testPrincipal{userID: "nobody-by-that-id", scope: testScope})
		must.NoError(t, err)
		test.False(t, owed)
	})

	T.Run("reports a directory that cannot answer", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		required, err := signingrpc.DirectoryPasswordChange(h.db, brokenDirectory{})
		must.NoError(t, err)

		_, err = required(t.Context(), &testPrincipal{userID: h.member.User.ID, scope: testScope})
		test.Error(t, err)
	})
}

// flag imposes or releases the member's forced password change, as an
// operator's write does.
func flag(t *testing.T, h *extractorHarness, requires bool) {
	t.Helper()

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		return h.store.SetUserRequiresPasswordChange(t.Context(), tx, testScope, h.member.User.ID, requires)
	}))
}

func TestPasswordChangeGate_interceptors(T *testing.T) {
	T.Parallel()

	// dial serves the sign-in surface behind the chain a deployment builds: the
	// error encoder and the authentication interceptor, whose gate is on
	// unless opts say otherwise.
	dial := func(t *testing.T, h *extractorHarness, opts ...signingrpc.ExtractorOption) *signinclient.Client {
		t.Helper()

		e := h.extractor(t, opts...)

		reqs, err := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).Build()
		must.NoError(t, err)

		srv, err := signingrpc.NewServer(h.svc, e.Extract,
			signingrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) { return testScope, nil }))
		must.NoError(t, err)

		grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
			grpcerrors.UnaryErrorEncodingInterceptor(),
			e.UnaryServerInterceptor(reqs),
		))
		srv.RegisterOn(grpcServer)

		listener := bufconn.Listen(1 << 20)

		go func() { _ = grpcServer.Serve(listener) }()

		t.Cleanup(grpcServer.Stop)

		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			signinclient.DefaultInterceptors(),
		)
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		return signinclient.Wrap(conn)
	}

	bearer := func(ctx context.Context, token string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}

	T.Run("a flagged caller is held at the form until the flag clears", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		client := dial(t, h)
		ctx := bearer(t.Context(), h.issue(t, h.member, false).Token)

		// The control: an ordinary call, before anything is owed.
		_, err := client.ListSignIns(ctx, &signinpb.ListSignInsRequest{})
		must.NoError(t, err)

		flag(t, h, true)

		_, err = client.VerifyTOTPSecret(ctx, &signinpb.VerifyTOTPSecretRequest{TotpCode: "000000"})
		must.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.EqOp(t, signin.ErrPasswordChangeRequired.Error(), status.Convert(err).Message())

		info, ok := grpcerrors.ClientReasonFromStatus(err)
		must.True(t, ok, must.Sprint("the refusal carried no reason to branch on"))
		test.EqOp(t, "PASSWORD_CHANGE_REQUIRED", info.GetReason())
		test.EqOp(t, signin.ClientReasonDomain, info.GetDomain())

		// The calls that discharge it, and the ones that tell the client to.
		self, err := client.GetSelf(ctx, &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, h.member.User.ID, self.GetUser().GetId())

		answer, err := client.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, answer.GetStatus().GetRequiresPasswordChange())

		flag(t, h, false)

		// Refused by the handler now, for its own reason, which is the gate
		// having stepped aside.
		_, err = client.VerifyTOTPSecret(ctx, &signinpb.VerifyTOTPSecretRequest{TotpCode: "000000"})
		test.NotEqOp(t, "PASSWORD_CHANGE_REQUIRED", reasonOf(err))
	})

	T.Run("an extractor without the gate lets a flagged caller through", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		client := dial(t, h, signingrpc.WithoutPasswordChangeGate())
		ctx := bearer(t.Context(), h.issue(t, h.member, false).Token)

		flag(t, h, true)

		_, err := client.ListSignIns(ctx, &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
	})

	T.Run("a request with nobody on it is not the gate's", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)

		_, err := dial(t, h).LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "member", Password: "wrong"},
		})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

// reasonOf is the reason a refusal carried, or empty.
func reasonOf(err error) string {
	info, ok := grpcerrors.ClientReasonFromStatus(err)
	if !ok {
		return ""
	}

	return info.GetReason()
}

func TestPasswordChangeGate_directInterceptors(T *testing.T) {
	T.Parallel()

	const ordinary = "/consumer.v1.Things/ListThings"

	unary := func(t *testing.T, gate *signingrpc.PasswordChangeGate, method string) (bool, error) {
		t.Helper()

		reached := false
		_, err := gate.UnaryServerInterceptor()(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(context.Context, any) (any, error) {
				reached = true

				return nil, nil
			})

		return reached, err
	}

	T.Run("nobody passes whatever the reading would say", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(nobody, always(true, platformerrors.New("never asked")))
		must.NoError(t, err)

		reached, err := unary(t, gate, ordinary)
		must.NoError(t, err)
		test.True(t, reached)
	})

	T.Run("a caller who owes nothing passes", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(false, nil))
		must.NoError(t, err)

		reached, err := unary(t, gate, ordinary)
		must.NoError(t, err)
		test.True(t, reached)
	})

	T.Run("a deployment's own allowed method passes a flagged caller", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(true, nil), signingrpc.WithAllowedMethods(ordinary))
		must.NoError(t, err)

		reached, err := unary(t, gate, ordinary)
		must.NoError(t, err)
		test.True(t, reached)
	})

	T.Run("a flagged caller is refused before the handler", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(true, nil))
		must.NoError(t, err)

		reached, err := unary(t, gate, ordinary)
		test.False(t, reached)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.ErrorIs(t, err, signin.ErrPasswordChangeRequired)
	})

	T.Run("a reading that fails is unavailable rather than a guess", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(false, platformerrors.New("directory down")))
		must.NoError(t, err)

		reached, err := unary(t, gate, ordinary)
		test.False(t, reached)
		test.EqOp(t, codes.Unavailable, status.Code(err))
	})

	T.Run("a stream is gated the same way", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(true, nil))
		must.NoError(t, err)

		reached := false
		err = gate.StreamServerInterceptor()(nil, &contextStream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: ordinary},
			func(any, grpc.ServerStream) error {
				reached = true

				return nil
			})
		test.False(t, reached)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}

// contextStream is a server stream that is only a context.
type contextStream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // a ServerStream's context is a field by gRPC's design.
}

func (s *contextStream) Context() context.Context { return s.ctx }

func TestPasswordChangeGate_HTTPMiddleware(T *testing.T) {
	T.Parallel()

	serve := func(t *testing.T, gate *signingrpc.PasswordChangeGate, allow func(*http.Request) bool) (*httptest.ResponseRecorder, bool) {
		t.Helper()

		reached := false
		handler := gate.HTTPMiddleware(allow)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true

			w.WriteHeader(http.StatusNoContent)
		}))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things", http.NoBody))

		return res, reached
	}

	T.Run("a flagged caller is a 403 in the platform's envelope", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(true, nil))
		must.NoError(t, err)

		res, reached := serve(t, gate, nil)
		test.False(t, reached)
		must.EqOp(t, http.StatusForbidden, res.Code)

		var body httperrors.APIResponse[any]
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		must.NotNil(t, body.Error)
		test.EqOp(t, httperrors.ErrUserIsNotAuthorized, body.Error.Code)

		// The same answer the registered mapper gives, since nothing else may
		// say what this refusal is.
		code, msg, ok := signin.HTTPMapper.Map(signin.ErrPasswordChangeRequired)
		must.True(t, ok)
		test.EqOp(t, code, body.Error.Code)
		test.EqOp(t, msg, body.Error.Message)
	})

	T.Run("a request the deployment allows passes a flagged caller", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(true, nil))
		must.NoError(t, err)

		_, reached := serve(t, gate, func(*http.Request) bool { return true })
		test.True(t, reached)
	})

	T.Run("nobody, and a caller who owes nothing, pass", func(t *testing.T) {
		t.Parallel()

		for _, extract := range []callers.PrincipalExtractor{nobody, somebody} {
			gate, err := signingrpc.NewPasswordChangeGate(extract, always(false, nil))
			must.NoError(t, err)

			_, reached := serve(t, gate, nil)
			test.True(t, reached)
		}

		// Nobody passes even where the reading would have said owed, since it
		// is never asked.
		gate, err := signingrpc.NewPasswordChangeGate(nobody, always(true, nil))
		must.NoError(t, err)

		_, reached := serve(t, gate, nil)
		test.True(t, reached)
	})

	T.Run("a reading that fails is a 503", func(t *testing.T) {
		t.Parallel()

		gate, err := signingrpc.NewPasswordChangeGate(somebody, always(false, platformerrors.New("directory down")))
		must.NoError(t, err)

		res, reached := serve(t, gate, nil)
		test.False(t, reached)
		must.EqOp(t, http.StatusServiceUnavailable, res.Code)
		test.StrContains(t, res.Header().Get("Content-Type"), "json")

		// The same envelope the 403 answers in, not a plain-text body, and one
		// that names neither the refusal nor the directory's own error.
		var body httperrors.APIResponse[any]
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		must.NotNil(t, body.Error)
		test.EqOp(t, httperrors.ErrNothingSpecific, body.Error.Code)
		test.StrNotContains(t, body.Error.Message, "directory down")
	})
}

func TestPrincipalExtractor_passwordChangeGate(T *testing.T) {
	T.Parallel()

	const ownMethod = "/consumer.v1.Profile/GetAvatar"

	reqs, reqsErr := signingrpc.NewAuthenticationRequirements().
		Declare(signingrpc.AuthenticationRequired, ownMethod).
		Build()
	must.NoError(T, reqsErr)

	// call runs ownMethod through e's unary interceptor as h's member,
	// reporting whether the handler was reached.
	call := func(t *testing.T, h *extractorHarness, e *signingrpc.PrincipalExtractor) (bool, error) {
		t.Helper()

		ctx := metadata.NewIncomingContext(t.Context(),
			metadata.Pairs("authorization", "Bearer "+h.issue(t, h.member, false).Token))

		reached := false
		_, callErr := e.UnaryServerInterceptor(reqs)(ctx, nil, &grpc.UnaryServerInfo{FullMethod: ownMethod},
			func(context.Context, any) (any, error) {
				reached = true

				return nil, nil
			})

		return reached, callErr
	}

	// serve runs one request through e's HTTP middleware as h's member.
	serve := func(t *testing.T, h *extractorHarness, e *signingrpc.PrincipalExtractor) (*httptest.ResponseRecorder, bool) {
		t.Helper()

		reached := false
		handler := e.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true

			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/avatar", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+h.issue(t, h.member, false).Token)

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)

		return res, reached
	}

	T.Run("is on by default, for a deployment's own methods too", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		e := h.extractor(t)

		reached, err := call(t, h, e)
		must.NoError(t, err)
		test.True(t, reached)

		flag(t, h, true)

		reached, err = call(t, h, e)
		test.False(t, reached)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.ErrorIs(t, err, signin.ErrPasswordChangeRequired)
	})

	T.Run("passes a method the deployment allows", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		e := h.extractor(t, signingrpc.WithPasswordChangeAllowedMethods(ownMethod))

		flag(t, h, true)

		reached, err := call(t, h, e)
		must.NoError(t, err)
		test.True(t, reached)
	})

	T.Run("reads the flag the way the deployment names", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		e := h.extractor(t, signingrpc.WithPasswordChangeRequired(always(true, nil)))

		reached, err := call(t, h, e)
		test.False(t, reached)
		test.ErrorIs(t, err, signin.ErrPasswordChangeRequired)
	})

	T.Run("holds an HTTP request too, unless the deployment allows it", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		flag(t, h, true)

		res, reached := serve(t, h, h.extractor(t))
		test.False(t, reached)
		test.EqOp(t, http.StatusForbidden, res.Code)

		_, reached = serve(t, h, h.extractor(t,
			signingrpc.WithPasswordChangeAllowedRequests(func(r *http.Request) bool { return r.URL.Path == "/avatar" })))
		test.True(t, reached)

		_, reached = serve(t, h, h.extractor(t, signingrpc.WithoutPasswordChangeGate()))
		test.True(t, reached)
	})

	T.Run("refuses a fallback it has no way to read the flag of", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		directory := principalsOnly{h.store}

		_, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, directory, signingrpc.WithFallback(somebody))
		test.ErrorIs(t, err, signingrpc.ErrNoPasswordChangeReading)

		// Each of the three ways out builds.
		for _, opts := range [][]signingrpc.ExtractorOption{
			{signingrpc.WithFallback(somebody), signingrpc.WithPasswordChangeRequired(always(false, nil))},
			{signingrpc.WithFallback(somebody), signingrpc.WithoutPasswordChangeGate()},
			nil,
		} {
			_, err = signingrpc.NewPrincipalExtractor(h.signer, h.db, directory, opts...)
			test.NoError(t, err)
		}
	})

	T.Run("reads a directory that cannot read users off the caller it resolved", func(t *testing.T) {
		t.Parallel()

		h := newExtractorHarness(t)
		e, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, principalsOnly{h.store})
		must.NoError(t, err)

		flag(t, h, true)

		reached, err := call(t, h, e)
		test.False(t, reached)
		test.ErrorIs(t, err, signin.ErrPasswordChangeRequired)
	})
}

// principalsOnly is a directory that can resolve principals and nothing else,
// as a deployment's own directory might.
type principalsOnly struct {
	directory signingrpc.PrincipalDirectory
}

func (d principalsOnly) GetPrincipal(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (*identity.Principal, error) {
	return d.directory.GetPrincipal(ctx, q, scope, userID, activeAccountID)
}

// brokenDirectory is a directory that cannot be read.
type brokenDirectory struct{}

func (brokenDirectory) GetUser(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) (*identity.User, error) {
	return nil, platformerrors.New("directory down")
}
