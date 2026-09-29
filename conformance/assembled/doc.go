/*
Package assembled is the conformance suite run against a service this module's
composition root built, rather than one a harness built by hand.

Every other subject in this tree hand-builds its server: it calls NewServer, hands
it a store and an extractor, and serves it. That proves what a handler decides and
nothing about whether service.Register, service.RegisterTransports and service.New
put the handler there. This package boots a service the way a consumer's main
does — a service.Config, Register, the application's own registrations, New, Run —
dials the port it bound, and runs the identical suites against it.

It holds no exported code. The harness is its test files, because what it proves is
this module's own composition root, and a consumer's equivalent is their own main.

# What the harness supplies, and why each is the consumer's

A composition root does not stand up a service on its own, and the gaps are the
point: each is something a consumer's main owes, so a harness that did not supply
it would be proving a service nobody could run.

  - The interceptors. The gRPC server resolves []grpc.UnaryServerInterceptor from
    the injector, and nothing in this module registers one. The harness registers
    the error encoder and signingrpc's authentication interceptor, in that order —
    and a consumer who forgets the encoder has every mapped sentinel reach their
    clients as Internal, which the anonymous suite reads as a failure. Every
    service but sign-in is declared optional on the interceptor's table, so that
    whether a request with nobody on it is refused stays each surface's decision
    and the anonymous suite keeps asserting the surfaces rather than the table.
    The forced-password-change gate is not registered separately: the
    extractor runs it by default, and the sign-in suite asserts it is there.
  - The services Register does not build. identity/config's RegisterService is a
    call the application makes. oauth2clients and passwordreset have config
    blocks, and the harness leaves both unset and builds the two services by
    hand instead, which is the other way a consumer mounts them. Configuring a
    block as well would register each service twice. signin is mounted through
    its block, so its surface is proven to come from a service.Config. What the
    harness supplies for it is what only an application can: the authenticator,
    which the hand-built reset flow resolves too, and the sign-in link mailer.
    A surface over a service nobody built stays absent. That is the absence
    rule working, not a gap in it.
  - The declarations no environment variable can express: comments.Targets and
    webhooks.Catalog.
  - The extractor, and the authorizers that surfaces refuse to mount
    without. The extractor is signingrpc's, named to service.Transports as
    both the extractor and the grants, and its interceptor and middleware are
    installed by the harness, since service installs neither. A subject's
    credential is therefore a token the sign-in service minted for it — through
    the administrative door for an administrator — and not a stand-in this
    harness reads back. The authorizers encode one rule — a
    caller has standing in their own user and their active account — rather
    than a yes, because a permissive authorizer would let every later
    confinement assertion pass on the strength of the rule being absent.
  - The role policy, on the extractor through signingrpc.WithGrants, which is
    what the surfaces that ask inside a handler read to decide whether
    include_archived is honored and whether a reserved setting may be written.
    An administrator holds a service role the extractor keeps only on an
    administrative token: a member holds every permission those surfaces'
    Permissions maps name except the archive grants and settings' reserved-write
    grant, and an administrator holds those as well. That split is what lets the
    subject mint an administrator for conformance.AsAdmin, so the granted half
    of each rule is asserted beside the refused one. These grants decide only
    those two questions; service mounts no method enforcement, and a
    consumer's main adds its own.
  - One piece of method enforcement of the harness's own, because every suite
    runs twice: once with members making every call, and once reserving a
    back office's worth of calls — the directory's administration, the
    catalog's writes, the scope-wide ledgers, the settings catalog, the client
    registry and the waitlist console, and never a door reached without a
    caller — where reserveStaffCalls refuses each to anybody but an
    administrator. The second run is what keeps the path a consumer's
    reservation takes exercised; that each caller makes only the calls it
    declared is checked by the suites themselves, on its own connection, in
    both.
  - The HTTP half of authorization, which service does not build either: an
    authorization/http Enforcer named to service.Transports as the
    HTTPEnforcer the three HTTP surfaces check their routes with. Its grants
    are the role policy's — a member holds every permission those surfaces'
    Permissions maps name — except in the reserving run, where httpGrants
    withholds from a member the permissions of staffOnlyRoutes, one route on
    each surface. That is how a consumer reserves a route, and the only way
    there is: nothing keyed by route sits in front of the surfaces. The run
    rides beside the credential as a header, the way it rides in metadata on
    gRPC.
  - The schema. Nothing in service runs migrations; a consumer renders each
    package's migrations.Statements with the prefix they configured, and so does
    this.

# Port 0, and the one thing it cannot say through a Config

The gRPC server is asked for an ephemeral port and the harness learns which one
from grpcserver.Server.Addr, so three dialects can boot in parallel without
reserving ports that something else might take between the reservation and the
bind.

Asking for port 0 through service.Config has a wrinkle worth knowing.
Config.ValidateWithContext releases every sub-config that holds nothing beyond what
its type parses to in an empty environment, and a grpcserver.Config whose only
setting is Port: 0 is exactly that — so it is released and no server is built. The
harness spells out MaxReceiveMessageSize at its default, which means the same thing
and survives. A consumer asking for an ephemeral port through their environment
alone meets the same rule.

# Every surface, or a failure

All twelve gRPC surfaces are mounted on every dialect, and the harness hands
every suite a client for each regardless of what mounted. That is deliberate: a surface the
composition root failed to mount answers Unimplemented, and the anonymous suite
reads that as a failure rather than skipping, so a regression in what
RegisterTransports mounts cannot pass as an absence.

The HTTP surfaces are mounted on every dialect too: mediaregistry, operations,
and dataprivacy, which fulfills its requests as operations. Subject.HTTP still
says which are mounted, because the flags are how a hand-built subject that
serves fewer reports it; this harness sets all three.

dataprivacy refuses to start with no collector registered, so the harness
registers identity's privacy adapter through privacyadapters — the call a
consumer makes — over the directory the composition root built. service mounts
the privacy surface with its default, which confines a request to its person
and names no tenant, so the adapter's scope resolver is the harness's record of
which directory it registered each caller into: that is where their data is.
Actions.ArtifactExpired is dataprivacy's own Sweeper at a clock past the
request's expiry, over a store narrowed to that one request, so a sweep never
expires an artifact a parallel assertion is still reading.

# Isolation

Each run renders every package's tables under a prefix of its own, through the same
TablePrefix field a consumer sets. That is what lets three dialects share one server
per test binary, and what lets a run pointed at a CONFORMANCE_*_DSN server share it
with whatever else is there: nothing needs permission to create a database.
*/
package assembled
