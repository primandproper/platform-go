package grpc_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"
	refreshmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/tokens/jwt"
	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
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

// serviceAdminRole is the service role the extractor suite's administrator
// holds, and the one the sign-in service's administrative door admits.
const serviceAdminRole = "service_admin"

// memberGrant and operatorGrant are the two permissions the suite's role policy
// hands out: every caller holds the first, and a caller whose principal still
// carries serviceAdminRole holds the second as well.
const (
	memberGrant   authorization.Permission = "read.own"
	operatorGrant authorization.Permission = "ban.anybody"
)

// rolePolicy is the consumer's role→permission policy, as small as it can be
// and still tell an operator from a member.
func rolePolicy(_ context.Context, principal callers.Principal) (authorization.Grants, error) {
	perms := []authorization.Permission{memberGrant}

	if caller, ok := principal.(*signingrpc.Caller); ok && slices.Contains(caller.Identity().ServiceRoles(), serviceAdminRole) {
		perms = append(perms, operatorGrant)
	}

	return authorization.NewGrants(authorization.NewPermissionSet(perms...)), nil
}

// extractorHarness is one database, a real JWT signer, a sign-in service that
// mints with it, and an extractor that reads with it.
type extractorHarness struct {
	db     database.Client
	store  identity.Store
	signer *jwt.Signer
	svc    *signin.Service

	member *identity.Registration
	admin  *identity.Registration
}

func newExtractorHarness(t *testing.T) *extractorHarness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "extractor.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("ex_%d", prefixCounter.Add(1))

	for _, render := range []func(dialect.Dialect, string) ([]string, error){
		migrations.Statements, refreshmigrations.Statements,
	} {
		stmts, stmtErr := render(dialect.SQLite, prefix)
		must.NoError(t, stmtErr)

		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}
	}

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	refreshStore, err := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: prefix}, db)
	must.NoError(t, err)

	signer, err := jwt.NewSigner("extractor", "extractor", []byte("a-signing-key-for-the-extractor-suite"))
	must.NoError(t, err)

	svc, err := signin.NewService(db, store, argon2.NewArgon2Authenticator(), signer,
		signin.WithAdminServiceRoles(serviceAdminRole),
		signin.WithRefreshTokenStore(refreshStore),
	)
	must.NoError(t, err)

	directory, err := identity.NewService(db, store)
	must.NoError(t, err)

	register := func(name string, serviceRoles ...string) *identity.Registration {
		reg, regErr := directory.Register(t.Context(), testScope,
			&identity.User{
				Username:      name,
				EmailAddress:  name + "@example.com",
				AccountStatus: identity.StatusGood,
				Scope:         testScope,
				ServiceRoles:  serviceRoles,
			},
			&identity.Account{Name: name + "'s", Scope: testScope},
			[]string{"owner"},
		)
		must.NoError(t, regErr)

		return reg
	}

	return &extractorHarness{
		db:     db,
		store:  store,
		signer: signer,
		svc:    svc,
		member: register("member"),
		admin:  register("operator", serviceAdminRole),
	}
}

// extractor builds one over the harness's signer and directory.
func (h *extractorHarness) extractor(t *testing.T, opts ...signingrpc.ExtractorOption) *signingrpc.PrincipalExtractor {
	t.Helper()

	e, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, h.store, opts...)
	must.NoError(t, err)

	return e
}

// issue mints a token for reg through the ordinary door, or the
// administrative one.
func (h *extractorHarness) issue(t *testing.T, reg *identity.Registration, administrative bool) *signin.SignIn {
	t.Helper()

	door := h.svc.IssueForPrincipal
	if administrative {
		door = h.svc.AdminIssueForPrincipal
	}

	issued, err := door(t.Context(), testScope, reg.User.ID, reg.Account.ID)
	must.NoError(t, err)

	return issued
}

