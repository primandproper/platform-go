package service

import (
	"context"
	"net/http"
	"strings"

	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"

	"github.com/primandproper/primitives-go/v2/cookies"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/routing"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	"github.com/primandproper/primitives-go/v2/server/grpcweb"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/samber/do/v2"
)

var (
	// ErrGRPCWebWithoutGRPCServer is a Config naming a gRPC-web prefix and no
	// gRPC server for it to reach.
	//
	// It is refused rather than read as an absence because the prefix is the
	// deployment asking for the route, and mounting nothing would leave every
	// call a browser makes there answered 404 by a server that looks healthy.
	ErrGRPCWebWithoutGRPCServer = platformerrors.New(
		"grpcWeb is configured but grpcServer is not; gRPC-web calls are served by the gRPC server",
	)

	// ErrGRPCWebWithoutHTTPServer is a Config naming a gRPC-web prefix and no
	// HTTP server to mount it on, refused for the reason
	// ErrGRPCWebWithoutGRPCServer is.
	ErrGRPCWebWithoutHTTPServer = platformerrors.New(
		"grpcWeb is configured but httpServer is not; gRPC-web calls arrive through the HTTP server",
	)

	// ErrGRPCWebWithoutCookies is a Config naming a gRPC-web prefix, with the
	// refresh token cookie left on, and no cookie keys to seal it with.
	//
	// It is refused rather than read as the cookie switched off because the
	// cookie is the only compliant place a browser has for a refresh token, so
	// mounting the route without it would hand one to page JavaScript in every
	// sign-in's body. A deployment that wants the token in the body says so with
	// RefreshTokenCookieDisabled.
	ErrGRPCWebWithoutCookies = platformerrors.New(
		"grpcWeb seals refresh tokens into a cookie but cookies is not configured; configure it, or set grpcWeb.refreshTokenCookieDisabled",
	)
)

// GRPCWebConfig puts the gRPC server's services on the HTTP server, for
// clients that cannot speak gRPC: a browser, or React Native.
//
// Presence is the switch, as it is for every other subsystem, and the prefix
// is the whole of the block, so naming one is what turns it on. A call to
// /<prefix>/<package.Service>/<Method> is handed to primitives-go's grpcweb
// handler with the prefix stripped, which serves it through the gRPC server's
// own interceptors, stats handler and drain — so authentication, tenancy and
// error encoding read a call made this way exactly as they read one that
// arrived on the gRPC listener.
//
// What grpcweb serves is unary calls in the binary encodings. A streaming
// method is answered UNIMPLEMENTED, so the media registry's upload and the
// operation event stream stay native-gRPC-only.
//
// A refresh token is sealed into a cookie rather than returned in the body, by
// signingrpc.RefreshTokenCookie: every response on the route that carries an
// IssuedToken has its refresh_token moved into an HttpOnly, Secure,
// SameSite=Strict cookie scoped to the prefix, and every request that spends
// one and arrives with the field empty has it filled from the cookie. A browser
// has no other compliant place to keep one, since page code cannot set an
// httpOnly cookie, so it is on unless RefreshTokenCookieDisabled says
// otherwise, and it seals with the Cookies block's keys, which it therefore
// requires. That block's Lifetime is how long a browser may go between
// exchanges before the cookie stops verifying.
//
// CORS is the application's. A browser on another origin preflights every
// call here, and whether it may is a decision about which origins the
// deployment trusts, so it goes on the router as middleware beside the
// application's others, before New mounts anything.
type GRPCWebConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// PathPrefix is where the gRPC services are reached on the HTTP server,
	// such as /grpc. It begins with a slash and does not end with one.
	PathPrefix string `env:"PATH_PREFIX" json:"pathPrefix,omitempty" yaml:"pathPrefix,omitempty"`

	// RefreshTokenCookieDisabled leaves the refresh token in the response body
	// of every call on the route, for a deployment whose gRPC-web clients all
	// keep their own — a React Native app with a keychain. A browser client of
	// such a deployment has nowhere compliant to keep one.
	RefreshTokenCookieDisabled bool `env:"REFRESH_TOKEN_COOKIE_DISABLED" json:"refreshTokenCookieDisabled,omitempty" yaml:"refreshTokenCookieDisabled,omitempty"`
}

var _ validation.ValidatableWithContext = (*GRPCWebConfig)(nil)

