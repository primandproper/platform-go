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
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditcfg "github.com/primandproper/platform-go/v14/audit/config"
	auditclient "github.com/primandproper/platform-go/v14/audit/grpc/client"
	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	oauth2clientsclient "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc/client"
	oauth2clientsmigrations "github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	passkeyscfg "github.com/primandproper/platform-go/v14/authentication/passkeys/config"
	passkeysclient "github.com/primandproper/platform-go/v14/authentication/passkeys/grpc/client"
	passkeysmigrations "github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	passwordresetmigrations "github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	signincfg "github.com/primandproper/platform-go/v14/authentication/signin/config"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	recoverycodemigrations "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"
	refreshtokenmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/authentication/webauthnsessions"
	webauthnsessionscfg "github.com/primandproper/platform-go/v14/authentication/webauthnsessions/config"
	webauthnsessionsmigrations "github.com/primandproper/platform-go/v14/authentication/webauthnsessions/migrations"
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billingcfg "github.com/primandproper/platform-go/v14/billing/config"
	billingclient "github.com/primandproper/platform-go/v14/billing/grpc/client"
	billingmigrations "github.com/primandproper/platform-go/v14/billing/migrations"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	commentsclient "github.com/primandproper/platform-go/v14/comments/grpc/client"
	commentsmigrations "github.com/primandproper/platform-go/v14/comments/migrations"
	"github.com/primandproper/platform-go/v14/conformance"
	conformanceall "github.com/primandproper/platform-go/v14/conformance/all"
	conformancereservations "github.com/primandproper/platform-go/v14/conformance/reservations"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacycfg "github.com/primandproper/platform-go/v14/dataprivacy/config"
	dataprivacymigrations "github.com/primandproper/platform-go/v14/dataprivacy/migrations"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	issuereportsclient "github.com/primandproper/platform-go/v14/issuereports/grpc/client"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	issuereportsmigrations "github.com/primandproper/platform-go/v14/issuereports/migrations"
	linksmigrations "github.com/primandproper/platform-go/v14/links/database/migrations"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	mediaregistrymigrations "github.com/primandproper/platform-go/v14/mediaregistry/migrations"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	notificationsclient "github.com/primandproper/platform-go/v14/notifications/grpc/client"
	notificationsmigrations "github.com/primandproper/platform-go/v14/notifications/migrations"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	operationsmigrations "github.com/primandproper/platform-go/v14/operations/migrations"
	"github.com/primandproper/platform-go/v14/service"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	settingsclient "github.com/primandproper/platform-go/v14/settings/grpc/client"
	settingsmigrations "github.com/primandproper/platform-go/v14/settings/migrations"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	waitlistsclient "github.com/primandproper/platform-go/v14/waitlists/grpc/client"
	waitlistsmigrations "github.com/primandproper/platform-go/v14/waitlists/migrations"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	webhookscfg "github.com/primandproper/platform-go/v14/webhooks/config"
	webhooksclient "github.com/primandproper/platform-go/v14/webhooks/grpc/client"
	webhooksmigrations "github.com/primandproper/platform-go/v14/webhooks/migrations"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"
	workqueuemigrations "github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/tokens"
	tokenscfg "github.com/primandproper/primitives-go/v2/authentication/tokens/config"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
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
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadscfg "github.com/primandproper/primitives-go/v2/uploads/config"
	"github.com/primandproper/primitives-go/v2/uploads/objectstorage"

	"github.com/samber/do/v2"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

// mdReserving says which of the harness's two runs a caller was minted in: the
// one reserving staffOnly, or the one whose members make every call. The server
// is one server either way, so the reservation rides beside the credential, in
// metadata, the way a deployment flag a consumer's interceptor reads would.
//
// It is the only thing that does. Who a caller is travels as a bearer token the
// sign-in service minted, read back by signingrpc's extractor — the one a
// consumer installs — rather than as a stand-in this harness invented.
const mdReserving = "conformance-reserving"

