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
    the error encoder and a stand-in authentication interceptor, in that order —
    and a consumer who forgets the encoder has every mapped sentinel reach their
    clients as Internal, which the anonymous suite reads as a failure.
  - The services Register does not build. identity/config's RegisterService is a
    call the application makes, so the identity surface mounts for an application
    that made it and is absent for one that did not.
  - The extractor, through service.Transports.
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

# Isolation

Each run renders every package's tables under a prefix of its own, through the same
TablePrefix field a consumer sets. That is what lets three dialects share one server
per test binary, and what lets a run pointed at a CONFORMANCE_*_DSN server share it
with whatever else is there: nothing needs permission to create a database.
*/
package assembled
