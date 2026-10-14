package assembled_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v15/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/identity"
	identityclient "github.com/primandproper/platform-go/v15/identity/grpc/client"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// grpcWebPrefix is where the harness's service.Config mounts gRPC-web on the
// HTTP server.
const grpcWebPrefix = "/grpc"

// grpcWebConn is a grpc.ClientConnInterface that makes each call as a gRPC-web
// client does: one HTTP/1.1 POST to the HTTP server, the request a
// length-prefixed message, and the status in a trailer frame at the end of the
// response body.
//
// It is hand-rolled, as a browser's fetch would be, rather than a library
// client, so what reaches the server is the wire protocol and nothing a library
// adds. Outgoing metadata travels as headers, which is how a gRPC-web client
// sends a bearer token. It is unary-only, as the server is.
//
// With a jar it keeps the cookies the server sets and sends them back, as a
// browser does for its own origin; without one it sends none.
type grpcWebConn struct {
	jar     *grpcWebJar
	baseURL string
}

// grpcWebJar is a browser's cookie store for the one origin the harness
// serves. It is not net/http/cookiejar, which withholds a Secure cookie from
// http://127.0.0.1 where a browser treats a loopback origin as secure and
// sends it.
type grpcWebJar struct {
	cookies map[string]*http.Cookie
	mu      sync.Mutex
}

func (j *grpcWebJar) keep(res *http.Response) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)

			continue
		}

		j.cookies[c.Name] = c
	}
}

func (j *grpcWebJar) send(req *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, c := range j.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
}

func (j *grpcWebJar) get(name string) (*http.Cookie, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	c, ok := j.cookies[name]

	return c, ok
}

var _ grpc.ClientConnInterface = (*grpcWebConn)(nil)

func (c *grpcWebConn) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	request, ok := args.(proto.Message)
	if !ok {
		return fmt.Errorf("gRPC-web request %T is not a proto message", args)
	}

	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}

	body := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(body[1:], uint32(len(payload)))
	body = append(body, payload...)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")

	md, _ := metadata.FromOutgoingContext(ctx)
	for key, values := range md {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	if c.jar != nil {
		c.jar.send(req)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = res.Body.Close() }()

	if c.jar != nil {
		c.jar.keep(res)
	}

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("gRPC-web call to %s answered HTTP %d: %s", method, res.StatusCode, raw)
	}

	// A trailers-only response carries the status as headers; any other
	// carries it in the last frame.
	trailers := map[string]string{}
	for _, name := range []string{"grpc-status", "grpc-message", "grpc-status-details-bin"} {
		if value := res.Header.Get(name); value != "" {
			trailers[name] = value
		}
	}

	var message []byte

	for len(raw) > 0 {
		if len(raw) < 5 {
			return fmt.Errorf("gRPC-web response to %s ends inside a frame header", method)
		}

		flag, size := raw[0], binary.BigEndian.Uint32(raw[1:5])
		if uint64(size) > uint64(len(raw)-5) {
			return fmt.Errorf("gRPC-web response to %s has a frame running past its body", method)
		}

		frame := raw[5 : 5+size]
		raw = raw[5+size:]

		if flag&0x80 == 0 {
			message = frame

			continue
		}

		for line := range strings.SplitSeq(string(frame), "\r\n") {
			if name, value, found := strings.Cut(line, ":"); found {
				trailers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
			}
		}
	}

	if err = trailerStatus(trailers); err != nil {
		return err
	}

	if message == nil {
		return fmt.Errorf("gRPC-web call to %s succeeded with no message", method)
	}

	response, ok := reply.(proto.Message)
	if !ok {
		return fmt.Errorf("gRPC-web reply %T is not a proto message", reply)
	}

	return proto.Unmarshal(message, response)
}

func (*grpcWebConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "gRPC-web serves unary calls only")
}

// trailerStatus is the error the status trailers describe, or nil for OK.
func trailerStatus(trailers map[string]string) error {
	raw, found := trailers["grpc-status"]
	if !found {
		return status.Error(codes.Internal, "gRPC-web response carried no grpc-status")
	}

	code, err := strconv.Atoi(raw)
	if err != nil {
		return status.Errorf(codes.Internal, "gRPC-web response carried grpc-status %q", raw)
	}

	if codes.Code(code) == codes.OK {
		return nil
	}

	if details, ok := trailers["grpc-status-details-bin"]; ok {
		encoded, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimRight(details, "="))
		if decodeErr == nil {
			var st spb.Status
			if proto.Unmarshal(encoded, &st) == nil {
				return status.FromProto(&st).Err()
			}
		}
	}

	message, err := url.PathUnescape(trailers["grpc-message"])
	if err != nil {
		message = trailers["grpc-message"]
	}

	return status.Error(codes.Code(code), message)
}

