/*
Package service is platform-go's composition root: one config struct describing
a whole service, and one walk that registers everything it names with a
samber/do injector.

The composition decision is presence in the config, and nothing else. Every
subsystem is a pointer sub-config, `env:",init"` allocates those pointers so a
deployment can configure a subsystem entirely from the environment, and
normalization releases the ones nothing was put into. What survives is what the
operator configured:

	non-nil sub-config  ->  registered
	nil sub-config      ->  never registered

That is "the provider is the only off switch" scaled up one level. There are no
feature flags and no builder DSL, because there is nothing for them to decide
that the config does not already say. A subsystem nobody configured is simply
absent from the injector, which internal/injection.InvokeOptional already
reports as absent — so dependents that treat absence as a noop get their noop,
and dependents that genuinely need it fail with do's own not-found error rather
than running against something that looks configured.

Two constraints hold this package's shape:

  - It defines no domain types, ever. Registries, catalogs, handlers, and
    policies are the application's, and reach the injector from the application.
  - It does not hide the injector. Register takes the caller's do.Injector and
    everything it registers stays individually invocable. This is convenience
    over do, not a wall in front of it.

Register is a pure function of the config, so what a service is made of can be
read off the config it booted with.

New and Run are the other half. Register says what a service is made of; New
builds it in the order it has to come up, and Run serves until the process is
signaled and then takes it down in the order that makes each drain mean
something — ingress first, background loops in reverse, the observability
pillars last. The convention that makes that orderable is Runner, which every
background loop in this module already satisfied before it had a name.

Health falls out of the same reading. Register wraps the infrastructure it
registered in the healthcheck adapters that have always existed for it, so a
service that configured a database and a queue has a readiness answer for both
without asking for one; the servers mount it, HTTP at /readyz and gRPC as
grpc_health_v1, from the one registry. What the platform cannot see — a domain
dependency, a cache whose type no config can name — joins through
WithHealthChecks.

# Transport surfaces

Register wires the stores, the services and the loops. RegisterTransports wires
what a client talks to:

	service.Register(i, cfg)
	service.RegisterTransports(i, &service.Transports{
		Extractor: principalFromContext,
		Authorizers: service.Authorizers{
			BillingAccounts:  accounts,
			IssueReports:     reports,
			SettingsSubjects: subjects,
			WaitlistSignups:  signups,
		},
	})

	svc, err := service.New(i)

Fifteen surfaces mount. Twelve gRPC — audit, oauth2clients, passwordreset,
signin, billing, comments, identity, issuereports, notifications, settings,
waitlists and webhooks — join the []grpcserver.RegistrationFunc the gRPC server
is built from. Three HTTP — dataprivacy, mediaregistry and operations — put their
routes on the router the HTTP server serves.

That router is checked, which routing.Router leaves to whoever holds it: it
accumulates registration failures rather than returning them, and nothing
between mounting and Serve looks again, so a pattern that collided with one
already there is a route quietly not on the server. Each HTTP surface is asked
after it mounts, and the lane is asked once before any of them do — a router
handed over already broken is ErrRouterAlreadyFailed, with the application's own
failure underneath, because after the first surface mounts there is no telling
the two apart.

A surface mounts when everything it is built from resolves, and absence is
absence: a config naming no billing registers no billing store, so no billing
surface mounts, and that is not a failure. A component that was registered and
cannot be built is, and it is reported naming the surface that wanted it. It is
the same distinction the rest of this package draws, and drawing it here is what
lets identity, oauth2clients and signin behave without a special case — their
servers are built over a service Register does not register, so they mount for an
application that registered one and stay absent for one that did not.
passwordreset is a service Register does register, from Config.PasswordReset, so
its surface mounts from the config plus the mailer and authenticator only the
application can supply.

# The two seams

Everything else about a surface is deterministic from the config. These are not,
because no environment variable can express them:

The extractor is how a surface tells who is calling. One of them, for the
fourteen that read one — passwordreset is the exception and needs none, because
every RPC on it is for somebody who cannot sign in — which is the argument the
callers package already makes: a deployment has one authentication interceptor
and one notion of a caller. Four surfaces
declare something narrower than a principal — audit and operations want a scope,
dataprivacy wants a subject, mediaregistry wants a caller identifier and a scope
— and each of those is derived from the one extractor rather than asked for
again.

The authorizers are the rules about which rows a caller who may make a call may
make it against. Four are required, and a surface configured without one fails
the startup that configured it rather than mounting open — under the surface's
own sentinel, because the surface is what knows what it was missing. Three have
a default their own package chose, and leaving the field nil leaves that choice
alone.

# What RegisterTransports owns, and what it leaves

It owns the []grpcserver.RegistrationFunc key. There is no way to mount a gRPC
surface without it, and samber/do refuses a second provider for one type by
panicking, so an application's own gRPC services arrive through
Transports.Registrations rather than through a second registration that would
race this one.

It registers no error mappers. errormappers.Register is the one door for those
and Register already calls it unconditionally, so there is nothing here for a
mount to switch on. operations/http.New installs its own HTTP mapper as it
always has — that is the module's single standing exception, tripped by mounting
that surface rather than added by this one, and nothing else follows it.

It declares no authorization requirements. Every gRPC surface ships a
Require(*authzgrpc.RequirementsBuilder) naming the permission each of its
methods needs, and installing those beside the interceptor that enforces them is
still the consumer's. A mount is not a policy, which is the same thing
identity/config's RegisterServer says about the one surface it builds.

sessions/http does not mount. Its constructor is NewManager[T] and a type
argument is not something a Config can supply — the same reason sessions/config
registers its store through a generic bridge — and what it produces is
middleware rather than routes. An application that has a session type has a
router to put it on.
*/
package service
