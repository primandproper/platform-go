package assembled_test

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/operations"
	operationscfg "github.com/primandproper/platform-go/v14/operations/config"
	"github.com/primandproper/platform-go/v14/privacyadapters"
	"github.com/primandproper/platform-go/v14/service"
	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/tokens"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
)

// registerApplication is what a consumer's main registers beyond its config:
// the declarations no environment variable can express, and the services this
// module's composition root deliberately does not build.
//
// Each surface below mounts only because something here made its dependency
// resolvable, which is service.RegisterTransports' absence rule doing its job —
// a surface over half a service is not a surface.
func registerApplication(i do.Injector, prefix string) {
	// The declarations. Which kinds of thing accept comments, and which events
	// an application publishes, are the application's to say.
	do.ProvideValue(i, comments.Targets{
		"conformance_thing": {Description: "a thing the conformance suite comments on"},
	})
	// Two event types rather than one, because the webhooks suite's assertions
	// about a subscription set — reconciling it, retiring one of it — need a
	// set with more than one member to be observable.
	do.ProvideValue(i, webhooks.Catalog{
		"conformance.happened": {Description: "something the conformance suite made happen"},
		"conformance.followed": {Description: "something that followed from what the suite made happen"},
	})

	// What kinds of long-running work the application runs. Empty is a real
	// answer: dataprivacy registers its own operation kinds into it as it is
	// built, and the application here runs none of its own.
	do.ProvideValue(i, operations.NewRegistry())

	// Which collectors and erasers answer a privacy request. dataprivacy refuses
	// an empty registry — a subject access request nothing can answer is not a
	// request — so this registers identity's adapter through privacyadapters,
	// the call a consumer makes, over the directory the composition root built.
	do.Provide(i, func(i do.Injector) (*dataprivacy.Registry, error) {
		registry := dataprivacy.NewRegistry()

		if _, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
			Reader: do.MustInvoke[database.Client](i).Reader(),
			Identity: &privacyadapters.IdentityAdapter{
				Store:   do.MustInvoke[identity.Store](i),
				Resolve: ownDirectory,
			},
		}); err != nil {
			return nil, err
		}

		return registry, nil
	})

	// identity's service, which identity/config ships a registration for and
	// service.Register does not call.
	identitycfg.RegisterService(i)

	// The three services with no config block at all. Each is built the way its
	// package's documentation says a consumer builds it, from what the
	// composition root already registered.
	do.Provide(i, func(i do.Injector) (oauth2clients.Store, error) {
		store, err := oauth2clients.NewSQLStore(do.MustInvoke[database.Client](i), oauth2clients.WithTablePrefix(prefix))
		if err != nil {
			return nil, err
		}

		return store, nil
	})

	do.Provide(i, func(i do.Injector) (*oauth2clients.Service, error) {
		return oauth2clients.NewService(do.MustInvoke[database.Client](i), do.MustInvoke[oauth2clients.Store](i))
	})

	do.Provide(i, func(i do.Injector) (*passwordreset.Service, error) {
		db := do.MustInvoke[database.Client](i)

		resetTokens, err := passwordreset.NewSQLStore(&passwordreset.Config{TablePrefix: prefix}, db)
		if err != nil {
			return nil, err
		}

		// The mailbox assemble registered, which is where the reset suite reads
		// the link a person would have been sent.
		return passwordreset.NewService(db, resetTokens, do.MustInvoke[identity.Store](i),
			argon2.NewArgon2Authenticator(), do.MustInvoke[*resetMailbox](i))
	})

	do.Provide(i, func(i do.Injector) (*signin.Service, error) {
		db := do.MustInvoke[database.Client](i)
		directory := do.MustInvoke[identity.Store](i)

		// Rotation and the passwordless door, each the store signin ships for
		// it under the run's prefix. Both are optional and a consumer adopting
		// them does exactly this, which is what puts refresh, sign-out and the
		// mailed sign-in link under assertion rather than under "not
		// configured".
		refreshTokens, err := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: prefix}, db)
		if err != nil {
			return nil, err
		}

		magicLinks, err := magiclinks.NewSQLStore(&magiclinks.Config{TablePrefix: prefix}, db)
		if err != nil {
			return nil, err
		}

		return signin.NewService(db, directory,
			argon2.NewArgon2Authenticator(), do.MustInvoke[tokens.Issuer](i),
			signin.WithTOTPIssuer("conformance"),
			signin.WithRegistrar(do.MustInvoke[*identity.Service](i)),
			signin.WithVerifications(directory),
			signin.WithRefreshTokenStore(refreshTokens),
			signin.WithMagicLinkStore(magicLinks),
			// The mailbox assemble registered, which is where the sign-in
			// suite reads the link a person would have been sent.
			signin.WithMagicLinkMailer(do.MustInvoke[*magicLinkMailbox](i)),
		)
	})
}