// grpcWebSubject is what the gRPC-web assertions need from an assembled
// service: the two ways in, and the services a person is made with.
type grpcWebSubject struct {
	native   grpc.ClientConnInterface
	web      grpc.ClientConnInterface
	identity *identity.Service
	signIn   *signin.Service

	// webURL is where web dials, for a browser of the assertion's own: one
	// that keeps cookies.
	webURL string
}

// registerWebPerson registers somebody in the global directory, where the
// sign-in surface's default resolver looks, with a password, and answers with
// their username and that password.
func registerWebPerson(t *testing.T, s grpcWebSubject, secret string) (reg *identity.Registration, username string) {
	t.Helper()

	hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), secret)
	must.NoError(t, err)

	username = "web_" + identifiers.New()
	agreedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	reg, err = s.identity.Register(t.Context(), tenancy.Global(),
		&identity.User{
			Username:                   username,
			EmailAddress:               identifiers.New() + "@conformance.invalid",
			HashedPassword:             hashed,
			AccountStatus:              identity.StatusGood,
			LastAcceptedTermsOfService: &agreedAt,
			LastAcceptedPrivacyPolicy:  &agreedAt,
		},
		&identity.Account{Name: "web_" + identifiers.New()},
		[]string{"owner"})
	must.NoError(t, err)

	return reg, username
}

