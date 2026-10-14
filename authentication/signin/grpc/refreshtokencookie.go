package grpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/cookies"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// RefreshTokenCookieName is the cookie [RefreshTokenCookie] keeps a browser's
// refresh token in.
//
// The __Secure- prefix is a browser's own check on the attributes: a cookie
// named this way is refused unless it is Secure and set from a secure origin,
// so a misconfigured proxy that drops TLS loses the cookie rather than sending
// it in the clear. It is not __Host-, which would also require Path=/, and the
// cookie is scoped to the gRPC-web route instead.
const RefreshTokenCookieName = "__Secure-signin-refresh-token" //nolint:gosec // G101: a cookie's name, not its value.

const (
	refreshTokenCookieServiceName = "signin_refresh_token_cookie"

	// frameHeaderSize is the flag byte and four-byte big-endian length ahead of
	// every gRPC-web frame.
	frameHeaderSize = 5

	// compressedFrameFlag marks a message frame whose payload is compressed,
	// and trailerFrameFlag the frame gRPC-web moves the trailers into.
	compressedFrameFlag byte = 0x01
	trailerFrameFlag    byte = 0x80

	// maxFilledRequestSize bounds the request body read to fill a refresh
	// token in. Every request that carries one is a handful of short strings;
	// one larger than this is passed through untouched, to be answered by the
	// server, rather than buffered.
	maxFilledRequestSize = 64 << 10
)

// RefreshTokenCookie moves the refresh token a sign-in issues out of the
// response body and into a sealed httpOnly cookie, and back into the request
// that spends it, so page JavaScript on a gRPC-web client never holds it.
//
// A browser has no other compliant place to keep one. The client contract puts
// a browser's credential store in an httpOnly cookie, page code cannot set
// one, and the schema returns IssuedToken.refresh_token in the body. Only a
// Set-Cookie from the server meets the bar, and this is that Set-Cookie.
//
// It is HTTP middleware for the gRPC-web route, mounted behind the path prefix
// is stripped and in front of the gRPC-web handler, and it does three things:
//
//   - A response carrying an IssuedToken, from any method on the server whose
//     response message holds one, has its refresh_token sealed into
//     [RefreshTokenCookieName] and cleared from the body. refresh_token_expires_at
//     stays in the body and is the cookie's expiry, so a client still knows a
//     cookie should exist and when it stops being worth presenting.
//   - A request to a method of signin.v1 whose request message carries a
//     refresh_token — ExchangeRefreshToken, SwitchAccount, SignOut — that
//     arrives with the field empty has it filled from the cookie. A request that
//     carries one in its body uses the body, though the token its response
//     issues is still sealed like any other. One with neither, or with a cookie
//     that does not verify, is answered UNAUTHENTICATED here: it presented no
//     credential.
//   - A successful SignOut or SignOutEverywhere deletes the cookie, since the
//     refresh token in it no longer mints anything.
//
// The access token stays in the body. Every other RPC keeps authenticating with
// authorization: Bearer, so principal extraction is the same for every client,
// and only the methods that spend a refresh token read the cookie — which keeps
// the cross-site request surface to those methods, and SameSite=Strict plus
// gRPC-web's content type, which no simple request can carry and so forces a
// CORS preflight, closes it. The cookie is only an envelope: the refresh token
// inside is still single-use and revocable server-side.
//
// The cookie is HttpOnly, Secure, SameSite=Strict, host-only and scoped to the
// route's path, whatever the cookie manager's own configuration says about
// the cookies it builds; the manager supplies the sealing — an HMAC over the
// name and a timestamp, and AES over the value — and nothing else. Its
// Lifetime bounds the sealed timestamp, so it is how long a browser may go
// between exchanges before the cookie stops verifying, whatever the refresh
// token's own expiry. Every exchange reseals, so a client that keeps refreshing
// is held only to the refresh token's window.
//
// The cookie is a credential in its own right — presenting it is presenting
// the refresh token — so it is removed from every request's Cookie header
// before the gRPC server sees it, and never reaches metadata, an interceptor
// or a log.
//
// Everything through the route is sealed, whichever client made the call. A
// deployment whose gRPC-web clients keep their own refresh tokens — a React
// Native app with a keychain — does not install this.
type RefreshTokenCookie struct {
	cookies cookies.Manager
	o11y    observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger         logging.Logger
	tracerProvider tracing.Provider
	methods        map[string]cookieMethod
	path           string
}

