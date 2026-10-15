package grpc_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/passkeys/passkeyspb"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/cookies"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/server/grpcweb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const cookiePath = "/grpc"

// cookieSignIn is a sign-in service that issues one refresh token at a time and
// exchanges only the current one, which is enough of the real one's protocol
// for the cookie to be held to it.
type cookieSignIn struct {
	signinpb.UnimplementedSignInServiceServer

	expiresAt time.Time

	current   string
	signedOut []string
	cookies   []string
	minted    int

	mu sync.Mutex
}

func (s *cookieSignIn) issue() *signinpb.IssuedToken {
	s.minted++
	s.current = "refresh-" + strconv.Itoa(s.minted)

	return &signinpb.IssuedToken{
		Token:                 "access-" + strconv.Itoa(s.minted),
		RefreshToken:          s.current,
		RefreshTokenExpiresAt: timestamppb.New(s.expiresAt),
		FamilyId:              "family",
	}
}

func (s *cookieSignIn) heard(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.cookies = append(s.cookies, md.Get("cookie")...)
}

func (s *cookieSignIn) LoginForToken(ctx context.Context, _ *signinpb.LoginForTokenRequest) (*signinpb.LoginForTokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)

	return &signinpb.LoginForTokenResponse{Token: s.issue()}, nil
}

func (s *cookieSignIn) ExchangeRefreshToken(
	ctx context.Context,
	request *signinpb.ExchangeRefreshTokenRequest,
) (*signinpb.ExchangeRefreshTokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)

	if request.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "empty refresh token")
	}

	if request.GetRefreshToken() != s.current {
		return nil, status.Error(codes.Unauthenticated, "that refresh token is not the current one")
	}

	return &signinpb.ExchangeRefreshTokenResponse{Token: s.issue()}, nil
}

func (s *cookieSignIn) AdminLoginForToken(
	ctx context.Context,
	_ *signinpb.AdminLoginForTokenRequest,
) (*signinpb.AdminLoginForTokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)

	return &signinpb.AdminLoginForTokenResponse{Token: s.issue()}, nil
}

func (s *cookieSignIn) RedeemMagicLink(ctx context.Context, _ *signinpb.RedeemMagicLinkRequest) (*signinpb.RedeemMagicLinkResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)

	return &signinpb.RedeemMagicLinkResponse{Token: s.issue()}, nil
}

// SwitchAccount spends a refresh token as ExchangeRefreshToken does, and
// refuses a switch that names no account, so a request the cookie filled is
// seen to keep the rest of what the client sent.
func (s *cookieSignIn) SwitchAccount(ctx context.Context, request *signinpb.SwitchAccountRequest) (*signinpb.SwitchAccountResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)

	if request.GetRefreshToken() == "" || request.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "a switch needs a refresh token and an account")
	}

	if request.GetRefreshToken() != s.current {
		return nil, status.Error(codes.Unauthenticated, "that refresh token is not the current one")
	}

	return &signinpb.SwitchAccountResponse{Token: s.issue()}, nil
}

func (s *cookieSignIn) SignOut(ctx context.Context, request *signinpb.SignOutRequest) (*signinpb.SignOutResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.heard(ctx)
	s.signedOut = append(s.signedOut, request.GetRefreshToken())

	return &signinpb.SignOutResponse{}, nil
}

func (s *cookieSignIn) SignOutEverywhere(context.Context, *signinpb.SignOutEverywhereRequest) (*signinpb.SignOutEverywhereResponse, error) {
	return nil, status.Error(codes.Unauthenticated, "authentication required")
}

// cookiePasskeys is a passkeys service whose FinishLogin issues a token, which
// no list in the middleware names: it is found from the schema.
type cookiePasskeys struct {
	passkeyspb.UnimplementedPasskeysServiceServer
}

func (cookiePasskeys) FinishLogin(context.Context, *passkeyspb.FinishLoginRequest) (*passkeyspb.FinishLoginResponse, error) {
	return &passkeyspb.FinishLoginResponse{Token: &signinpb.IssuedToken{Token: "access", RefreshToken: "passkey-refresh"}}, nil
}