func TestNewPrincipalExtractor(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	T.Run("refuses each missing dependency by name", func(t *testing.T) {
		t.Parallel()

		_, err := signingrpc.NewPrincipalExtractor(nil, h.db, h.store)
		test.ErrorIs(t, err, signingrpc.ErrNilTokenVerifier)

		_, err = signingrpc.NewPrincipalExtractor(h.signer, nil, h.store)
		test.ErrorIs(t, err, signingrpc.ErrNilExtractorClient)

		_, err = signingrpc.NewPrincipalExtractor(h.signer, h.db, nil)
		test.ErrorIs(t, err, signingrpc.ErrNilPrincipalDirectory)
	})
}

func TestPrincipalExtractor_Authenticate(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	T.Run("a minted token is the caller it was minted for, in the login it began", func(t *testing.T) {
		t.Parallel()

		issued := h.issue(t, h.member, false)

		caller, err := h.extractor(t).Authenticate(t.Context(), issued.Token)
		must.NoError(t, err)

		test.EqOp(t, h.member.User.ID, caller.UserID())
		test.EqOp(t, h.member.Account.ID, caller.ActiveAccountID())
		test.EqOp(t, testScope, caller.Scope())
		test.EqOp(t, issued.FamilyID, caller.FamilyID())
		test.EqOp(t, issued.TokenID, caller.TokenID())
		test.False(t, caller.Administrative())
	})

	T.Run("an ordinary-door token of a service admin carries none of their service roles", func(t *testing.T) {
		t.Parallel()

		e := h.extractor(t, signingrpc.WithGrants(rolePolicy))

		caller, err := e.Authenticate(t.Context(), h.issue(t, h.admin, false).Token)
		must.NoError(t, err)

		test.SliceEmpty(t, caller.Identity().ServiceRoles())
		test.SliceNotContains(t, caller.Identity().Roles(), serviceAdminRole)

		// And the role policy, reading the principal as any policy would, grants
		// a member's permissions and nothing of an operator's.
		grants, ok := e.Grants(t.Context())
		test.False(t, ok, test.Sprint("a context nothing resolved has no grants"))
		test.True(t, grants.IsEmpty())

		saw := serveThrough(t, e, "Bearer "+h.issue(t, h.admin, false).Token)
		must.True(t, saw.granted)
		test.True(t, saw.grants.Has(memberGrant))
		test.False(t, saw.grants.Has(operatorGrant))

		// The directory still holds the role; only the request's copy lacks it.
		user, err := h.store.GetUser(t.Context(), h.db.Reader(), testScope, h.admin.User.ID)
		must.NoError(t, err)
		test.Eq(t, []string{serviceAdminRole}, user.ServiceRoles)
	})

	T.Run("an administrative token carries them", func(t *testing.T) {
		t.Parallel()

		e := h.extractor(t, signingrpc.WithGrants(rolePolicy))

		caller, err := e.Authenticate(t.Context(), h.issue(t, h.admin, true).Token)
		must.NoError(t, err)

		test.True(t, caller.Administrative())
		test.Eq(t, []string{serviceAdminRole}, caller.Identity().ServiceRoles())

		grants, err := rolePolicy(t.Context(), caller)
		must.NoError(t, err)
		test.True(t, grants.Has(operatorGrant))
	})

	T.Run("an ordinary-door token keeps what the consumer's seam says it keeps", func(t *testing.T) {
		t.Parallel()

		e := h.extractor(t, signingrpc.WithOrdinaryServiceRoles(func(_ context.Context, roles []string) []string {
			return roles
		}))

		caller, err := e.Authenticate(t.Context(), h.issue(t, h.admin, false).Token)
		must.NoError(t, err)

		test.Eq(t, []string{serviceAdminRole}, caller.Identity().ServiceRoles())
	})

	T.Run("a token that does not verify names nobody", func(t *testing.T) {
		t.Parallel()

		_, err := h.extractor(t).Authenticate(t.Context(), "not-a-token")
		test.ErrorIs(t, err, signingrpc.ErrUnauthenticated)
	})

	T.Run("a verified token without the sign-in claims is not a sign-in token", func(t *testing.T) {
		t.Parallel()

		token, _, err := h.signer.IssueToken(t.Context(), h.member.User.ID, time.Minute, nil)
		must.NoError(t, err)

		_, err = h.extractor(t).Authenticate(t.Context(), token)
		test.ErrorIs(t, err, signingrpc.ErrNotASignInToken)
		test.ErrorIs(t, err, signingrpc.ErrUnauthenticated)
	})

	T.Run("a banned user's token names nobody, and says why", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)
		issued := fresh.issue(t, fresh.member, false)

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		_, err := fresh.extractor(t).Authenticate(t.Context(), issued.Token)
		test.ErrorIs(t, err, signingrpc.ErrUnauthenticated)
		test.ErrorIs(t, err, identity.ErrSignInNotAdmitted)
	})

	T.Run("a directory that cannot answer is not a refusal of the credential", func(t *testing.T) {
		t.Parallel()

		e, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, failingDirectory{})
		must.NoError(t, err)

		_, err = e.Authenticate(t.Context(), h.issue(t, h.member, false).Token)
		must.Error(t, err)
		test.False(t, platformerrors.Is(err, signingrpc.ErrUnauthenticated))
	})
}

