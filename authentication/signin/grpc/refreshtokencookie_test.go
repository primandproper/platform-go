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

	body := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(body[1:], uint32(len(payload)))
	body = append(body, payload...)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url+method, bytes.NewReader(body))
	must.NoError(t, err)
	req.Header.Set("Content-Type", "application/grpc-web+proto")

	for _, c := range sent {
		req.AddCookie(c)
	}

	res, err := http.DefaultClient.Do(req)
	must.NoError(t, err)

	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(res.Body)
	must.NoError(t, err)
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