// assertGRPCWeb holds the assembled service to what a browser needs of it: a
// person signs in through the HTTP server, uses the token they were issued
// there, and is read as the same caller, in the same tenant, as they would be
// over the gRPC listener.
func assertGRPCWeb(t *testing.T, s grpcWebSubject) {
	t.Helper()

	t.Run("a person signs in over gRPC-web and calls an authenticated RPC with the token", func(t *testing.T) {
		t.Parallel()

		const secret = "correct horse battery staple, over gRPC-web"

		reg, username := registerWebPerson(t, s, secret)

		web := signinclient.Wrap(s.web)

		signedIn, err := web.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: username, Password: secret},
		})
		must.NoError(t, err, must.Sprint("signing in through the HTTP server's gRPC-web route"))

		token := signedIn.GetToken().GetToken()
		must.NotEq(t, "", token, must.Sprint("a gRPC-web sign-in answered with no token"))

		self, err := web.GetSelf(bearer(t.Context(), token), &signinpb.GetSelfRequest{})
		must.NoError(t, err, must.Sprint("an authenticated call over gRPC-web with the token gRPC-web issued"))
		test.EqOp(t, reg.User.ID, self.GetUser().GetId())

		// And the token is the same credential on either transport.
		native, err := signinclient.Wrap(s.native).GetSelf(bearer(t.Context(), token), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.True(t, proto.Equal(native, self), test.Sprint("GetSelf answered differently over gRPC-web than over gRPC"))

		// And nobody on the call is refused over gRPC-web as over gRPC, which
		// is the half that says the token is what admitted the first call.
		_, err = web.GetSelf(t.Context(), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("a browser's refresh token lives in a cookie and never in a body", func(t *testing.T) {
		t.Parallel()

		const secret = "correct horse battery staple, kept in a cookie"

		reg, username := registerWebPerson(t, s, secret)
		credentials := &signinpb.Credentials{Username: username, Password: secret}

		jar := &grpcWebJar{cookies: map[string]*http.Cookie{}}
		browser := signinclient.Wrap(&grpcWebConn{baseURL: s.webURL, jar: jar})

		signedIn, err := browser.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{Credentials: credentials})
		must.NoError(t, err)
		test.EqOp(t, "", signedIn.GetToken().GetRefreshToken(), test.Sprint("a gRPC-web sign-in put its refresh token in the body"))
		test.NotNil(t, signedIn.GetToken().GetRefreshTokenExpiresAt(),
			test.Sprint("refresh_token_expires_at left the body, so a client cannot tell a cookie should exist"))

		sealed, ok := jar.get(signingrpc.RefreshTokenCookieName)
		must.True(t, ok, must.Sprint("a gRPC-web sign-in set no refresh token cookie"))
		test.True(t, sealed.HttpOnly)
		test.True(t, sealed.Secure)
		test.EqOp(t, http.SameSiteStrictMode, sealed.SameSite)
		test.EqOp(t, grpcWebPrefix, sealed.Path)

		// The exchange a browser makes: nothing in the body, the cookie
		// carrying the credential.
		exchanged, err := browser.ExchangeRefreshToken(t.Context(), &signinpb.ExchangeRefreshTokenRequest{})
		must.NoError(t, err, must.Sprint("an exchange carried by the cookie"))
		test.EqOp(t, "", exchanged.GetToken().GetRefreshToken())

		rotated, ok := jar.get(signingrpc.RefreshTokenCookieName)
		must.True(t, ok)
		test.NotEq(t, sealed.Value, rotated.Value, test.Sprint("an exchange did not rotate the cookie"))

		self, err := browser.GetSelf(bearer(t.Context(), exchanged.GetToken().GetToken()), &signinpb.GetSelfRequest{})
		must.NoError(t, err, must.Sprint("an authenticated call with the token the cookie's exchange minted"))
		test.EqOp(t, reg.User.ID, self.GetUser().GetId())

		// The cookie a browser held before the exchange is spent, as the
		// token in it is.
		stale := signinclient.Wrap(&grpcWebConn{
			baseURL: s.webURL,
			jar:     &grpcWebJar{cookies: map[string]*http.Cookie{sealed.Name: sealed}},
		})
		_, err = stale.ExchangeRefreshToken(t.Context(), &signinpb.ExchangeRefreshTokenRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))

		// Spending the stale cookie was a replay, which ends the login; a
		// fresh one signs out.
		_, err = browser.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{Credentials: credentials})
		must.NoError(t, err)

		_, err = browser.SignOut(t.Context(), &signinpb.SignOutRequest{})
		must.NoError(t, err, must.Sprint("a sign-out carried by the cookie"))

		_, ok = jar.get(signingrpc.RefreshTokenCookieName)
		test.False(t, ok, test.Sprint("a sign-out left the refresh token cookie standing"))

		_, err = browser.ExchangeRefreshToken(t.Context(), &signinpb.ExchangeRefreshTokenRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))

		// And a native client, which keeps its own, still gets it in the body.
		native, err := signinclient.Wrap(s.native).LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{Credentials: credentials})
		must.NoError(t, err)
		test.NotEq(t, "", native.GetToken().GetRefreshToken())
	})

	t.Run("tenancy resolves the same over gRPC-web as over gRPC", func(t *testing.T) {
		t.Parallel()

		// A tenant of their own, so a call that lost the caller's scope on the
		// way through the HTTP server would read the global directory and find
		// nobody.
		scope := tenancy.Of(identifiers.New())

		reg, err := s.identity.Register(t.Context(), scope,
			&identity.User{
				Username:      "web_" + identifiers.New(),
				EmailAddress:  identifiers.New() + "@conformance.invalid",
				AccountStatus: identity.StatusGood,
			},
			&identity.Account{Name: "web_" + identifiers.New()},
			[]string{"owner"})
		must.NoError(t, err)

		issued, err := s.signIn.IssueForPrincipal(t.Context(), scope, reg.User.ID, reg.Account.ID, signin.MultiFactor())
		must.NoError(t, err)

		ctx := bearer(t.Context(), issued.Token)

		overWeb, err := identityclient.Wrap(s.web).GetPrincipal(ctx, &identitypb.GetPrincipalRequest{})
		must.NoError(t, err, must.Sprint("reading a tenant's principal over gRPC-web"))

		overGRPC, err := identityclient.Wrap(s.native).GetPrincipal(ctx, &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		test.EqOp(t, reg.User.ID, overWeb.GetPrincipal().GetUser().GetId())
		test.EqOp(t, reg.Account.ID, overWeb.GetPrincipal().GetActiveAccountId())
		test.True(t, proto.Equal(overGRPC, overWeb),
			test.Sprint("GetPrincipal answered differently over gRPC-web than over gRPC"))
	})
}

// bearer is ctx carrying token the way every client here sends one.
func bearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}