// cookieMethod is what RefreshTokenCookie does on one method, read off its
// descriptors once, at construction.
type cookieMethod struct {
	request  protoreflect.MessageType
	response protoreflect.MessageType

	// refreshToken is the request's refresh_token field, or nil for a method
	// whose request carries none.
	refreshToken protoreflect.FieldDescriptor

	// issues is whether the response carries an IssuedToken, and ends whether
	// a successful call leaves the cookie holding a dead credential.
	issues bool
	ends   bool
}

// RefreshTokenCookieOption configures a RefreshTokenCookie.
type RefreshTokenCookieOption func(*RefreshTokenCookie)

// WithCookieLogger sets the logger. Absent means no logging.
func WithCookieLogger(logger logging.Logger) RefreshTokenCookieOption {
	return func(c *RefreshTokenCookie) { c.logger = logger }
}

// WithCookieTracerProvider sets the tracer provider. Absent means no tracing.
func WithCookieTracerProvider(tracerProvider tracing.Provider) RefreshTokenCookieOption {
	return func(c *RefreshTokenCookie) { c.tracerProvider = tracerProvider }
}

// WithCookiePillars supplies the logger and tracer provider at once. It records
// no metrics of its own: every call it rewrites is counted by the gRPC server
// that answers it.
func WithCookiePillars(p *observability.Pillars) RefreshTokenCookieOption {
	return func(c *RefreshTokenCookie) { c.logger, c.tracerProvider, _ = p.Deps() }
}

// NewRefreshTokenCookie builds the middleware for a gRPC-web route at path,
// sealing with manager, over the services a gRPC server reports from
// GetServiceInfo.
//
// Which methods it rewrites is read from their descriptors, not listed: any
// unary method whose response message holds an IssuedToken, at any depth of
// singular message fields, has it sealed, so a consumer's own RPC that issues
// one is covered the day it is registered. A service whose descriptor is not in
// the global registry has no protobuf schema to read and is passed through.
//
// path is the route's prefix, such as /grpc, and is the cookie's Path.
func NewRefreshTokenCookie(
	manager cookies.Manager,
	services map[string]grpc.ServiceInfo,
	path string,
	opts ...RefreshTokenCookieOption,
) (*RefreshTokenCookie, error) {
	if manager == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "a refresh token cookie needs a cookie manager to seal with")
	}

	if !strings.HasPrefix(path, "/") {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "refresh token cookie path %q is not rooted", path)
	}

	methods, err := cookieMethods(services)
	if err != nil {
		return nil, err
	}

	c := &RefreshTokenCookie{cookies: manager, methods: methods, path: path}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	c.o11y = observability.NewObserver(refreshTokenCookieServiceName, c.logger, c.tracerProvider)
	c.logger, c.tracerProvider = nil, nil

	return c, nil
}

// cookieMethods reads, for every unary method the services name, whether it
// issues an IssuedToken, spends a refresh token, or ends a sign-in.
func cookieMethods(services map[string]grpc.ServiceInfo) (map[string]cookieMethod, error) {
	issuedToken := (&signinpb.IssuedToken{}).ProtoReflect().Descriptor()
	signInPackage := issuedToken.ParentFile().Package()
	ending := map[string]struct{}{
		signinpb.SignInService_SignOut_FullMethodName:           {},
		signinpb.SignInService_SignOutEverywhere_FullMethodName: {},
	}

	methods := map[string]cookieMethod{}

	for _, service := range slices.Sorted(maps.Keys(services)) {
		found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
		if err != nil {
			continue
		}

		descriptor, ok := found.(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}

		for _, info := range services[service].Methods {
			// gRPC-web answers a streaming method UNIMPLEMENTED without
			// reaching the server, so there is nothing of one to rewrite.
			if info.IsClientStream || info.IsServerStream {
				continue
			}

			md := descriptor.Methods().ByName(protoreflect.Name(info.Name))
			if md == nil {
				continue
			}

			fullMethod := "/" + service + "/" + info.Name
			_, ends := ending[fullMethod]

			m := cookieMethod{
				issues: carries(md.Output(), issuedToken.FullName(), map[protoreflect.FullName]struct{}{}),
				ends:   ends,
			}

			if md.Input().ParentFile().Package() == signInPackage {
				if fd := md.Input().Fields().ByName("refresh_token"); fd != nil && fd.Kind() == protoreflect.StringKind && !fd.IsList() {
					m.refreshToken = fd
				}
			}

			if !m.issues && !m.ends && m.refreshToken == nil {
				continue
			}

			if m.request, err = protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName()); err != nil {
				return nil, platformerrors.Wrapf(err, "finding the Go type of %s's request", fullMethod)
			}

			if m.response, err = protoregistry.GlobalTypes.FindMessageByName(md.Output().FullName()); err != nil {
				return nil, platformerrors.Wrapf(err, "finding the Go type of %s's response", fullMethod)
			}

			methods[fullMethod] = m
		}
	}

	return methods, nil
}