func (cookiePasskeys) AdminFinishLogin(context.Context, *passkeyspb.AdminFinishLoginRequest) (*passkeyspb.AdminFinishLoginResponse, error) {
	return &passkeyspb.AdminFinishLoginResponse{Token: &signinpb.IssuedToken{Token: "access", RefreshToken: "admin-passkey-refresh"}}, nil
}

func newCookieManager(t *testing.T) *cookies.SecureCookieManager {
	t.Helper()

	key := func() string {
		raw := make([]byte, 32)
		_, err := rand.Read(raw)
		must.NoError(t, err)

		return base64.StdEncoding.EncodeToString(raw)
	}

	manager, err := cookies.NewCookieManager(&cookies.Config{Base64EncodedHashKey: key(), Base64EncodedBlockKey: key()})
	must.NoError(t, err)

	return manager
}

type cookieHarness struct {
	service *cookieSignIn
	manager *cookies.SecureCookieManager
	server  *grpc.Server
	url     string
}

func newCookieHarness(t *testing.T) *cookieHarness {
	t.Helper()

	signIn := &cookieSignIn{expiresAt: time.Now().Add(time.Hour).Truncate(time.Second)}

	server := grpc.NewServer()
	signinpb.RegisterSignInServiceServer(server, signIn)
	passkeyspb.RegisterPasskeysServiceServer(server, cookiePasskeys{})

	manager := newCookieManager(t)

	middleware, err := signingrpc.NewRefreshTokenCookie(manager, server.GetServiceInfo(), cookiePath)
	must.NoError(t, err)

	web := httptest.NewServer(http.StripPrefix(cookiePath, middleware.Wrap(grpcweb.NewHandler(server))))
	t.Cleanup(web.Close)

	return &cookieHarness{service: signIn, manager: manager, server: server, url: web.URL + cookiePath}
}

// webCall is one gRPC-web call's outcome: its status, the message it carried,
// and the response it arrived in.
type webCall struct {
	response *http.Response
	message  []byte
	code     codes.Code
}

// call makes method as a browser's gRPC-web client does, sending the cookies
// given.
func (h *cookieHarness) call(t *testing.T, method string, request proto.Message, sent ...*http.Cookie) webCall {
	t.Helper()

	payload, err := proto.Marshal(request)
	must.NoError(t, err)

	return h.callRaw(t, method, webFrame(0, payload), sent...)
}

// callRaw is call with the request body as given, framed or not.
func (h *cookieHarness) callRaw(t *testing.T, method string, body []byte, sent ...*http.Cookie) webCall {
	t.Helper()

	res, raw := h.post(t, method, "application/grpc-web+proto", body, sent...)
	must.EqOp(t, http.StatusOK, res.StatusCode, must.Sprintf("HTTP answer: %s", raw))

	grpcStatus := res.Header.Get("Grpc-Status")

	var message []byte

	for len(raw) > 0 {
		must.GreaterEq(t, 5, len(raw), must.Sprint("a body ending inside a frame header"))

		flag, size := raw[0], binary.BigEndian.Uint32(raw[1:5])
		frame := raw[5 : 5+size]
		raw = raw[5+size:]

		if flag&0x80 == 0 {
			message = frame

			continue
		}

		for line := range strings.SplitSeq(string(frame), "\r\n") {
			if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "grpc-status") {
				grpcStatus = strings.TrimSpace(value)
			}
		}
	}

	code, err := strconv.Atoi(grpcStatus)
	must.NoError(t, err, must.Sprintf("grpc-status %q", grpcStatus))

	return webCall{response: res, message: message, code: codes.Code(code)}
}

// post sends body to method in contentType, with the cookies given, and
// answers the response and its body.
func (h *cookieHarness) post(t *testing.T, method, contentType string, body []byte, sent ...*http.Cookie) (res *http.Response, raw []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url+method, bytes.NewReader(body))
	must.NoError(t, err)
	req.Header.Set("Content-Type", contentType)

	for _, c := range sent {
		req.AddCookie(c)
	}

	res, err = http.DefaultClient.Do(req)
	must.NoError(t, err)

	defer func() { _ = res.Body.Close() }()

	raw, err = io.ReadAll(res.Body)
	must.NoError(t, err)

	return res, raw
}