// seen is what a handler behind HTTPMiddleware read off its request.
type seen struct {
	principal callers.Principal
	grants    authorization.Grants
	code      int
	granted   bool
}

// serveThrough runs one request carrying the given Authorization header through
// e's HTTPMiddleware, and reports what Extract and Grants answered inside it.
func serveThrough(t *testing.T, e *signingrpc.PrincipalExtractor, header string) seen {
	t.Helper()

	var saw seen

	handler := e.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw.principal, _ = e.Extract(r.Context())
		saw.grants, saw.granted = e.Grants(r.Context())

		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	saw.code = rec.Code

	return saw
}

func TestPrincipalExtractor_Extract(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	// Extract reads no credential of its own. A good token on a request that
	// neither the interceptor nor the middleware saw is a server that installed
	// neither, and it sees nobody rather than resolving the token per call.
	T.Run("names nobody on a request nothing resolved, whatever token it carries", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+h.issue(t, h.member, false).Token))

		_, ok := h.extractor(t).Extract(ctx)
		test.False(t, ok)
	})

	T.Run("answers with the caller the middleware resolved", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, h.extractor(t), "bearer "+h.issue(t, h.member, false).Token)
		must.NotNil(t, saw.principal)
		test.EqOp(t, h.member.User.ID, saw.principal.UserID())
	})

	T.Run("a request with no token carries nobody", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, h.extractor(t), "")
		test.Nil(t, saw.principal)
	})

	T.Run("the fallback answers for a request with no token, and for a token that is not ours", func(t *testing.T) {
		t.Parallel()

		other := &testPrincipal{userID: "legacy", scope: tenancy.Global()}
		e := h.extractor(t, signingrpc.WithFallback(func(context.Context) (callers.Principal, bool) { return other, true }))

		saw := serveThrough(t, e, "")
		must.NotNil(t, saw.principal)
		test.EqOp(t, "legacy", saw.principal.UserID())

		saw = serveThrough(t, e, "Bearer an-opaque-oauth2-token")
		must.NotNil(t, saw.principal)
		test.EqOp(t, "legacy", saw.principal.UserID())
	})

	T.Run("the fallback is not a second opinion on a user the directory refuses", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)
		issued := fresh.issue(t, fresh.member, false)

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		consulted := false
		e := fresh.extractor(t, signingrpc.WithFallback(func(context.Context) (callers.Principal, bool) {
			consulted = true

			return &testPrincipal{userID: "legacy"}, true
		}))

		saw := serveThrough(t, e, "Bearer "+issued.Token)
		test.EqOp(t, http.StatusForbidden, saw.code)
		test.False(t, consulted)
	})

	T.Run("grants report nothing without a policy", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, h.extractor(t), "Bearer "+h.issue(t, h.member, false).Token)
		must.NotNil(t, saw.principal)
		test.False(t, saw.granted)
	})
}