// carries reports whether a message of type md holds a target in a singular
// message field, at any depth. A list or a map of them is not looked into: no
// response carries more than one token, and a cookie holds one.
func carries(md protoreflect.MessageDescriptor, target protoreflect.FullName, seen map[protoreflect.FullName]struct{}) bool {
	if _, visited := seen[md.FullName()]; visited {
		return false
	}

	seen[md.FullName()] = struct{}{}

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Message() == nil || fd.IsList() || fd.IsMap() {
			continue
		}

		if fd.Message().FullName() == target || carries(fd.Message(), target, seen) {
			return true
		}
	}

	return false
}

// Wrap is the middleware, in front of next, the gRPC-web handler.
//
// It expects the route's prefix already stripped, so the path it reads is the
// gRPC method's own.
func (c *RefreshTokenCookie) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		stripped, sealed := withoutRefreshTokenCookie(req)

		method, ok := c.methods[req.URL.Path]
		if !ok || !isGRPCWeb(req) {
			next.ServeHTTP(w, stripped)

			return
		}

		ctx, op := c.o11y.Begin(req.Context(), observability.WithValue("rpc.method", req.URL.Path))
		defer op.End()

		// Its body and headers are rewritten below, and they are the
		// caller's until it is a copy.
		r := stripped.Clone(ctx)

		if method.refreshToken != nil {
			if !c.fill(ctx, op, r, method, sealed) {
				if err := trailersOnly(w, r, codes.Unauthenticated,
					"no refresh token: the request carries none and no sign-in cookie that verifies came with it"); err != nil {
					op.Logger().WithError(err).Debug("writing a gRPC-web refusal")
				}

				return
			}
		}

		if method.issues {
			// A compressed message cannot be read to be sealed, and
			// grpc-go answers in whatever encoding the client accepts, so
			// a response that may carry a refresh token is asked for
			// uncompressed.
			r.Header.Del("Grpc-Accept-Encoding")
		}

		held := &heldResponse{header: http.Header{}}
		next.ServeHTTP(held, r)

		c.answer(ctx, op, w, r, method, held)
	})
}

// fill puts the cookie's refresh token into r, a request whose own is empty,
// and reports false for one that has neither. A request it cannot read is
// passed on as it arrived, for the server to refuse.
func (c *RefreshTokenCookie) fill(ctx context.Context, op observability.Operation, r *http.Request, method cookieMethod, sealed string) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFilledRequestSize+1))
	if err != nil || len(body) > maxFilledRequestSize {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))

		return true
	}

	r.Body = io.NopCloser(bytes.NewReader(body))

	payload, flag, ok := singleMessage(body)
	if !ok || flag&compressedFrameFlag != 0 {
		return true
	}

	request := method.request.New()
	if err = proto.Unmarshal(payload, request.Interface()); err != nil {
		return true
	}

	if request.Get(method.refreshToken).String() != "" {
		return true
	}

	token, ok := c.unseal(ctx, op, sealed)
	if !ok {
		return false
	}

	request.Set(method.refreshToken, protoreflect.ValueOfString(token))

	rewritten, err := proto.Marshal(request.Interface())
	if err != nil {
		op.Acknowledge(err, "re-encoding a request filled from the refresh token cookie")

		return false
	}

	framed := frame(0, rewritten)
	r.Body = io.NopCloser(bytes.NewReader(framed))
	r.ContentLength = int64(len(framed))
	r.Header.Del("Content-Length")

	return true
}

// unseal reads the refresh token out of the cookie's sealed value. A cookie
// that is absent, empty, or does not verify — sealed under another key,
// tampered with, or older than the manager's lifetime — is no credential at
// all.
func (c *RefreshTokenCookie) unseal(ctx context.Context, op observability.Operation, sealed string) (string, bool) {
	if sealed == "" {
		return "", false
	}

	var token string
	if err := c.cookies.Decode(ctx, RefreshTokenCookieName, sealed, &token); err != nil {
		op.Acknowledge(err, "reading the refresh token cookie")

		return "", false
	}

	return token, token != ""
}