func webFrame(flag byte, payload []byte) []byte {
	framed := make([]byte, 5, 5+len(payload))
	framed[0] = flag
	binary.BigEndian.PutUint32(framed[1:], uint32(len(payload)))

	return append(framed, payload...)
}

func marshaled(t *testing.T, m proto.Message) []byte {
	t.Helper()

	payload, err := proto.Marshal(m)
	must.NoError(t, err)

	return payload
}

// standIn is a handler in place of the gRPC-web one, which answers OK with body
// and remembers the last request that reached it, so a test can answer with
// what the real handler never writes and see what the middleware passed on.
type standIn struct {
	cookies []string
	body    []byte
	got     []byte
	reached bool
	mu      sync.Mutex
}

func (s *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	got, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	s.reached, s.got, s.cookies = true, got, r.Header.Values("Cookie")
	body := s.body
	s.mu.Unlock()

	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
	w.Header().Set("Grpc-Status", "0")
	_, _ = w.Write(body)
}

// errEncode is the cookie manager refusing to seal.
var errEncode = platformerrors.New("the cookie manager under test refuses to seal")

// refusingManager seals nothing, and reads whatever its embedded manager does.
type refusingManager struct {
	cookies.Manager
}

func (refusingManager) Encode(context.Context, string, any) (string, error) { return "", errEncode }

// newStandInHarness is the middleware over the platform's sign-in service, in
// front of next rather than a gRPC-web handler.
func newStandInHarness(t *testing.T, manager cookies.Manager, next http.Handler) *cookieHarness {
	t.Helper()

	server := grpc.NewServer()
	signinpb.RegisterSignInServiceServer(server, signinpb.UnimplementedSignInServiceServer{})

	middleware, err := signingrpc.NewRefreshTokenCookie(manager, server.GetServiceInfo(), cookiePath)
	must.NoError(t, err)

	web := httptest.NewServer(http.StripPrefix(cookiePath, middleware.Wrap(next)))
	t.Cleanup(web.Close)

	return &cookieHarness{url: web.URL + cookiePath}
}

// refreshCookie is the refresh token cookie a response set, or nil.
func refreshCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == signingrpc.RefreshTokenCookieName {
			return c
		}
	}

	return nil
}

// unsealed is the refresh token a cookie carries.
func unsealed(t *testing.T, manager cookies.Manager, c *http.Cookie) string {
	t.Helper()

	var token string
	must.NoError(t, manager.Decode(t.Context(), signingrpc.RefreshTokenCookieName, c.Value, &token))

	return token
}

func (h *cookieHarness) signIn(t *testing.T) (*signinpb.IssuedToken, *http.Cookie) {
	t.Helper()

	call := h.call(t, signinpb.SignInService_LoginForToken_FullMethodName, &signinpb.LoginForTokenRequest{})
	must.EqOp(t, codes.OK, call.code)

	var response signinpb.LoginForTokenResponse
	must.NoError(t, proto.Unmarshal(call.message, &response))

	cookie := refreshCookie(call.response)
	must.NotNil(t, cookie, must.Sprint("a gRPC-web sign-in set no refresh token cookie"))

	return response.GetToken(), cookie
}

func TestNewRefreshTokenCookie(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil manager", func(t *testing.T) {
		t.Parallel()

		middleware, err := signingrpc.NewRefreshTokenCookie(nil, nil, cookiePath)
		test.Nil(t, middleware)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a path that is not rooted", func(t *testing.T) {
		t.Parallel()

		middleware, err := signingrpc.NewRefreshTokenCookie(newCookieManager(t), nil, "grpc")
		test.Nil(t, middleware)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("takes its observability as options, and skips a nil one", func(t *testing.T) {
		t.Parallel()

		middleware, err := signingrpc.NewRefreshTokenCookie(newCookieManager(t), nil, cookiePath,
			signingrpc.WithCookieLogger(logging.EnsureLogger(nil)),
			signingrpc.WithCookieTracerProvider(tracing.EnsureTracerProvider(nil)),
			signingrpc.WithCookiePillars(&observability.Pillars{}),
			nil,
		)
		must.NoError(t, err)
		test.NotNil(t, middleware)
	})
}

