/*
Package notifications is the durable half of telling somebody something: the
in-app inbox, and the registry of devices a push can be addressed to.

The subpackages beside it deliver. notifications/mobile sends to handsets
through APNs and FCM; notifications/async pushes to a connected browser through
Ably, Pusher, SSE or a websocket. Neither stores anything, which left every
consumer writing the same two tables: the row that says "this person was told
this, and has or has not read it", and the list of tokens to push to.

# The two halves, and why the registry is the one that goes wrong

The inbox is the generic half. Created, read, archived is the same lifecycle in
every application, and what varies — the topic, the wording, the link — is
opaque to this package.

The registry is the half with a feedback loop, and it is why this lives beside
the senders rather than in each consumer. Providers report invalid and expired
tokens on send: a phone is wiped, an app is uninstalled, a token is reissued,
and the next push comes back Unregistered. A registry that never hears about
that keeps addressing pushes to handsets that no longer exist, indefinitely,
while every send reports a failure nobody connects to a row anybody could
delete. So a rejection the provider calls permanent becomes
mobile.ErrTokenInvalid at the sender, and [Registry.InvalidateDeviceToken] is
the hook that removes the row:

	store, err := notifications.NewSQLStore(client)
	// ...
	sender := mobile.NewMultiPlatformPushSender(apnsSender, fcmSender,
		mobile.WithTokenInvalidator(store))

Wire it and dead tokens leave on their own. Leave it unwired and the
classification still reaches the caller as an error — nothing is hidden — but
nothing prunes.

From configuration the same wiring is two registrations rather than a call:
notifications/config registers the store, notifications/mobile/config registers
the sender and resolves the registry optionally, and a container carrying both
prunes. A container carrying only the sender behaves exactly as it did before.

# The transaction is the caller's

Every write a consumer calls takes a database.Tx and every read takes the wider
database.SQLQueryExecutor, which is the module's store convention. Nothing here
opens a transaction of its own, and that absence is what the package is for: a
notification is almost always *about* something else that was just written, and
one filed in a second transaction survives the rollback of the operation it
describes. A refused order that has already told somebody it shipped is a
failure running in the direction the user can see, which is the one direction
worth spending a signature on.

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if err := orders.Place(ctx, tx, scope, order); err != nil {
			return err
		}

		return store.CreateNotification(ctx, tx, scope, notification)
	})

The reads take the wider type so one method serves both moments: a client
polling its inbox passes Client.Reader(), and a service that has just filed a
notification passes the Tx it filed through and sees it.

[Registry.InvalidateDeviceToken] takes neither, for the reason below.

# Tenancy, and the one method without it

Every read and write here takes a tenancy.Scope, and the inbox takes a principal
beside it, because a notification addressed to somebody is not a row the rest of
their tenant may read. There is no unscoped variant of anything a consumer
calls. The scope is an argument even on the two writes that take a whole entity:
[Notification] and [Device] carry one, but a scope read off a struct the caller
assembled elsewhere is derived rather than bound, and an entity that disagrees
with the argument is [ErrScopeMismatch] rather than either value quietly
winning.

[Registry.InvalidateDeviceToken] is the exception — to the scope and to the
transaction both — and it is the component servicing itself rather than
answering a consumer read. What a provider hands back is a token and nothing
else — not the tenant, not the person — so a scoped variant would require the
caller to already know the answer the hook exists to act on. The token is unique
across the whole registry, so naming it names one device. And the caller is a
send path mid round trip to a provider, with no transaction of anybody's to
join, so it runs on the connection the store was built with.

# Where the SQL comes from

The store executes no SQL this module has not checked against its own schema.
notifications/internal/queries describes the two tables as data; a generator
renders that into one .sql per dialect;
sqlc checks each against the DDL notifications/migrations ships; and
sqlc-gen-unison turns the checked statements into the typed querier the store
calls. A column renamed in a migration is a failed generate rather than a
runtime scan error, on all three dialects at once.

	make generate   # re-renders internal/queries/<dialect>_generated.sql
	make unison     # re-renders the schema and the generated querier

# Getting the tables

notifications/migrations renders the DDL for a dialect and a table prefix. It
ships no numbered migration file, because migration numbers are global per
consumer; hand migrations.SQL to database/migrate's WithGeneratedMigration and
the tables are created by your own migration run.

# Where this package stops

Nine of the twelve methods on the two seams are served over gRPC by
notifications/grpc: the six inbox calls a bell icon makes, and the three a
handset makes about itself. That surface reads both the directory and the
recipient off the caller the consumer's interceptor resolved, so no request on
it names either, and it opens its own transaction for each write — the "caller
with nothing to join" the section above describes, written once there instead of
once per consumer.

The three that stay behind are the three shapes of machinery, and each says so
on its own Store method: [Inbox.CreateNotification] is the transactional
companion, [Registry.ListDevicesByPrincipals] is the internal fan-out, and
[Registry.InvalidateDeviceToken] is the provider callback hook. What is still an
application's own is the policy — who is calling, and what each method requires
— and the module README's "Transports" section is where that line is drawn for
the module as a whole.
*/
package notifications

//go:generate go run ./internal/queriesgen
