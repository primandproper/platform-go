/*
Package grpc serves the in-app inbox and the device registry over gRPC.

It is the transport and not the policy, which is the bargain notifications
states one layer down and identity/grpc states in the same words: who is calling
is an interface a consumer's own authentication interceptor satisfies, and what
each method requires is a default map a consumer composes into their own.

	srv, _ := notificationsgrpc.NewServer(store, store, db, extractPrincipal)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

It is imported as notificationsgrpc.

# Nine RPCs over two seams

Six on the inbox — list, list unread, get, mark one read, mark them all read,
archive — and three on the registry: register, list, revoke. The six are the
bell icon, which is the screen every consumer's application has and the code
every consumer otherwise writes. The three are the surface whose caller is
literally a remote device: a handset re-registers on every app launch and every
token rotation, which is a description of an RPC being called from a phone.

[NewServer] takes the two interfaces separately because notifications declares
them separately, and one value satisfies both — notifications.SQLStore is passed
twice. Both are required. A deployment with no mobile application never calls
the three device RPCs, which costs it nothing; a server that accepted a nil
registry would answer three of its nine methods differently depending on wiring
nobody can see from the client side, and silently is the one way this surface
must not fail.

# Row-level permission, and why there is no TargetAuthorizer

identity/grpc grew [github.com/primandproper/platform-go/v14/identity/grpc.TargetAuthorizer]
because eleven of its RPCs take their target from the request, and whether the
caller has standing in that row cannot be answered by an interceptor holding
only the method name and the caller's grants.

No RPC here takes a target that way. Every row this surface reaches is reached
by a statement binding both the caller's scope and the caller's own principal,
and the schema has no field either could come from — notifications.proto
reserves the names "scope" and "principal" on every request message, so a fork
of the file cannot grow one either. The two ids a request does carry, a
notification id and a device id, name a row inside that filter rather than
around it: a notification belonging to somebody else reads as absent, which is
notifications.ErrNotificationNotFound and is what keeps a read from being an
oracle for what other people have been told.

So the row-level question is answered by construction here rather than by a
seam, and adding a seam would mean adding the request field it would have to
read. [Permissions] remains the first half — whether this deployment offers the
call at all — and is declared for every method the service has.

# A caller with no identity has no inbox

[ErrNoPrincipalIdentifier] is this surface's own refusal, and it has no
counterpart on the OAuth2 client registry next door, where a principal with an
empty user identifier is an ordinary machine caller acting on the registry's
administered rows. Here the caller *is* the row's address. A principal whose
UserID is empty names no inbox and no handset, so the RPC stops with
codes.Unauthenticated rather than reaching a store that would refuse it as
notifications.ErrEmptyPrincipal — which is a true statement about an argument
and a misleading one about a request, since there is no field for the caller to
correct.

# The transaction is this handler's

notifications is the module's sharpest statement of the store convention: every
write takes a database.Tx, and no form of any write opens a transaction of its
own, because a notification is almost always about something else that was just
written. That leaves an RPC handler as exactly the caller that convention
describes as opening one anyway — "a caller with genuinely nothing to join opens
one with Client.WithTransaction and passes the Tx it is handed" — and it is one
line, written once here instead of once per consumer.

RegisterDevice reads its row back inside that transaction, which is what the
store's reads taking a database.SQLQueryExecutor rather than a reader is for. A
re-registration answers with the original id and creation time, and without the
transaction it would answer with whatever was committed before the write.

# Errors

This package registers nothing. notifications.HTTPMapper and
notifications.GRPCMapper live beside the sentinels they map, and installing them
is the composition root's one call — errormappers.Register, which
service.Register makes for a service built from a service.Config and a service
assembled by hand makes itself. Without it notifications.ErrNotificationNotFound
arrives at a client as codes.Unknown.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel.

# The pattern this surface follows

identity/grpc's doc.go states what a domain transport here looks like, and this
package cites it rather than re-deriving it. The three writes that stay off the
wire are three different shapes of the same test — who is the realistic caller —
and notifications is the package where all three appear at once:
Inbox.CreateNotification is the transactional companion,
Registry.ListDevicesByPrincipals is the internal fan-out, and
Registry.InvalidateDeviceToken is the provider callback hook. Each argues its
own absence on its own Store method; roster_test.go holds the service descriptor
against both interfaces so a tenth method arrives classified or not at all.
*/
package grpc

//platform:transport resource surface: the in-app inbox and the device registry — over `notifications.Inbox` and `notifications.Registry`