func TestPrincipalExtractor_HTTPMiddleware(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	serve := func(t *testing.T, e *signingrpc.PrincipalExtractor, header string) (int, string) {
		t.Helper()

		saw := serveThrough(t, e, header)
		if saw.principal == nil {
			return saw.code, ""
		}

		return saw.code, saw.principal.UserID()
	}

	T.Run("puts the caller a bearer token names on the request", func(t *testing.T) {
		t.Parallel()

		code, seen := serve(t, h.extractor(t), "Bearer "+h.issue(t, h.member, false).Token)
		test.EqOp(t, http.StatusNoContent, code)
		test.EqOp(t, h.member.User.ID, seen)
	})

	T.Run("refuses nothing: a bad credential proceeds as nobody", func(t *testing.T) {
		t.Parallel()

		code, seen := serve(t, h.extractor(t), "Bearer nonsense")
		test.EqOp(t, http.StatusNoContent, code)
		test.EqOp(t, "", seen)
	})

	T.Run("answers a banned caller's token itself", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)
		issued := fresh.issue(t, fresh.member, false)

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		code, _ := serve(t, fresh.extractor(t), "Bearer "+issued.Token)
		test.EqOp(t, http.StatusForbidden, code)
	})

	T.Run("answers a directory outage itself", func(t *testing.T) {
		t.Parallel()

		e, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, failingDirectory{})
		must.NoError(t, err)

		code, _ := serve(t, e, "Bearer "+h.issue(t, h.member, false).Token)
		test.EqOp(t, http.StatusServiceUnavailable, code)
	})
}

func TestAuthenticationRequirements(T *testing.T) {
	T.Parallel()

	T.Run("a method declared by name wins over its service", func(t *testing.T) {
		t.Parallel()

		reqs, err := signingrpc.NewAuthenticationRequirements().
			Declare(signingrpc.AuthenticationAnonymous, "/pkg.Service/Open").
			DeclareService(signingrpc.AuthenticationRequired, "pkg.Service").
			Build()
		must.NoError(t, err)

		requirement, ok := reqs.Lookup("/pkg.Service/Open")
		must.True(t, ok)
		test.EqOp(t, signingrpc.AuthenticationAnonymous, requirement)

		requirement, ok = reqs.Lookup("/pkg.Service/Closed")
		must.True(t, ok)
		test.EqOp(t, signingrpc.AuthenticationRequired, requirement)

		_, ok = reqs.Lookup("/pkg.Other/Anything")
		test.False(t, ok)
	})

	T.Run("a method declared twice is refused", func(t *testing.T) {
		t.Parallel()

		_, err := signingrpc.NewAuthenticationRequirements().
			Declare(signingrpc.AuthenticationAnonymous, "/pkg.Service/Open").
			Declare(signingrpc.AuthenticationRequired, "/pkg.Service/Open").
			Build()
		test.ErrorIs(t, err, signingrpc.ErrDuplicateAuthenticationMethod)
	})

	T.Run("a declaration naming nothing is refused", func(t *testing.T) {
		t.Parallel()

		_, err := signingrpc.NewAuthenticationRequirements().Declare(signingrpc.AuthenticationRequired, "").Build()
		test.ErrorIs(t, err, signingrpc.ErrInvalidAuthenticationMethod)

		_, err = signingrpc.NewAuthenticationRequirements().Declare(signingrpc.Authentication(0), "/pkg.Service/Open").Build()
		test.ErrorIs(t, err, signingrpc.ErrInvalidAuthenticationMethod)
	})

	T.Run("sign-in declares every method it has, and GetAuthStatus looks at the credential", func(t *testing.T) {
		t.Parallel()

		reqs, err := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).Build()
		must.NoError(t, err)

		for _, method := range signinpb.SignInService_ServiceDesc.Methods {
			_, ok := reqs.Lookup("/" + signinpb.SignInService_ServiceDesc.ServiceName + "/" + method.MethodName)
			test.True(t, ok, test.Sprintf("%s is undeclared", method.MethodName))
		}

		requirement, _ := reqs.Lookup(signinpb.SignInService_GetAuthStatus_FullMethodName)
		test.EqOp(t, signingrpc.AuthenticationOptional, requirement)

		requirement, _ = reqs.Lookup(signinpb.SignInService_GetSelf_FullMethodName)
		test.EqOp(t, signingrpc.AuthenticationRequired, requirement)

		requirement, _ = reqs.Lookup(signinpb.SignInService_ExchangeRefreshToken_FullMethodName)
		test.EqOp(t, signingrpc.AuthenticationAnonymous, requirement)
	})
}

