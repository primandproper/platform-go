package assembled_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	auditcfg "github.com/primandproper/platform-go/v14/audit/config"
	auditclient "github.com/primandproper/platform-go/v14/audit/grpc/client"
	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	oauth2clientsclient "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc/client"
	oauth2clientsmigrations "github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"
	passwordresetmigrations "github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	refreshtokenmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/billing"
	billingcfg "github.com/primandproper/platform-go/v14/billing/config"
	billingclient "github.com/primandproper/platform-go/v14/billing/grpc/client"
	billingmigrations "github.com/primandproper/platform-go/v14/billing/migrations"
	"github.com/primandproper/platform-go/v14/callers"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	commentsclient "github.com/primandproper/platform-go/v14/comments/grpc/client"
	commentsmigrations "github.com/primandproper/platform-go/v14/comments/migrations"
	"github.com/primandproper/platform-go/v14/conformance"
	conformanceall "github.com/primandproper/platform-go/v14/conformance/all"
	dataprivacycfg "github.com/primandproper/platform-go/v14/dataprivacy/config"
	dataprivacymigrations "github.com/primandproper/platform-go/v14/dataprivacy/migrations"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	issuereportsclient "github.com/primandproper/platform-go/v14/issuereports/grpc/client"
	issuereportsmigrations "github.com/primandproper/platform-go/v14/issuereports/migrations"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	mediaregistrymigrations "github.com/primandproper/platform-go/v14/mediaregistry/migrations"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	notificationsclient "github.com/primandproper/platform-go/v14/notifications/grpc/client"
	notificationsmigrations "github.com/primandproper/platform-go/v14/notifications/migrations"
	operationsmigrations "github.com/primandproper/platform-go/v14/operations/migrations"
	"github.com/primandproper/platform-go/v14/service"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	settingsclient "github.com/primandproper/platform-go/v14/settings/grpc/client"
	settingsmigrations "github.com/primandproper/platform-go/v14/settings/migrations"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"
	waitlistsclient "github.com/primandproper/platform-go/v14/waitlists/grpc/client"
	waitlistsmigrations "github.com/primandproper/platform-go/v14/waitlists/migrations"
	webhookscfg "github.com/primandproper/platform-go/v14/webhooks/config"
	webhooksclient "github.com/primandproper/platform-go/v14/webhooks/grpc/client"
	webhooksmigrations "github.com/primandproper/platform-go/v14/webhooks/migrations"
	workqueuemigrations "github.com/primandproper/platform-go/v14/workqueue/migrations"

	tokenscfg "github.com/primandproper/primitives-go/v2/authentication/tokens/config"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/encoding"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"
	routingcfg "github.com/primandproper/primitives-go/v2/routing/config"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	httpserver "github.com/primandproper/primitives-go/v2/server/http"
	"github.com/primandproper/primitives-go/v2/tenancy"
	uploadscfg "github.com/primandproper/primitives-go/v2/uploads/config"
	"github.com/primandproper/primitives-go/v2/uploads/objectstorage"

	"github.com/samber/do/v2"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// The metadata this harness's stand-in credential travels in. Metadata rather
// than a context value, because a context value does not cross a connection.
const (
	mdUserID  = "conformance-user-id"
	mdScope   = "conformance-scope"
	mdAccount = "conformance-account-id"
)

// auditedResourceType is what this harness's auditable action touches.
const auditedResourceType = "conformance_audited"

// bootTimeout bounds how long a service may take to bind. It is generous because
// a container that has only just come up is still warming its first connections.
const bootTimeout = 30 * time.Second

// prefixCounter keeps each run's tables apart, which is what lets the dialects
// share one server per test binary.
var prefixCounter atomic.Uint64