// answer writes the held response to w: sealed, when it carries a refresh
// token, and with the cookie deleted, when it ended the sign-in in it.
//
// A response carrying a refresh token that cannot be sealed is not sent at
// all. The body is the one place page JavaScript can read, and the token in it
// is the credential this exists to keep out of there.
func (c *RefreshTokenCookie) answer(
	ctx context.Context,
	op observability.Operation,
	w http.ResponseWriter,
	r *http.Request,
	method cookieMethod,
	held *heldResponse,
) {
	status := held.status
	if status == 0 {
		status = http.StatusOK
	}

	body := held.body.Bytes()

	if status == http.StatusOK && method.issues {
		sealed, cookie, err := c.seal(ctx, method, body)
		if err != nil {
			op.Acknowledge(err, "sealing a refresh token into its cookie")

			if err = trailersOnly(w, r, codes.Internal, "the issued refresh token could not be sealed"); err != nil {
				op.Logger().WithError(err).Debug("writing a gRPC-web refusal")
			}

			return
		}

		body = sealed
		if cookie != nil {
			held.header.Add("Set-Cookie", cookie.String())
		}
	}

	if status == http.StatusOK && method.ends && succeeded(held.header, body) {
		held.header.Add("Set-Cookie", c.cookie("", -1).String())
	}

	maps.Copy(w.Header(), held.header)
	w.Header().Del("Content-Length")
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil {
		// The client has gone. The call itself completed and was logged by
		// the server's interceptors; only its answer went undelivered.
		op.Logger().WithError(err).Debug("writing a gRPC-web response")
	}
}

// seal moves the refresh token in body's message into a cookie, answering the
// body with it cleared and the cookie to set — or body unchanged and no cookie
// when there was no message, or no refresh token in it.
func (c *RefreshTokenCookie) seal(ctx context.Context, method cookieMethod, body []byte) ([]byte, *http.Cookie, error) {
	frames, err := splitFrames(body)
	if err != nil {
		return nil, nil, err
	}

	var (
		message  = -1
		messages int
	)

	for i := range frames {
		if frames[i].flag&trailerFrameFlag == 0 {
			message = i
			messages++
		}
	}

	switch {
	case messages == 0:
		// A failure, which carries no token.
		return body, nil, nil
	case messages > 1:
		return nil, nil, platformerrors.New("a unary response with more than one message")
	case frames[message].flag&compressedFrameFlag != 0:
		return nil, nil, platformerrors.New("a compressed response, which cannot be read to be sealed")
	}

	response := method.response.New()
	if err = proto.Unmarshal(frames[message].payload, response.Interface()); err != nil {
		return nil, nil, platformerrors.Wrap(err, "decoding the response")
	}

	var tokens []*signinpb.IssuedToken
	collect(response, &tokens)

	switch {
	case len(tokens) == 0:
		return body, nil, nil
	case len(tokens) > 1:
		return nil, nil, platformerrors.New("a response carrying more than one refresh token, and a cookie holds one")
	}

	token := tokens[0]

	encoded, err := c.cookies.Encode(ctx, RefreshTokenCookieName, token.GetRefreshToken())
	if err != nil {
		return nil, nil, platformerrors.Wrap(err, "sealing the refresh token")
	}

	cookie := c.cookie(encoded, 0)
	if expiresAt := token.GetRefreshTokenExpiresAt(); expiresAt != nil {
		cookie.Expires = expiresAt.AsTime()
	}

	token.RefreshToken = ""

	payload, err := proto.Marshal(response.Interface())
	if err != nil {
		return nil, nil, platformerrors.Wrap(err, "re-encoding the response")
	}

	frames[message].payload = payload

	var rewritten bytes.Buffer
	for i := range frames {
		rewritten.Write(frame(frames[i].flag, frames[i].payload))
	}

	return rewritten.Bytes(), cookie, nil
}

// collect appends every IssuedToken in m carrying a refresh token, from
// singular message fields at any depth — the same fields carries looks in.
func collect(m protoreflect.Message, tokens *[]*signinpb.IssuedToken) {
	if token, ok := m.Interface().(*signinpb.IssuedToken); ok {
		if token.GetRefreshToken() != "" {
			*tokens = append(*tokens, token)
		}

		return
	}

	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() != nil && !fd.IsList() && !fd.IsMap() {
			collect(v.Message(), tokens)
		}

		return true
	})
}

// cookie is the refresh token cookie carrying value, with the attributes it
// always has. A negative maxAge deletes it.
func (c *RefreshTokenCookie) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    value,
		Path:     c.path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// withoutRefreshTokenCookie is r with the refresh token cookie taken out of
