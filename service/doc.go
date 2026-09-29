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

# A worker process

A worker is a service built from a Config like any other, with no servers in
it. It configures what it drains — JOBS_SCHEDULER_*, the message queue, the
database — and provides the three things a worker is made of that no
environment variable can name, before New:

	service.Register(i, cfg)

	registry := searchsync.NewRegistry(searchsync.WithRegistryPillars(pillars))
	// searchsync.RegisterIndex(registry, ...) for each index.
	do.ProvideValue(i, registry)

	do.Provide(i, func(i do.Injector) (*jobs.PoolGroup, error) {
		consumers, err := do.Invoke[messagequeue.ConsumerProvider](i)
		if err != nil {
			return nil, err
		}

		return jobs.NewPoolGroup(ctx, append(registry.PoolSpecs(), handlerSpecs...), consumers)
	})

	do.ProvideValue(i, []jobs.Job{reapJob, rebuildJob})

	svc, err := service.New(i)

Each joins the lifecycle where its obligations put it:

  - The *jobs.PoolGroup is a background loop, beside the single *jobs.Pool a
    JOBS_POOL_* config builds. It is started by Run once every other loop is
    running, and its Start is the one start that can fail — a subscription the
    broker refuses — so Run returns that failure and takes the rest down
    rather than running a worker that drains nothing. On the way out it closes
    after the scheduler that enqueues into it and before the outbox relay.
  - The *searchsync.Registry is a final flush: its stamp buffers are written
    out after every loop, the pool group among them, has stopped, and before
    the database client they write through is released.
  - The []jobs.Job is handed to the *jobs.Scheduler in New, since
    Scheduler.Register has to precede the Run that Service.Run calls. There is
    one provider of it and it is the application's; samber/do refuses a second
    one for a type, so a job the platform schedules for itself joins the same
    Register call rather than a provider of its own. A list with no scheduler
    to run it is ErrScheduledJobsWithoutScheduler, and a list the scheduler
    refuses — a duplicate name, an invalid job — fails New whole.

All three are optional. A process that provides none of them is the service it
was before; one that provides the jobs without configuring a scheduler is the
one combination refused, because those jobs would never run.

# Transport surfaces

Register wires the stores, the services and the loops. RegisterTransports wires
what a client talks to:

	service.Register(i, cfg)
	service.RegisterTransports(i, &service.Transports{
		Extractor: principalFromContext,
		TenantOf:  service.DirectoryTenant,
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
lets identity behave without a special case — its server is built over a
service Register does not register, so it mounts for an application that
registered one and stays absent for one that did not. oauth2clients,
passwordreset and signin are services Register does register: oauth2clients
from Config.OAuth2Clients, so its surface mounts from the config alone,
passwordreset from Config.PasswordReset, so its surface mounts from the config
plus the mailer and authenticator only the application can supply, and signin
from Config.SignIn, so its surface mounts from the config plus that same
authenticator.

# The seams

Everything else about a surface is deterministic from the config. These are not,
because no environment variable can express them:

The extractor is how a surface tells who is calling. One of them, for every
surface that reads one — passwordreset is the exception and needs none, because
every RPC on it is for somebody who cannot sign in — which is the argument the
callers package already makes: a deployment has one authentication interceptor
and one notion of a caller. Four surfaces
declare something narrower than a principal — audit and operations want a scope,
dataprivacy wants a subject, mediaregistry wants a caller identifier and a scope
— and each of those is derived from the one extractor rather than asked for
again.

The tenant scope is the one derivation an extractor cannot always make. A
principal carries the directory it is in, and for a deployment with one
directory that is the global scope; a deployment whose tenant is the account
files its audit entries, its operations and its media under the account instead.
Those two readings cannot both be Principal.Scope() — identity reads it as the
directory — so a deployment where they differ supplies Transports.TenantOf,
which reads the tenant off a principal this package has already found, and the
three surfaces that mean the tenant are mounted with it. It has no default: a
deployment mounting any of the three without one fails at startup with
ErrNilTenantOf, and a deployment whose directory is its tenant names
DirectoryTenant.

The authorizers are the rules about which rows a caller who may make a call may
make it against. Some are required, and a surface configured without one fails
the startup that configured it rather than mounting open — under the surface's
own sentinel, because the surface is what knows what it was missing. The rest
have a default their own package chose, and leaving the field nil leaves that choice
alone.

The grants extractor is the optional fourth, and it answers what the caller may
do for the seven surfaces that ask inside a handler — billing, comments,
issuereports, notifications, settings, waitlists and webhooks — whether a read
that sent include_archived receives the archived rows, and whether a settings
write may name a setting the catalog reserved to administrators. Neither is a
question a method grant can answer, because both turn on the request. Left nil,
each surface keeps its own fail-closed answer: include_archived is cleared and
every reserved write is refused, for administrators too. That is a server that
withholds rather than one that mounts open, so nil stays legal, and a service
that means to serve either feature supplies the same
authorization.GrantsExtractor its authorization interceptor reads.

# Configuring or replacing a mounted surface

The seams above are the whole list, and it is closed. Anything else a surface
takes — one of its own options, or a required seam of a surface added since —
arrives through Transports.Options, one slice per surface in that surface's own
Option type, passed to its constructor after everything this package supplies:

	service.RegisterTransports(i, &service.Transports{
		Extractor: principalFromContext,
		TenantOf:  service.DirectoryTenant,
		Options: service.SurfaceOptions{
			Audit: []auditgrpc.Option{auditgrpc.WithChainsResolver(chains)},
		},
		Skip: []service.Surface{service.SurfaceOperations},
	})

So a surface's configuration is learned once, on the surface, rather than once
there and again as a field here, and a surface joins the automatic mount only if
every seam it has carries a default — the ones that do not are configured
through Options and refuse the startup in their own words without it. A surface
given options is not refused for a missing Extractor or TenantOf: the resolver
those would have derived is skipped, and the surface is built from the
application's options, refusing if they name no resolver either.

Skip leaves a surface unmounted even when everything it is built from resolves,
so an application can build its own over the same store and mount it through
Transports.Registrations or on the router. A name this package does not mount is
ErrUnknownSurface, because a misspelled skip is the platform's surface mounted
beside its replacement.

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