// assemble boots a service over db the way a consumer's main does, and runs every
// suite against it.
//
// db is the only thing that varies between dialects. Everything else — which
// surfaces mount, what the server is built from, the order it comes up in — is
// the composition root's, which is what this subject exists to put under test.
func assemble(t *testing.T, db *databasecfg.Config, d dialect.Dialect) {
	t.Helper()

	prefix := fmt.Sprintf("asm_%d", prefixCounter.Add(1))

	cfg := &service.Config{
		Name:     "conformance",
		Database: db,
		// Port 0 is an ephemeral port. MaxReceiveMessageSize is spelled out at
		// its default only so the block survives normalization; the package
		// documentation says why.
		GRPCServer: &grpcserver.Config{MaxReceiveMessageSize: grpcserver.DefaultMaxMessageSize},
		Tokens:     tokenConfig(t),

		// The HTTP lane: a router, the encoding it speaks, and a server on an
		// ephemeral port. StartupDeadline is spelled out for the reason
		// MaxReceiveMessageSize is above — a block holding only Port: 0 is
		// released as unconfigured.
		HTTPServer: &httpserver.Config{StartupDeadline: bootTimeout},
		Routing: &routingcfg.Config{
			Provider: routingcfg.ProviderChi,
			Chi:      &chi.Config{ServiceName: "conformance", SilenceRouteLogging: true},
		},
		Encoding: &encoding.Config{ContentType: "application/json"},
		Uploads: &uploadscfg.Config{Storage: objectstorage.Config{
			Provider:   objectstorage.MemoryProvider,
			BucketName: "conformance",
		}},

		// Every surface that has a config block, each under the run's prefix.
		Audit:         &auditcfg.Config{Dialect: d, TablePrefix: prefix},
		Billing:       &billingcfg.Config{TablePrefix: prefix},
		Comments:      &commentscfg.Config{TablePrefix: prefix},
		Identity:      &identitycfg.Config{TablePrefix: prefix},
		IssueReports:  &issuereportscfg.Config{TablePrefix: prefix},
		Notifications: &notificationscfg.Config{TablePrefix: prefix},
		Settings:      &settingscfg.Config{TablePrefix: prefix},
		Waitlists:     &waitlistscfg.Config{TablePrefix: prefix},
		Webhooks:      &webhookscfg.Config{TablePrefix: prefix},

		// And the HTTP surface every dialect can serve.
		MediaRegistry: &mediaregistrycfg.Config{TablePrefix: prefix},
	}

	// operations runs on a work queue that claims with SKIP LOCKED, which is
	// Postgres's alone (the README's matrix says so), and dataprivacy fulfills
	// its requests as operations — service.Config refuses the second without
	// the first. So both HTTP surfaces are mounted on Postgres and absent
	// elsewhere, and the HTTP flags below say which, so the anonymous suite
	// asserts the routes a dialect actually serves rather than failing on ones
	// it cannot.
	servesOperations := d == dialect.Postgres
	if servesOperations {
		cfg.Operations = operationsConfig(prefix)
		cfg.DataPrivacy = &dataprivacycfg.Config{Dialect: d, TablePrefix: prefix}
	}
	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	i := do.New()
	do.ProvideValue(i, t.Context())

	service.Register(i, cfg)

	// What a consumer's main adds beyond the config: the declarations and
	// services Register does not build, the interceptors the gRPC server
	// resolves, the extractor, and the rules about rows.
	registerApplication(i, prefix)

	// The consumer's identity hooks, which is where an invitation's token goes
	// to be mailed. identity/config resolves them when it builds the service.
	invites := &invitationTokens{}
	do.ProvideValue[identity.Hooks](i, invites)

	// And the consumer's reset mailer, which is where a reset link goes.
	mailbox := &resetMailbox{}
	do.ProvideValue(i, mailbox)

	// And the consumer's sign-in link mailer.
	links := &magicLinkMailbox{}
	do.ProvideValue(i, links)
	do.ProvideValue(i, []grpc.UnaryServerInterceptor{
		grpcerrors.UnaryErrorEncodingInterceptor(),
		authenticate,
	})
	do.ProvideValue(i, []grpc.StreamServerInterceptor{})
	// The HTTP half of the stand-in credential, on the router before anything
	// mounts on it: chi refuses middleware added after the first route, which
	// is a constraint a consumer's main meets in the same place.
	do.MustInvoke[*routing.Router](i).Use(authenticateHTTP)

	service.RegisterTransports(i, &service.Transports{
		Extractor:   extractPrincipal,
		Authorizers: authorizers(),
	})

	svc, err := service.New(i)
	must.NoError(t, err)

	client := do.MustInvoke[database.Client](i)
	migrate(t, client, d, prefix)

	addrs := run(t, svc, do.MustInvoke[*grpcserver.Server](i), do.MustInvoke[*httpserver.APIServer](i))
	conn := dial(t, addrs.grpc)
	baseURL := "http://" + loopback(t, addrs.http)

	identitySvc := do.MustInvoke[*identity.Service](i)
	identityStore := do.MustInvoke[identity.Store](i)
	recorder := do.MustInvoke[audit.Recorder](i)

	// Every surface, on the one connection. passwordreset ships no client
	// wrapper, so it is the generated interface directly.
	surfaces := conformance.Surfaces{
		Audit:         auditclient.Wrap(conn),
		Billing:       billingclient.Wrap(conn),
		Comments:      commentsclient.Wrap(conn),
		Identity:      identityclient.Wrap(conn),
		IssueReports:  issuereportsclient.Wrap(conn),
		Notifications: notificationsclient.Wrap(conn),
		OAuth2Clients: oauth2clientsclient.Wrap(conn),
		PasswordReset: passwordresetpb.NewPasswordResetServiceClient(conn),
		Settings:      settingsclient.Wrap(conn),
		SignIn:        signinclient.Wrap(conn),
		Waitlists:     waitlistsclient.Wrap(conn),
		Webhooks:      webhooksclient.Wrap(conn),
	}

	conformanceall.Run(t, conformance.Seams{
		NewSubject: func(ctx context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
			req := conformance.NewSubjectRequest(opts...)

			// No administrative caller: service roles are a deployment's to
			// define, and a stand-in that invented one would be asserted against.
			if req.Admin {
				return nil, conformance.ErrSubjectUnsupported
			}

			scope := tenancy.Of(identifiers.New())
			if req.Scope != nil {
				scope = *req.Scope
			}

			// Through the service rather than the surface: every identity RPC
			// requires a caller, Register included, so there is no client-only
			// way to mint the first one.
			reg, registerErr := identitySvc.Register(ctx, scope,
				// In good standing, because a subject is a signed-in caller
				// and an unverified account is one sign-in does not admit:
				// a caller registered as one could not exist in a
				// deployment, and the principal read says so.
				&identity.User{
					Username:      "conf_" + identifiers.New(),
					EmailAddress:  identifiers.New() + "@conformance.invalid",
					AccountStatus: identity.StatusGood,
				},
				&identity.Account{Name: "conf_" + identifiers.New()},
				[]string{"account_admin"})
			if registerErr != nil {
				return nil, registerErr
			}

			return &conformance.Subject{
				Scope:     scope,
				UserID:    reg.User.ID,
				AccountID: reg.Account.ID,
				Conn:      conn,
				Surfaces:  surfaces,
				HTTP: &conformance.HTTPSurfaces{
					Client: &http.Client{Transport: &credentialTransport{
						userID: reg.User.ID, scope: scope, accountID: reg.Account.ID,
					}},
					BaseURL:       baseURL,
					DataPrivacy:   servesOperations,
					MediaRegistry: true,
					Operations:    servesOperations,
				},
				// The scope travels as its owner identifier rather than its
				// String, which is prose: String renders the global scope as a
				// placeholder, and the interceptor would read that back as a
				// tenant of that name rather than as the scope belonging to
				// nobody.
				Decorate: func(ctx context.Context) context.Context {
					return metadata.NewOutgoingContext(ctx, metadata.Pairs(
						mdUserID, reg.User.ID,
						mdScope, scope.Owner(),
						mdAccount, reg.Account.ID,
					))
				},
			}, nil
		},

		Actions: conformance.Actions{
			InvitationToken:    invites.token,
			EmailVerified:      verifyEmail(client, identityStore),
			Subscribed:         subscribe(client, do.MustInvoke[billing.Store](i)),
			PasswordResetToken: mailbox.token,
			VerificationToken:  invites.verificationToken,
			MagicLinkToken:     links.token,

			// The recorder the composition root built, inside a transaction on
			// the client it built — the end of the path a consumer's handler
			// takes, since no surface in this module records an entry itself.
			Auditable: func(ctx context.Context, scope tenancy.Scope) (*conformance.Audited, error) {
				entry := &audit.Entry{
					Scope:        scope,
					EventType:    audit.EventType("conformance.acted"),
					ResourceType: auditedResourceType,
					ResourceID:   identifiers.New(),
					Actor:        audit.Actor{ID: identifiers.New(), Type: audit.ActorUser},
				}

				if recordErr := client.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
					return recorder.Record(ctx, tx, scope, entry)
				}); recordErr != nil {
					return nil, recordErr
				}

				return &conformance.Audited{
					ResourceType: entry.ResourceType,
					ResourceID:   entry.ResourceID,
					ActorID:      entry.Actor.ID,
				}, nil
			},

			Credentialed: func(ctx context.Context, scope tenancy.Scope, userID string) (string, error) {
				const hash = "$argon2id$v=19$m=65536,t=3,p=2$Y29uZm9ybWFuY2U$notarealsecret"

				if writeErr := client.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
					return identityStore.UpdateUserPassword(ctx, tx, scope, userID, hash)
				}); writeErr != nil {
					return "", writeErr
				}

				return "notarealsecret", nil
			},
		},

		// This harness's credential is per call, so a caller with none is the
		// same connection without the metadata.
		Anonymous: func(context.Context) (grpc.ClientConnInterface, error) {
			return conn, nil
		},
		AnonymousHTTP: func(context.Context) (*http.Client, error) {
			return http.DefaultClient, nil
		},

		// The one target type registerApplication declares.
		CommentTargetType: "conformance_thing",

		Dialect: d,

		// Every table is this run's own, by prefix.
		ExclusiveDatabase: true,

		// service mounts waitlists with its default scope resolver, which is
		// the single-tenant answer: a visitor is in the global directory.
		VisitorScope: new(tenancy.Global()),
	})
}