// ownDirectory is the harness's answer to which tenants a person's data lives
// in: the one the request was made in. Every caller here is minted into a
// directory of its own, so that is the whole of the truth rather than a
// narrowing of it.
func ownDirectory(_ context.Context, requestScope tenancy.Scope, _ dataprivacy.Subject) ([]tenancy.Scope, error) {
	return []tenancy.Scope{requestScope}, nil
}

// operationsConfig puts both of the operations block's tables under the run's
// prefix: the operations themselves and the work queue that runs them.
func operationsConfig(prefix string) *operationscfg.Config {
	cfg := &operationscfg.Config{}
	cfg.Operations.TablePrefix = prefix
	cfg.Queue.TablePrefix = prefix

	return cfg
}

// authorizers are this harness's rules about which rows a caller has standing
// in, and there is one rule: a caller's own user and their active account, and
// nothing else.
//
// A rule rather than a yes. A permissive authorizer would make every confinement
// assertion a later suite writes pass on the strength of the rule being absent,
// which is the failure the positive-control ruling exists to catch — so each of
// these refuses with callers.ErrTargetNotPermitted, the refusal every surface
// already answers.
func authorizers() service.Authorizers {
	return service.Authorizers{
		BillingAccounts:  standing{},
		IssueReports:     standing{},
		SettingsSubjects: standing{},
		WaitlistSignups:  standing{},
	}
}

type standing struct{}

func (standing) own(caller callers.Principal, subjectType, id string) error {
	switch {
	case subjectType == "user" && id != "" && id == caller.UserID():
		return nil
	case subjectType == "account" && id != "" && id == caller.ActiveAccountID():
		return nil
	default:
		return callers.ErrTargetNotPermitted
	}
}

func (s standing) AuthorizeAccount(_ context.Context, caller callers.Principal, accountID string) error {
	return s.own(caller, "account", accountID)
}

func (s standing) AuthorizeReport(_ context.Context, caller callers.Principal, report *issuereports.Report) error {
	if report == nil {
		return callers.ErrTargetNotPermitted
	}

	return s.own(caller, "user", report.Reporter)
}

func (s standing) AuthorizeReporter(_ context.Context, caller callers.Principal, reporter string) error {
	return s.own(caller, "user", reporter)
}

func (s standing) AuthorizeSubject(_ context.Context, caller callers.Principal, subject settings.Subject) error {
	return s.own(caller, string(subject.Type), subject.ID)
}

func (s standing) AuthorizeSubjectRead(
	_ context.Context,
	caller callers.Principal,
	_ tenancy.Scope,
	subject waitlists.Subject,
) error {
	return s.own(caller, string(subject.Type), subject.ID)
}

// AuthorizeWithdrawal refuses. A withdrawal names a signup rather than a person,
// and whose signup it is lives in the store — so this harness cannot answer the
// question without reading a table, and a rule that cannot answer refuses
// rather than permits.
func (standing) AuthorizeWithdrawal(context.Context, callers.Principal, tenancy.Scope, string, string) error {
	return callers.ErrTargetNotPermitted
}
