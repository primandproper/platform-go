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
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
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
type grpcWebConn struct {
	baseURL string
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

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = res.Body.Close() }()

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

		hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), secret)
		must.NoError(t, err)

		username := "web_" + identifiers.New()
		agreedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

		// The sign-in surface reads a person's directory with its default
		// resolver, which is the global one, so that is where they live.
		reg, err := s.identity.Register(t.Context(), tenancy.Global(),
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