// tokenConfig is a JWT issuer with a key minted for this run, which is what
// signin's service signs with. Nothing asserts on a token's contents yet; the
// key is random so that no two runs could accept each other's.
func tokenConfig(t *testing.T) *tokenscfg.Config {
	t.Helper()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	must.NoError(t, err)

	return &tokenscfg.Config{
		Provider:                tokenscfg.ProviderJWT,
		Issuer:                  "conformance",
		Audience:                "conformance",
		Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(key),
	}
}

// boundAddrs are where the two servers bound.
type boundAddrs struct {
	grpc net.Addr
	http net.Addr
}

// run starts svc and returns the addresses its servers bound, stopping it when
// the test ends.
func run(t *testing.T, svc *service.Service, srv *grpcserver.Server, httpSrv *httpserver.APIServer) boundAddrs {
	t.Helper()

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the assembled service did not stop cleanly: %v", err)
			}
		case <-time.After(bootTimeout):
			t.Error("the assembled service did not stop within the boot timeout")
		}
	})

	bootCtx, bootCancel := context.WithTimeout(t.Context(), bootTimeout)
	defer bootCancel()

	var addrs boundAddrs

	for name, wait := range map[string]func(context.Context) (net.Addr, error){
		"gRPC": func(ctx context.Context) (net.Addr, error) { return srv.Addr(ctx) },
		"HTTP": func(ctx context.Context) (net.Addr, error) { return httpSrv.Addr(ctx) },
	} {
		addr, err := wait(bootCtx)
		if err != nil {
			// A service that stopped before binding says why on done.
			select {
			case runErr := <-done:
				t.Fatalf("the assembled service stopped before its %s server bound: %v (%v)", name, runErr, err)
			default:
				t.Fatalf("waiting for the assembled %s server to bind: %v", name, err)
			}
		}

		if name == "gRPC" {
			addrs.grpc = addr
		} else {
			addrs.http = addr
		}
	}

	return addrs
}