// adminServiceRole is the service role an administrator subject holds, and the
// one the sign-in block's administrative door admits. An administrator's token
// is minted through that door, which is what makes the role reach grantsOf: the
// extractor withholds service roles from an ordinary-door token.
const adminServiceRole = "conformance_admin"

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
// db is the only thing that varies between dialects, besides whether the
// waitlist confirmation loop runs — see waitlistConfirmation. Everything else —
// which surfaces mount, what the server is built from, the order it comes up in
// — is the composition root's, which is what this subject exists to put under
// test.
func assemble(t *testing.T, db *databasecfg.Config, d dialect.Dialect, waitlists waitlistConfirmation) {
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
		Identity:      &identitycfg.Config{TablePrefix: prefix, ReturnInvitationToken: true},
		IssueReports:  &issuereportscfg.Config{TablePrefix: prefix},
		Notifications: &notificationscfg.Config{TablePrefix: prefix},
		Settings:      &settingscfg.Config{TablePrefix: prefix},
		Waitlists:     &waitlistscfg.Config{TablePrefix: prefix},
		Webhooks:      &webhookscfg.Config{TablePrefix: prefix},

		// Sign-in with every door the suites knock on. Rotation, recovery codes
		// and registration are on by default. The passwordless door is the
		// one a block switches on.
		SignIn: &signincfg.Config{
			TOTPIssuer:        "conformance",
			AdminServiceRoles: []string{adminServiceRole},
			RefreshTokens:     signincfg.RefreshTokensConfig{TablePrefix: prefix},
			RecoveryCodes:     signincfg.RecoveryCodesConfig{TablePrefix: prefix},
			MagicLinks:        &signincfg.MagicLinksConfig{TablePrefix: prefix},
		},

		// Passkeys, over a relying party whose ceremony state is the SQL table,
		// so a login's challenge is spent on every dialect rather than in a
		// process's memory. The origin is the one the virtual authenticator
		// answers from; see passkeyRelyingParty.
		WebAuthn: &webauthnsessionscfg.Config{
			Provider:     webauthnsessionscfg.ProviderDatabase,
			Database:     webauthnsessions.Config{TablePrefix: prefix},
			RelyingParty: passkeyRelyingParty(),
		},
		Passkeys: &passkeyscfg.Config{TablePrefix: prefix},

		// And the HTTP surfaces, on every dialect. dataprivacy fulfills its
		// requests as operations, and service.Config refuses the first without
		// the second.
		MediaRegistry: &mediaregistrycfg.Config{TablePrefix: prefix},
		Operations:    operationsConfig(prefix),
		DataPrivacy:   &dataprivacycfg.Config{Dialect: d, TablePrefix: prefix},
	}
	if waitlists == confirmsWaitlists {
		cfg.Links = waitlistLinksConfig(prefix)
	}

	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	i := do.New()
	do.ProvideValue(i, t.Context())

	service.Register(i, cfg)

	// What a consumer's main adds beyond the config: the declarations and
	// services Register does not build, the interceptors the gRPC server
	// resolves, the extractor, and the rules about rows.
	commentable := &things{}
	people := &directories{}
	registerApplication(i, prefix, commentable, people)
	registerPasskeyApplication(i)

	// The consumer's identity hooks, which is where an invitation's token goes
	// to be mailed. identity/config resolves them when it builds the service.
	invites := &invitationTokens{}
	do.ProvideValue[identity.Hooks](i, invites)

	// The same value is the consumer's verification mailer, so a resent link
	// lands where the registration's did and the action reads the newest.
	do.ProvideValue[signin.VerificationMailer](i, invites)

	// And the consumer's reset mailer, which is where a reset link goes.
	mailbox := &resetMailbox{}
	do.ProvideValue(i, mailbox)

	// And the consumer's sign-in link mailer, under the key the sign-in block
	// resolves as well as its own, which is where the sign-in suite reads the
	// link a person would have been sent.
	links := &magicLinkMailbox{}
	do.ProvideValue(i, links)
	do.ProvideValue[signin.MagicLinkMailer](i, links)

	// And the consumer's handle reminder mailer, whose presence is what turns
	// that door on — it has no block of its own.
	reminders := &handleReminderMailbox{}
	do.ProvideValue[signin.HandleReminderMailer](i, reminders)

	// And the consumer's registration policy, which refuses a registrant who
	// has not accepted every agreement — so the sign-in suite runs against a
	// deployment that requires them.
	do.ProvideValue[signin.RegistrationPolicy](i, requireAgreements)

	// And, on a run that confirms, the consumer's waitlist confirmation mailer,
	// whose presence is what mounts the loop — over the minter the Links block
	// above registered.
	waitlistMail := &waitlistMailbox{}
	if waitlists == confirmsWaitlists {
		do.ProvideValue[waitlistsgrpc.ConfirmationMailer](i, waitlistMail)
	}
	// The sign-in extractor, with this harness's role policy on it, installed
	// the way a consumer's main installs it: its interceptor in the chain, its
	// middleware on the router, and it named to Transports as the extractor
	// and the grants every surface reads. service builds none of this.
	//
	// It checks each token's login as well, which is the per-request read a
	// deployment buys immediate revocation with, and what lets this harness
	// declare Seams.ImmediateRevocation.
	extractor, err := signingrpc.NewPrincipalExtractor(
		do.MustInvoke[tokens.Issuer](i),
		do.MustInvoke[database.Client](i),
		do.MustInvoke[identity.Store](i),
		signingrpc.WithGrants(grantsOf),
		signingrpc.WithSignInCheck(do.MustInvoke[*signin.Service](i)),
	)
	must.NoError(t, err)

	do.ProvideValue(i, []grpc.UnaryServerInterceptor{
		grpcerrors.UnaryErrorEncodingInterceptor(),
		extractor.UnaryServerInterceptor(authenticationRequirements(t)),
		reserveStaffCalls(extractor),
	})
	do.ProvideValue(i, []grpc.StreamServerInterceptor{})
	// The HTTP half, on the router before anything mounts on it: chi refuses
	// middleware added after the first route, which is a constraint a
	// consumer's main meets in the same place.
	do.MustInvoke[*routing.Router](i).Use(extractor.HTTPMiddleware, markReserving)

	// And the HTTP half of authorization, which the three HTTP surfaces check
	// their routes with. A consumer builds it over the grants its interceptor
	// reads; this one's are the role policy, narrowed in the reserving run.
	httpEnforcer, err := authzhttp.NewEnforcer(httpGrants(extractor))
	must.NoError(t, err)

	service.RegisterTransports(i, &service.Transports{
		Extractor:    extractor.Extract,
		TenantOf:     service.DirectoryTenant,
		Grants:       extractor.Grants,
		HTTPEnforcer: httpEnforcer,
		Authorizers:  authorizers(),
	})

	svc, err := service.New(i)
	must.NoError(t, err)

	client := do.MustInvoke[database.Client](i)
	migrate(t, client, d, prefix, waitlists)

	addrs := run(t, svc, do.MustInvoke[*grpcserver.Server](i), do.MustInvoke[*httpserver.APIServer](i))
	conn := dial(t, addrs.grpc)
	baseURL := "http://" + loopback(t, addrs.http)

	identitySvc := do.MustInvoke[*identity.Service](i)
	identityStore := do.MustInvoke[identity.Store](i)
	signIn := do.MustInvoke[*signin.Service](i)
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
		Passkeys:      passkeysclient.Wrap(conn),
		PasswordReset: passwordresetpb.NewPasswordResetServiceClient(conn),
		Settings:      settingsclient.Wrap(conn),
		SignIn:        signinclient.Wrap(conn),
		Waitlists:     waitlistsclient.Wrap(conn),
		Webhooks:      webhooksclient.Wrap(conn),
	}

	// Every run against this server is one of these, differing only in what
	// it reserves.
	seams := func(reserved, reservedRoutes []string) conformance.Seams {
		reserving := strconv.FormatBool(len(reserved) > 0 || len(reservedRoutes) > 0)

		return conformance.Seams{
			OperatorMethods: reserved,
			OperatorRoutes:  reservedRoutes,

			NewSubject: func(ctx context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
				req := conformance.NewSubjectRequest(opts...)

				scope := tenancy.Of(identifiers.New())
				if req.Scope != nil {
					scope = *req.Scope
				}

				// An hour back, so a stamp a suite's own call writes is never
				// mistaken for the one registration wrote — SQLite keeps whole
				// seconds, and a stamp from this second would be both.
				agreedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

				// An administrator holds the service role the administrative door
				// admits, and nobody else holds any.
				var serviceRoles []string
				if req.Admin {
					serviceRoles = []string{adminServiceRole}
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
						// And having agreed to both documents, as a registration
						// that collects acceptance at sign-up writes them, so no
						// suite can read "nothing changed" as "still unset".
						LastAcceptedTermsOfService: &agreedAt,
						LastAcceptedPrivacyPolicy:  &agreedAt,
						ServiceRoles:               serviceRoles,
					},
					&identity.Account{Name: "conf_" + identifiers.New()},
					[]string{"owner"})
				if registerErr != nil {
					return nil, registerErr
				}

				people.remember(reg.User.ID, scope)

				// The subject's credential is a token the sign-in service minted
				// for them, the way a sign-in would have. An administrator's comes
				// through the administrative door, which is what their service
				// role — and so adminRole, the grants the surfaces ask inside a
				// handler — rides on: the extractor keeps it off an
				// ordinary-door token. The credential is taken to have been two
				// factors, as a verified passkey is, which the administrative door
				// requires.
				issueOpts := []signin.IssueOption{signin.MultiFactor()}
				if req.Admin {
					issueOpts = append(issueOpts, signin.Administrative())
				}

				issued, issueErr := signIn.IssueForPrincipal(ctx, scope, reg.User.ID, reg.Account.ID, issueOpts...)
				if issueErr != nil {
					return nil, issueErr
				}

				return &conformance.Subject{
					Scope:     scope,
					UserID:    reg.User.ID,
					AccountID: reg.Account.ID,
					Conn:      conn,
					Surfaces:  surfaces,
					HTTP: &conformance.HTTPSurfaces{
						Client:        &http.Client{Transport: &credentialTransport{token: issued.Token, reserving: reserving}},
						BaseURL:       baseURL,
						DataPrivacy:   true,
						MediaRegistry: true,
						Operations:    true,
					},
					Decorate: func(ctx context.Context) context.Context {
						md := metadata.Pairs(
							"authorization", "Bearer "+issued.Token,
							mdReserving, reserving,
						)

						return metadata.NewOutgoingContext(ctx, md)
					},
				}, nil
			},

			Actions: conformance.Actions{
				InvitationToken:    invites.token,
				EmailVerified:      verifyEmail(client, identityStore),
				Subscribed:         subscribe(client, do.MustInvoke[billing.Store](i)),
				PasswordResetToken: mailbox.token,
				Notified:           notify(client, do.MustInvoke[notifications.Inbox](i)),
				VerificationToken:  invites.verificationToken,
				MagicLinkToken:     links.token,
				HandleReminder:     reminders.handle,
				WaitlistLinks:      waitlistLinks(waitlists, waitlistMail),
				Registered: register(client,
					do.MustInvoke[uploads.UploadManager](i), do.MustInvoke[mediaregistry.Store](i)),
				CommentTarget: commentable.bring,
				ArtifactExpired: expireArtifact(client,
					do.MustInvoke[dataprivacy.Store](i), do.MustInvoke[uploads.UploadManager](i)),

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

			// And a caller the suite signed in itself is the same connection
			// with that token on every call, read back by the extractor as any
			// minted subject's is — the contract's default Authorizer.
			SignedIn: func(_ context.Context, issued *signinpb.IssuedToken) (grpc.ClientConnInterface, error) {
				return &bearerConn{ClientConnInterface: conn, token: issued.GetToken(), reserving: reserving}, nil
			},

			// The extractor above checks every token's login on every request.
			ImmediateRevocation: true,

			// The one target type registerApplication declares, for the reads that
			// name a target without writing to it. Its writes go through
			// CommentTarget, since the type checks that a target exists.
			CommentTargetType: string(thingType),

			// The identity block above returns an invitation's token to its
			// sender, so the suites assert that reading here and the
			// identity harness, built on the server's default, asserts the
			// other.
			InvitationTokenReturned: true,

			Dialect: d,

			// The one role the sign-in block's administrative door admits.
			Roles: conformance.Roles{Administrator: adminServiceRole},

			// service mounts waitlists with its default scope resolver, which is
			// the single-tenant answer: a visitor is in the global directory.
			VisitorScope: new(tenancy.Global()),

			// The relying party the WebAuthn block above verifies against.
			WebAuthn: &conformance.WebAuthnDeployment{
				RPID:   passkeyRelyingParty().RPID,
				Origin: passkeyRelyingParty().RPOrigins[0],
			},
		}
	}

	// Twice, because which calls a deployment keeps from its members is the
	// deployment's to decide, and the suites have to be right either way.
	//
	// The first run is this module's own answer, where a member holds every
	// grant but the archive ones and so makes every call: it is what keeps each
	// promise asserted of a member. The second reserves staffOnly, which
	// reserveStaffCalls refuses to anybody but an administrator, and
	// staffOnlyRoutes, whose permissions httpGrants withholds from a member: it is what
	// keeps the path a consumer's reservation takes exercised, each reserved
	// call made by an administrator minted for it and each assertion about a
	// member of a reserved call skipping rather than failing. That every call a
	// caller makes is one it declared is checked in both, on the caller's own
	// connection. Sequential rather than parallel, because each claims the
	// database as its own.
	t.Run("members make every call", func(t *testing.T) {
		conformanceall.Run(t, seams(nil, nil))
	})

	t.Run("staff calls reserved", func(t *testing.T) {
		conformanceall.Run(t, seams(staffOnly, staffOnlyRoutes))
	})

	// And the record the reservations suite skips by, held to the handlers it
	// describes, which here sit behind this module's own authorizers.
	t.Run("empty requests refused", func(t *testing.T) {
		conformance.Run(t, seams(nil, nil), conformancereservations.RosterSuite())
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
func migrate(t *testing.T, db database.Client, d dialect.Dialect, prefix string, waitlists waitlistConfirmation) {
	t.Helper()

	schemas := map[string]func(dialect.Dialect, string) ([]string, error){
		"audit":          auditmigrations.Statements,
		"billing":        billingmigrations.Statements,
		"comments":       commentsmigrations.Statements,
		"identity":       identitymigrations.Statements,
		"issue reports":  issuereportsmigrations.Statements,
		"notifications":  notificationsmigrations.Statements,
		"oauth2 clients": oauth2clientsmigrations.Statements,
		"passkeys":       passkeysmigrations.Statements,
		"password reset": passwordresetmigrations.Statements,
		"webauthn":       webauthnsessionsmigrations.Statements,
		"settings":       settingsmigrations.Statements,
		"waitlists":      waitlistsmigrations.Statements,
		"webhooks":       webhooksmigrations.Statements,
		"media registry": mediaregistrymigrations.Statements,
		"magic links":    magiclinkmigrations.Statements,
		"refresh tokens": refreshtokenmigrations.Statements,
		"recovery codes": recoverycodemigrations.Statements,
		"data privacy":   dataprivacymigrations.Statements,
		"operations":     operationsmigrations.Statements,
		"work queue":     workqueuemigrations.Statements,
	}

	if waitlists == confirmsWaitlists {
		schemas["action links"] = linksmigrations.Statements
	}

	for name, render := range schemas {
		stmts, err := render(d, prefix)
		must.NoError(t, err, must.Sprintf("rendering %s's migrations", name))

		for _, stmt := range stmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("migrating %s: %q", name, stmt))
		}
	}
}