// TestRefreshTokenCookie_methods pins what the descriptor walk finds over the
// platform's own services, so a schema change that adds a method issuing,
// spending or ending a refresh token, or that moves one out of the walk's
// reach, fails here and the list is changed on purpose. Missing an issuer is
// a refresh token in a browser-readable body.
func TestRefreshTokenCookie_methods(T *testing.T) {
	T.Parallel()

	T.Run("over the platform's services", func(t *testing.T) {
		t.Parallel()

		server := grpc.NewServer()
		signinpb.RegisterSignInServiceServer(server, signinpb.UnimplementedSignInServiceServer{})
		signinpb.RegisterSignInAdministrationServiceServer(server, signinpb.UnimplementedSignInAdministrationServiceServer{})
		passkeyspb.RegisterPasskeysServiceServer(server, passkeyspb.UnimplementedPasskeysServiceServer{})

		roles, err := signingrpc.CookieRoles(server.GetServiceInfo())
		must.NoError(t, err)

		test.Eq(t, map[string]signingrpc.CookieRole{
			signinpb.SignInService_LoginForToken_FullMethodName:        {Issues: true},
			signinpb.SignInService_AdminLoginForToken_FullMethodName:   {Issues: true},
			signinpb.SignInService_RedeemMagicLink_FullMethodName:      {Issues: true},
			signinpb.SignInService_ExchangeRefreshToken_FullMethodName: {Issues: true, Spends: true},
			signinpb.SignInService_SwitchAccount_FullMethodName:        {Issues: true, Spends: true},
			signinpb.SignInService_SignOut_FullMethodName:              {Spends: true, Ends: true},
			signinpb.SignInService_SignOutEverywhere_FullMethodName:    {Ends: true},
			passkeyspb.PasskeysService_FinishLogin_FullMethodName:      {Issues: true},
			passkeyspb.PasskeysService_AdminFinishLogin_FullMethodName: {Issues: true},
		}, roles)
	})

	for name, services := range map[string]map[string]grpc.ServiceInfo{
		"a service with no schema in the registry": {
			"example.v1.ExampleService": {Methods: []grpc.MethodInfo{{Name: "LoginForToken"}}},
		},
		"a name that is not a service": {
			"primandproper.platform.signin.v1.IssuedToken": {Methods: []grpc.MethodInfo{{Name: "LoginForToken"}}},
		},
		"a streaming method, which gRPC-web does not serve": {
			signinpb.SignInService_ServiceDesc.ServiceName: {Methods: []grpc.MethodInfo{{Name: "LoginForToken", IsServerStream: true}}},
		},
		"a method the schema does not declare": {
			signinpb.SignInService_ServiceDesc.ServiceName: {Methods: []grpc.MethodInfo{{Name: "LoginForTokenTwice"}}},
		},
	} {
		T.Run("passes over "+name, func(t *testing.T) {
			t.Parallel()

			roles, err := signingrpc.CookieRoles(services)
			must.NoError(t, err)
			test.MapEmpty(t, roles)
		})
	}
}

// issuedTokenResponse is any response message that carries one IssuedToken
// at its top level.
type issuedTokenResponse interface {
	proto.Message
	GetToken() *signinpb.IssuedToken
}