// loopback is the port a server bound, on loopback. The servers listen on every
// interface, so their addresses name none; the port is what is dialed.
func loopback(t *testing.T, addr net.Addr) string {
	t.Helper()

	tcp, ok := addr.(*net.TCPAddr)
	must.True(t, ok, must.Sprintf("a server bound %T, not a TCP address", addr))

	return net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
}

// dial connects to the port the gRPC server bound.
func dial(t *testing.T, addr net.Addr) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(loopback(t, addr),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Every client in this module installs the same chain, and exports it
		// for a connection shared between services.
		identityclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// migrate renders each mounted package's schema under the run's prefix, on the
// client the service built — which is what a consumer's migration step does,
// since nothing in service runs one.
func migrate(t *testing.T, db database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	for name, render := range map[string]func(dialect.Dialect, string) ([]string, error){
		"audit":          auditmigrations.Statements,
		"billing":        billingmigrations.Statements,
		"comments":       commentsmigrations.Statements,
		"identity":       identitymigrations.Statements,
		"issue reports":  issuereportsmigrations.Statements,
		"notifications":  notificationsmigrations.Statements,
		"oauth2 clients": oauth2clientsmigrations.Statements,
		"password reset": passwordresetmigrations.Statements,
		"settings":       settingsmigrations.Statements,
		"waitlists":      waitlistsmigrations.Statements,
		"webhooks":       webhooksmigrations.Statements,
		"media registry": mediaregistrymigrations.Statements,
		"magic links":    magiclinkmigrations.Statements,
		"refresh tokens": refreshtokenmigrations.Statements,
	} {
		stmts, err := render(d, prefix)
		must.NoError(t, err, must.Sprintf("rendering %s's migrations", name))

		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("migrating %s: %q", name, stmt))
		}
	}

	if d != dialect.Postgres {
		return
	}

	// The Postgres-only packages: operations, the queue it runs on, and
	// dataprivacy, whose service needs both.
	for name, render := range map[string]func(dialect.Dialect, string) ([]string, error){
		"data privacy": dataprivacymigrations.Statements,
		"operations":   operationsmigrations.Statements,
		"work queue":   workqueuemigrations.Statements,
	} {
		stmts, err := render(d, prefix)
		must.NoError(t, err, must.Sprintf("rendering %s's migrations", name))

		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("migrating %s: %q", name, stmt))
		}
	}
}