// authenticationRequirements is the table this harness's authentication
// interceptor enforces, composed the way a consumer composes one: sign-in's
// own declarations, then every other service mounted here.
//
// Those are optional rather than required, deliberately. Whether a request
// with nobody on it is refused is each surface's decision, and the anonymous
// suite asserts the surfaces make it; a table that refused here would make that
// suite assert this table instead. Optional still resolves every caller who
// presents a token, which is all the other suites need.
func authenticationRequirements(t *testing.T) *signingrpc.AuthenticationRequirements {
	t.Helper()

	reqs, err := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).
		DeclareService(signingrpc.AuthenticationOptional,
			auditpb.AuditService_ServiceDesc.ServiceName,
			billingpb.BillingService_ServiceDesc.ServiceName,
			commentspb.CommentsService_ServiceDesc.ServiceName,
			identitypb.IdentityService_ServiceDesc.ServiceName,
			issuereportspb.IssueReportsService_ServiceDesc.ServiceName,
			notificationspb.NotificationsService_ServiceDesc.ServiceName,
			oauth2clientspb.OAuth2ClientsService_ServiceDesc.ServiceName,
			passkeyspb.PasskeysService_ServiceDesc.ServiceName,
			passwordresetpb.PasswordResetService_ServiceDesc.ServiceName,
			settingspb.SettingsService_ServiceDesc.ServiceName,
			waitlistspb.WaitlistsService_ServiceDesc.ServiceName,
			webhookspb.WebhooksService_ServiceDesc.ServiceName,
		).
		DeclareService(signingrpc.AuthenticationAnonymous, grpc_health_v1.Health_ServiceDesc.ServiceName).
		Build()
	must.NoError(t, err)

	return reqs
}

// bearerConn puts one signed-in caller's token on every call, beside which run
// the call is made in, which is what a consumer's authenticated client
// connection does with the first half.
type bearerConn struct {
	grpc.ClientConnInterface

	token     string
	reserving string
}

func (c *bearerConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return c.ClientConnInterface.Invoke(c.carrying(ctx), method, args, reply, opts...)
}

func (c *bearerConn) NewStream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	method string,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	return c.ClientConnInterface.NewStream(c.carrying(ctx), desc, method, opts...)
}

func (c *bearerConn) carrying(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token, mdReserving, c.reserving)
}

// credentialTransport puts one subject's bearer token on every request, which
// is what a consumer's authenticated HTTP client does.
//
// It carries the run the subject was minted in beside the credential, as
// headerReserving, the way Decorate carries mdReserving beside the gRPC one.
type credentialTransport struct {
	token     string
	reserving string
}

func (c *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(headerReserving, c.reserving)

	return http.DefaultTransport.RoundTrip(req)
}