// its Cookie header, every other cookie kept, and the sealed value the cookie
// carried, or "" when it carried none.
func withoutRefreshTokenCookie(r *http.Request) (stripped *http.Request, sealed string) {
	all := r.Cookies()
	kept := make([]*http.Cookie, 0, len(all))

	for _, c := range all {
		if c.Name == RefreshTokenCookieName {
			sealed = c.Value
			continue
		}

		kept = append(kept, c)
	}

	if len(kept) == len(all) {
		return r, ""
	}

	stripped = r.Clone(r.Context())
	stripped.Header.Del("Cookie")

	for _, c := range kept {
		stripped.AddCookie(c)
	}

	return stripped, sealed
}

// isGRPCWeb is whether r is in one of the encodings the gRPC-web handler
// serves. Anything else is passed straight to it, to be refused there.
func isGRPCWeb(r *http.Request) bool {
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")

	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/grpc-web", "application/grpc-web+proto":
		return true
	default:
		return false
	}
}

// heldResponse holds what the gRPC-web handler writes, so the response can be
// rewritten before any of it reaches the client.
type heldResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(code int) {
	if h.status == 0 {
		h.status = code
	}
}

func (h *heldResponse) Write(p []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}

	return h.body.Write(p)
}

// Flush does nothing: the response is sent whole, once it has been rewritten.
func (*heldResponse) Flush() {}

type grpcWebFrame struct {
	payload []byte
	flag    byte
}

func splitFrames(body []byte) ([]grpcWebFrame, error) {
	var frames []grpcWebFrame

	for len(body) > 0 {
		if len(body) < frameHeaderSize {
			return nil, platformerrors.New("a gRPC-web body ending inside a frame header")
		}

		size := binary.BigEndian.Uint32(body[1:frameHeaderSize])
		if uint64(size) > uint64(len(body)-frameHeaderSize) {
			return nil, platformerrors.New("a gRPC-web frame running past the end of its body")
		}

		frames = append(frames, grpcWebFrame{flag: body[0], payload: body[frameHeaderSize : frameHeaderSize+size]})
		body = body[frameHeaderSize+size:]
	}

	return frames, nil
}

// singleMessage is the payload and flag of a request body that is exactly one
// message frame, which every unary gRPC-web request is.
func singleMessage(body []byte) (payload []byte, flag byte, ok bool) {
	frames, err := splitFrames(body)
	if err != nil || len(frames) != 1 || frames[0].flag&trailerFrameFlag != 0 {
		return nil, 0, false
	}

	return frames[0].payload, frames[0].flag, true
}

func frame(flag byte, payload []byte) []byte {
	framed := make([]byte, frameHeaderSize, frameHeaderSize+len(payload))
	framed[0] = flag
	binary.BigEndian.PutUint32(framed[1:], uint32(len(payload)))

	return append(framed, payload...)
}

// succeeded reports whether a gRPC-web response ended OK: its grpc-status, in
// the trailer frame, or in the headers of a trailers-only response.
func succeeded(header http.Header, body []byte) bool {
	status := header.Get("Grpc-Status")

	if frames, err := splitFrames(body); err == nil {
		for i := range frames {
			if frames[i].flag&trailerFrameFlag == 0 {
				continue
			}

			for line := range strings.SplitSeq(string(frames[i].payload), "\r\n") {
				if name, value, found := strings.Cut(line, ":"); found && strings.EqualFold(strings.TrimSpace(name), "grpc-status") {
					status = strings.TrimSpace(value)
				}
			}
		}
	}

	return status == strconv.Itoa(int(codes.OK))
}

// trailersOnly answers a call with a status and no message, in the content
// type it arrived in: the status as headers, and again as the body's trailer
// frame, as primitives-go's gRPC-web handler answers one.
//
// message must be printable ASCII with no percent sign, which grpc-message
// would otherwise have to percent-encode.
func trailersOnly(w http.ResponseWriter, r *http.Request, code codes.Code, message string) error {
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	grpcStatus := strconv.Itoa(int(code))

	h := w.Header()
	h.Set("Content-Type", strings.ToLower(strings.TrimSpace(mediaType)))
	h.Set("Grpc-Status", grpcStatus)
	h.Set("Grpc-Message", message)
	w.WriteHeader(http.StatusOK)

	_, err := w.Write(frame(trailerFrameFlag, []byte("grpc-message: "+message+"\r\ngrpc-status: "+grpcStatus+"\r\n")))

	return err
}