// authenticate is this harness's stand-in for a consumer's authentication
// interceptor. It leaves an anonymous request alone rather than refusing it,
// because whether one is refused is each surface's decision, and an interceptor
// that refused here would make the anonymous suite assert this function.
func authenticate(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return handler(ctx, req)
	}

	userIDs := md.Get(mdUserID)
	if len(userIDs) == 0 || userIDs[0] == "" {
		return handler(ctx, req)
	}

	principal := &testPrincipal{userID: userIDs[0], scope: tenancy.Global()}

	if owners := md.Get(mdScope); len(owners) > 0 && owners[0] != "" {
		principal.scope = tenancy.Of(owners[0])
	}

	if accounts := md.Get(mdAccount); len(accounts) > 0 {
		principal.activeAccountID = accounts[0]
	}

	return handler(context.WithValue(ctx, principalKey{}, principal), req)
}

// authenticateHTTP is authenticate's counterpart on the router: the same
// stand-in credential, read off headers instead of metadata, and the same
// refusal to refuse — a request with no credential passes through with nobody
// on it, and each surface decides what that means.
func authenticateHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get(mdUserID)
		if userID == "" {
			next.ServeHTTP(w, r)

			return
		}

		principal := &testPrincipal{userID: userID, scope: tenancy.Global(), activeAccountID: r.Header.Get(mdAccount)}
		if owner := r.Header.Get(mdScope); owner != "" {
			principal.scope = tenancy.Of(owner)
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

// credentialTransport puts one subject's stand-in credential on every request,
// which is what a consumer's authenticated HTTP client does with a cookie or a
// bearer token.
type credentialTransport struct {
	userID    string
	accountID string
	scope     tenancy.Scope
}

func (c *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(mdUserID, c.userID)
	req.Header.Set(mdScope, c.scope.Owner())
	req.Header.Set(mdAccount, c.accountID)

	return http.DefaultTransport.RoundTrip(req)
}

type principalKey struct{}

func extractPrincipal(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

type testPrincipal struct {
	userID          string
	activeAccountID string
	scope           tenancy.Scope
}

var _ callers.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return p.activeAccountID }