// TestPrincipalExtractor_interceptor is the extractor installed the way a
// deployment installs it: the interceptor in front of the sign-in surface, the
// extractor handed to the surface, and a real token on a real connection.
func TestPrincipalExtractor_interceptor(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	dial := func(t *testing.T, e *signingrpc.PrincipalExtractor, reqs *signingrpc.AuthenticationRequirements) *signinclient.Client {
		t.Helper()

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

	signInReqs, reqsErr := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).Build()
	must.NoError(T, reqsErr)

	bearer := func(ctx context.Context, token string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}

	T.Run("a signed-in caller reaches a self-service method, in the login they signed in with", func(t *testing.T) {
		t.Parallel()

		client := dial(t, h.extractor(t), signInReqs)
		issued := h.issue(t, h.member, false)

		self, err := client.GetSelf(bearer(t.Context(), issued.Token), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, h.member.User.ID, self.GetUser().GetId())

		listed, err := client.ListSignIns(bearer(t.Context(), issued.Token), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)

		current := 0

		for _, s := range listed.GetSignIns() {
			if s.GetCurrent() {
				current++

				test.EqOp(t, issued.FamilyID, s.GetFamilyId())
			}
		}

		test.EqOp(t, 1, current)
	})

	T.Run("a required method with no credential is refused before the handler", func(t *testing.T) {
		t.Parallel()

		_, err := dial(t, h.extractor(t), signInReqs).GetSelf(t.Context(), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("an optional method answers a bad credential as nobody rather than refusing", func(t *testing.T) {
		t.Parallel()

		answer, err := dial(t, h.extractor(t), signInReqs).GetAuthStatus(bearer(t.Context(), "expired-or-forged"), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, answer.GetAuthenticated())
	})

	T.Run("an optional method sees a good credential", func(t *testing.T) {
		t.Parallel()

		answer, err := dial(t, h.extractor(t), signInReqs).GetAuthStatus(
			bearer(t.Context(), h.issue(t, h.member, false).Token), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, answer.GetAuthenticated())
	})

	T.Run("a method the table does not declare is refused", func(t *testing.T) {
		t.Parallel()

		empty, err := signingrpc.NewAuthenticationRequirements().Build()
		must.NoError(t, err)

		_, err = dial(t, h.extractor(t), empty).GetAuthStatus(t.Context(), &signinpb.GetAuthStatusRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("a banned caller's token is refused as the person, even where a caller is optional", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)
		issued := fresh.issue(t, fresh.member, false)

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		client := dial(t, fresh.extractor(t), signInReqs)

		_, err := client.GetSelf(bearer(t.Context(), issued.Token), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		_, err = client.GetAuthStatus(bearer(t.Context(), issued.Token), &signinpb.GetAuthStatusRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("a directory outage is unavailable, not unauthenticated", func(t *testing.T) {
		t.Parallel()

		e, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, failingDirectory{})
		must.NoError(t, err)

		_, err = dial(t, e, signInReqs).GetSelf(bearer(t.Context(), h.issue(t, h.member, false).Token), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unavailable, status.Code(err))
	})
}

// failingDirectory is a directory that cannot be read.
type failingDirectory struct{}

func (failingDirectory) GetPrincipal(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string, string,
) (*identity.Principal, error) {
	return nil, platformerrors.New("the directory's database is down")
}