// ValidateWithContext refuses a prefix that is not a rooted path of literal
// segments. A trailing slash would put an empty segment in every route, and a
// brace would be read by the router as a parameter.
func (cfg *GRPCWebConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.PathPrefix, validation.Required, validation.By(func(any) error {
			return validatePathPrefix(cfg.PathPrefix)
		})),
	)
}

func validatePathPrefix(prefix string) error {
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") {
		return platformerrors.New("must begin with a slash and not end with one")
	}

	if strings.ContainsAny(prefix, "{}*: \t") || strings.Contains(prefix, "//") {
		return platformerrors.New("must be literal path segments, with no route parameters, wildcards or empty segments")
	}

	return nil
}

// grpcWebRequirement is the cross-subsystem rule Config.ValidateWithContext
// applies to the GRPCWeb block, and the refusal the mount repeats for a Config
// that was handed to Register unvalidated.
func grpcWebRequirement(cfg *Config) error {
	if cfg.GRPCWeb == nil {
		return nil
	}

	if cfg.GRPCServer == nil {
		return ErrGRPCWebWithoutGRPCServer
	}

	if cfg.HTTPServer == nil {
		return ErrGRPCWebWithoutHTTPServer
	}

	if !cfg.GRPCWeb.RefreshTokenCookieDisabled && cfg.Cookies == nil {
		return ErrGRPCWebWithoutCookies
	}

	return nil
}

// grpcWebMount is the gRPC-web route, mounted. It holds nothing: the route is
// on the router by the time it exists, and the two servers it joins own their
// own start and stop. It is a type so New can build it before the HTTP server.
type grpcWebMount struct{}

// registerGRPCWeb registers the mount, which New builds before the HTTP
// server.
//
// It is built over the *grpcserver.Server the injector holds, so it serves
// whatever that server was built with — the platform's surfaces, when
// RegisterTransports ran, and the application's own services either way — and
// the refresh token cookie reads which of those methods issue or spend a
// refresh token from the same server. A Register call that was not handed a
// gRPC server, an HTTP server, or the cookie keys the cookie needs, is refused
// here by name, which is the same answer ValidateWithContext gives.
func registerGRPCWeb(i do.Injector, cfg *Config) {
	do.Provide(i, func(i do.Injector) (*grpcWebMount, error) {
		if err := grpcWebRequirement(cfg); err != nil {
			return nil, err
		}

		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		router, err := do.Invoke[*routing.Router](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking the router gRPC-web mounts on")
		}

		server, err := do.Invoke[*grpcserver.Server](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking the gRPC server gRPC-web serves")
		}

		handler := grpcweb.NewHandler(server, grpcweb.WithLogger(pillars.Logger))

		if !cfg.GRPCWeb.RefreshTokenCookieDisabled {
			cookie, cookieErr := refreshTokenCookie(i, server, cfg.GRPCWeb.PathPrefix, pillars)
			if cookieErr != nil {
				return nil, cookieErr
			}

			handler = cookie.Wrap(handler)
		}

		return mountGRPCWeb(router, cfg.GRPCWeb.PathPrefix, handler)
	})
}

// refreshTokenCookie builds the middleware that keeps the route's refresh
// tokens in a cookie, sealing with the cookie manager the Cookies block
// registered, over the methods server serves.
func refreshTokenCookie(
	i do.Injector,
	server grpcweb.Server,
	prefix string,
	pillars *observability.Pillars,
) (*signingrpc.RefreshTokenCookie, error) {
	manager, err := do.Invoke[cookies.Manager](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking the cookie manager the refresh token cookie seals with")
	}

	cookie, err := signingrpc.NewRefreshTokenCookie(manager, server.GetServiceInfo(), prefix, signingrpc.WithCookiePillars(pillars))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the gRPC-web refresh token cookie")
	}

	return cookie, nil
}

// mountGRPCWeb puts the handler on router at prefix.
//
// The route is the two segments a gRPC method's path is made of, under the
// prefix, so it means the same thing on every routing backend — none of them
// agree on a catch-all — and the prefix is stripped before the handler sees
// the request, since what the gRPC server dispatches on is that path.
//
// POST only, because that is the only method gRPC-web uses. A preflight is
// answered by whatever CORS middleware the application put on the router.
func mountGRPCWeb(router *routing.Router, prefix string, handler http.Handler) (*grpcWebMount, error) {
	router.Handle(http.MethodPost, prefix+"/{service}/{method}", http.StripPrefix(prefix, handler))

	if err := router.Err(); err != nil {
		return nil, platformerrors.Wrapf(err, "mounting gRPC-web at %s", prefix)
	}

	return &grpcWebMount{}, nil
}