func TestRefreshTokenCookie(T *testing.T) {
	T.Parallel()

	T.Run("a sign-in seals the refresh token into the cookie and clears it from the body", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		token, cookie := h.signIn(t)

		test.EqOp(t, "", token.GetRefreshToken())
		test.EqOp(t, "access-1", token.GetToken())
		test.EqOp(t, "family", token.GetFamilyId())
		must.NotNil(t, token.GetRefreshTokenExpiresAt(), must.Sprint("refresh_token_expires_at left the body with the token"))
		test.EqOp(t, h.service.expiresAt.Unix(), token.GetRefreshTokenExpiresAt().AsTime().Unix())

		test.EqOp(t, "refresh-1", unsealed(t, h.manager, cookie))
		test.True(t, cookie.HttpOnly)
		test.True(t, cookie.Secure)
		test.EqOp(t, http.SameSiteStrictMode, cookie.SameSite)
		test.EqOp(t, cookiePath, cookie.Path)
		test.EqOp(t, "", cookie.Domain)
		test.EqOp(t, h.service.expiresAt.Unix(), cookie.Expires.Unix())
	})

	T.Run("an exchange with an empty field spends the cookie's token and rotates the cookie", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName, &signinpb.ExchangeRefreshTokenRequest{}, cookie)
		must.EqOp(t, codes.OK, call.code)

		var response signinpb.ExchangeRefreshTokenResponse
		must.NoError(t, proto.Unmarshal(call.message, &response))
		test.EqOp(t, "access-2", response.GetToken().GetToken())
		test.EqOp(t, "", response.GetToken().GetRefreshToken())

		rotated := refreshCookie(call.response)
		must.NotNil(t, rotated, must.Sprint("an exchange did not rotate the cookie"))
		test.EqOp(t, "refresh-2", unsealed(t, h.manager, rotated))

		// And the cookie it replaced is spent, as the token in it is.
		again := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName, &signinpb.ExchangeRefreshTokenRequest{}, cookie)
		test.EqOp(t, codes.Unauthenticated, again.code)
	})

	T.Run("a request carrying its own refresh token uses the body", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName,
			&signinpb.ExchangeRefreshTokenRequest{RefreshToken: "somebody-elses"}, cookie)
		test.EqOp(t, codes.Unauthenticated, call.code)
		test.Nil(t, refreshCookie(call.response))
	})

	T.Run("a cookie sealed under another key is no credential", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		h.signIn(t)

		foreign, err := newCookieManager(t).Encode(t.Context(), signingrpc.RefreshTokenCookieName, "refresh-1")
		must.NoError(t, err)

		call := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName, &signinpb.ExchangeRefreshTokenRequest{},
			&http.Cookie{Name: signingrpc.RefreshTokenCookieName, Value: foreign})
		test.EqOp(t, codes.Unauthenticated, call.code)
		test.Nil(t, call.message)
		test.Nil(t, refreshCookie(call.response))
	})

	T.Run("an exchange with neither a field nor a cookie is no credential", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)

		call := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName, &signinpb.ExchangeRefreshTokenRequest{})
		test.EqOp(t, codes.Unauthenticated, call.code)
	})

	T.Run("a sign-out spends the cookie's token and deletes the cookie", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_SignOut_FullMethodName, &signinpb.SignOutRequest{}, cookie)
		must.EqOp(t, codes.OK, call.code)

		deleted := refreshCookie(call.response)
		must.NotNil(t, deleted, must.Sprint("a sign-out left the cookie standing"))
		test.EqOp(t, "", deleted.Value)
		test.Less(t, 0, deleted.MaxAge)
		test.EqOp(t, cookiePath, deleted.Path)

		h.service.mu.Lock()
		defer h.service.mu.Unlock()
		test.Eq(t, []string{"refresh-1"}, h.service.signedOut)
	})

	T.Run("a failed sign-out everywhere leaves the cookie standing", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_SignOutEverywhere_FullMethodName, &signinpb.SignOutEverywhereRequest{}, cookie)
		test.EqOp(t, codes.Unauthenticated, call.code)
		test.Nil(t, refreshCookie(call.response))
	})

	T.Run("the cookie never reaches the server, and other cookies still do", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_ExchangeRefreshToken_FullMethodName, &signinpb.ExchangeRefreshTokenRequest{},
			cookie, &http.Cookie{Name: "theme", Value: "dark"})
		must.EqOp(t, codes.OK, call.code)

		h.service.mu.Lock()
		defer h.service.mu.Unlock()
		test.Eq(t, []string{"theme=dark"}, h.service.cookies)
	})

	T.Run("a method of another service that issues a token is sealed too", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)

		call := h.call(t, passkeyspb.PasskeysService_FinishLogin_FullMethodName, &passkeyspb.FinishLoginRequest{})
		must.EqOp(t, codes.OK, call.code)

		var response passkeyspb.FinishLoginResponse
		must.NoError(t, proto.Unmarshal(call.message, &response))
		test.EqOp(t, "", response.GetToken().GetRefreshToken())

		cookie := refreshCookie(call.response)
		must.NotNil(t, cookie)
		test.EqOp(t, "passkey-refresh", unsealed(t, h.manager, cookie))
	})

	for name, tc := range map[string]struct {
		request  proto.Message
		response issuedTokenResponse
		method   string
		sealed   string
	}{
		"AdminLoginForToken": {
			method:   signinpb.SignInService_AdminLoginForToken_FullMethodName,
			request:  &signinpb.AdminLoginForTokenRequest{},
			response: &signinpb.AdminLoginForTokenResponse{},
			sealed:   "refresh-1",
		},
		"RedeemMagicLink": {
			method:   signinpb.SignInService_RedeemMagicLink_FullMethodName,
			request:  &signinpb.RedeemMagicLinkRequest{Token: "link"},
			response: &signinpb.RedeemMagicLinkResponse{},
			sealed:   "refresh-1",
		},
		"AdminFinishLogin": {
			method:   passkeyspb.PasskeysService_AdminFinishLogin_FullMethodName,
			request:  &passkeyspb.AdminFinishLoginRequest{},
			response: &passkeyspb.AdminFinishLoginResponse{},
			sealed:   "admin-passkey-refresh",
		},
	} {
		T.Run(name+" seals the refresh token into the cookie and clears it from the body", func(t *testing.T) {
			t.Parallel()

			h := newCookieHarness(t)

			call := h.call(t, tc.method, tc.request)
			must.EqOp(t, codes.OK, call.code)

			response := proto.Clone(tc.response).(issuedTokenResponse)
			must.NoError(t, proto.Unmarshal(call.message, response))
			test.EqOp(t, "", response.GetToken().GetRefreshToken())
			test.NotEq(t, "", response.GetToken().GetToken())

			cookie := refreshCookie(call.response)
			must.NotNil(t, cookie, must.Sprintf("%s set no refresh token cookie", tc.method))
			test.EqOp(t, tc.sealed, unsealed(t, h.manager, cookie))
			test.True(t, cookie.HttpOnly)
			test.EqOp(t, cookiePath, cookie.Path)
		})
	}

	T.Run("a switch with an empty field spends the cookie's token, keeps the account, and rotates the cookie", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)
		_, cookie := h.signIn(t)

		call := h.call(t, signinpb.SignInService_SwitchAccount_FullMethodName, &signinpb.SwitchAccountRequest{AccountId: "other"}, cookie)
		must.EqOp(t, codes.OK, call.code)

		var response signinpb.SwitchAccountResponse
		must.NoError(t, proto.Unmarshal(call.message, &response))
		test.EqOp(t, "access-2", response.GetToken().GetToken())
		test.EqOp(t, "", response.GetToken().GetRefreshToken())

		rotated := refreshCookie(call.response)
		must.NotNil(t, rotated, must.Sprint("a switch did not rotate the cookie"))
		test.EqOp(t, "refresh-2", unsealed(t, h.manager, rotated))

		again := h.call(t, signinpb.SignInService_SwitchAccount_FullMethodName, &signinpb.SwitchAccountRequest{AccountId: "other"}, cookie)
		test.EqOp(t, codes.Unauthenticated, again.code)
		test.Nil(t, refreshCookie(again.response))
	})

	T.Run("a switch with neither a field nor a cookie is no credential", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)

		call := h.call(t, signinpb.SignInService_SwitchAccount_FullMethodName, &signinpb.SwitchAccountRequest{AccountId: "other"})
		test.EqOp(t, codes.Unauthenticated, call.code)
		test.Nil(t, call.message)
	})

	T.Run("a method it does not rewrite is passed on as it came, less the cookie", func(t *testing.T) {
		t.Parallel()

		next := &standIn{}
		h := newStandInHarness(t, newCookieManager(t), next)

		body := webFrame(0, marshaled(t, &signinpb.GetSelfRequest{}))
		h.callRaw(t, signinpb.SignInService_GetSelf_FullMethodName, body,
			&http.Cookie{Name: signingrpc.RefreshTokenCookieName, Value: "sealed"}, &http.Cookie{Name: "theme", Value: "dark"})

		next.mu.Lock()
		defer next.mu.Unlock()
		test.Eq(t, body, next.got)
		test.Eq(t, []string{"theme=dark"}, next.cookies)
	})

	T.Run("a call in an encoding gRPC-web does not serve is passed on, for the handler to refuse", func(t *testing.T) {
		t.Parallel()

		token := marshaled(t, &signinpb.LoginForTokenResponse{Token: &signinpb.IssuedToken{RefreshToken: "refresh"}})
		next := &standIn{body: webFrame(0, token)}
		h := newStandInHarness(t, newCookieManager(t), next)

		res, raw := h.post(t, signinpb.SignInService_LoginForToken_FullMethodName, "application/grpc-web-text", nil)
		test.Eq(t, webFrame(0, token), raw)
		test.Nil(t, refreshCookie(res))
	})

	for name, body := range map[string][]byte{
		"a body that is not one whole frame": {0, 0, 0},
		"a message that does not decode":     webFrame(0, []byte{0xff, 0xff}),
		"a body too large to read":           webFrame(0, marshaled(T, &signinpb.SwitchAccountRequest{AccountId: strings.Repeat("a", 65<<10)})),
	} {
		T.Run(name+" is passed on unfilled, for the server to answer", func(t *testing.T) {
			t.Parallel()

			next := &standIn{}
			h := newStandInHarness(t, newCookieManager(t), next)

			// No cookie came with it, so a request that had been read would
			// have been refused here as presenting no credential.
			h.callRaw(t, signinpb.SignInService_SwitchAccount_FullMethodName, body)

			next.mu.Lock()
			defer next.mu.Unlock()
			test.True(t, next.reached)
			test.Eq(t, body, next.got)
		})
	}

	T.Run("a response carrying no refresh token is passed on as it came", func(t *testing.T) {
		t.Parallel()

		payload := marshaled(t, &signinpb.LoginForTokenResponse{Token: &signinpb.IssuedToken{Token: "access"}})
		next := &standIn{body: append(webFrame(0, payload), webFrame(0x80, []byte("grpc-status: 0\r\n"))...)}
		h := newStandInHarness(t, newCookieManager(t), next)

		call := h.call(t, signinpb.SignInService_LoginForToken_FullMethodName, &signinpb.LoginForTokenRequest{})
		test.EqOp(t, codes.OK, call.code)
		test.Eq(t, payload, call.message)
		test.Nil(t, refreshCookie(call.response))
	})

	token := marshaled(T, &signinpb.LoginForTokenResponse{Token: &signinpb.IssuedToken{Token: "access", RefreshToken: "refresh"}})

	for name, tc := range map[string]struct {
		body    []byte
		refuses bool
	}{
		"a body ending inside a frame header": {body: []byte{0, 0}},
		"a frame running past its body":       {body: webFrame(0, token)[:8]},
		"two messages":                        {body: append(webFrame(0, token), webFrame(0, token)...)},
		"a compressed message":                {body: webFrame(1, token)},
		"a message that does not decode":      {body: webFrame(0, []byte{0xff, 0xff})},
		"a token the manager will not seal":   {body: webFrame(0, token), refuses: true},
	} {
		T.Run("a response it cannot seal is refused rather than sent: "+name, func(t *testing.T) {
			t.Parallel()

			var manager cookies.Manager = newCookieManager(t)
			if tc.refuses {
				manager = refusingManager{Manager: manager}
			}

			h := newStandInHarness(t, manager, &standIn{body: tc.body})

			call := h.call(t, signinpb.SignInService_LoginForToken_FullMethodName, &signinpb.LoginForTokenRequest{})
			test.EqOp(t, codes.Internal, call.code)
			test.Nil(t, call.message)
			test.Nil(t, refreshCookie(call.response))
		})
	}

	T.Run("a native gRPC sign-in still carries the refresh token in the body", func(t *testing.T) {
		t.Parallel()

		h := newCookieHarness(t)

		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		must.NoError(t, err)

		go func() { _ = h.server.Serve(listener) }()
		t.Cleanup(h.server.Stop)

		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		response, err := signinpb.NewSignInServiceClient(conn).LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{})
		must.NoError(t, err)
		test.EqOp(t, "refresh-1", response.GetToken().GetRefreshToken())
	})
}
