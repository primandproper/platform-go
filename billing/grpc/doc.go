/*
Package grpc is billing on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/billing]'s Store, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into their policy.

It is imported as billinggrpc.

It follows the pattern identity/grpc's documentation states for a domain surface
in this module, and this comment records only where billing diverges from it and
why. Most of the divergences are consequences of one fact: this package has money
on it, and a caller who is mostly not the one writing.

	srv, _ := billinggrpc.NewServer(store, client, extractPrincipal, authorizer)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

# Eighteen RPCs over a thirty-method store

Twelve reads and six writes — two that stock and revise the catalog, and the
four-strong Archive set. The subset is the decision worth reading first, because
on this table it is not the usual one.

	the catalog        GetProduct, ListProducts
	its administration CreateProduct, UpdateProduct, ArchiveProduct
	somebody's own     GetSubscription, ListSubscriptionsForAccount,
	                   ListCurrentSubscriptions, GetPurchase,
	                   ListPurchasesForAccount, GetTransaction,
	                   ListTransactionsForAccount
	an operator's      ListSubscriptions, ListPurchases, ListTransactions
	administrative     ArchiveSubscription, ArchivePurchase, ArchiveTransaction

# The seven writes that are not RPCs

CreateSubscription, UpdateSubscription, SetSubscriptionStatus, CreatePurchase,
CompletePurchase, RecordTransaction and SetTransactionStatus are absent, and each
says so on its own billing.Store method.

They are the pattern's first rule — a write whose caller is already inside the
process's own transaction is not an RPC — and billing is where it applies to
seven methods rather than one. Four are a payment processor's callback: an event
arrives at a Stripe or RevenueCat receiver the consumer owns, is mapped through a
capitalism adapter, and is written beside an audit entry naming who was billed
and a data change event somebody fans out. Those three writes are one fact. A
subscription status change nobody can attribute is the expensive kind of missing
record, in the one domain where attribution is the point.

The other three are the checkout flow's, which is the same argument one step
earlier: CreatePurchase records the attempt in the transaction that recorded the
payment intent, so that the intent has something of ours to point at.

Two of the four callback writes are also *guarded* — SetSubscriptionStatus is
`SET status = X WHERE status <> X` and CompletePurchase is guarded on
completed_at being NULL — and the guard is the mechanism that makes a
redelivered event safe. Over an RPC the guard still fires and the answer still
comes back, and the caller who acts on it is a webhook handler that has to decide
whether to acknowledge the delivery. Putting a network hop between the handler
and that decision buys nothing and can lose the transaction.

# The ruling on the *ByExternalID reads

billing.Store has four lookups by a payment provider's identifier —
GetProductByExternalID, GetSubscriptionByExternalID, GetPurchaseByExternalID and
GetTransactionByExternalID. None of them is on this surface, and this is the
ruling the issue that opened this package left open.

The caller who holds a provider's identifier is the callback that was handed one,
and it already holds the store: those reads exist so that a handler can tell a
redelivery from a new charge before it writes, on the executor its transaction is
running on. Nothing left over is a client.

What a client would be doing with one is the other half. The identifiers are
another system's namespace — sub_1234, pi_5678 — and a lookup keyed on them
answers "whose subscription is this" for an id the caller did not mint and cannot
be assumed to have any standing in. Every other keyed read here resolves an
account and asks [AccountAuthorizer] about it; these would too, but the id space
is one an attacker can walk in a way this module's own ids are not, and the read
would be a probe of the processor's account rather than of ours.

The reverse direction stays: a Subscription this caller may already read carries
its external_subscription_id, which is what a console links out to the processor
with. Reading an id off a row you hold is not the same act as finding a row from
an id.

# What is on the wire has no reading of it

There is no GetAccountStanding, no is_active, no entitled and no in_good_standing
— not as an RPC, not as a field on any message, and not as a filter. The
temptation is real and it is the one thing this surface must not do: billing
stores facts a payment provider owns and interprets none of them, so a standing
computed here would be a policy on this module's release cadence, disagreeing
with whichever deployment sells the same thing differently.

What ships is Subscription.status and the read that pages the agreements whose
paid period covers now. The seam the reading belongs in already exists —
billing/plans fills entitlements' PlanSource with exactly that read plus a
function the consumer writes — so a standing RPC would be a second home for a
decision that has one.

No message carries a scope either, and that is spelled as `reserved "scope"` on
every one of them rather than as a comment — which is the mechanism audit.proto
established for this lane. A comment is something a later field can be added
beneath; a reservation is something protoc refuses, in this repository and in a
consumer's fork of the file. The reservation is on the four entities as well as
on the requests, since a response telling a client its tenant would be answering
with something the client supplied.

# Row-level permission, and the two shapes a refusal takes

[Permissions] is a grant on the method, evaluated by authorization/grpc's
interceptor before the request body is looked at. [AccountAuthorizer] is the
second half, and it is here for the reason identity/grpc's TargetAuthorizer is:
an interceptor holding the request and the grants has no handle to read a
membership with, so the question is asked inside the handler.

Two things about it differ from the directory's.

It has no default, where MembershipAuthorizer is identity/grpc's. That package
owns the membership table; this one does not know what makes an account
somebody's, so the seam is a required positional argument rather than a default
that would be wrong in a way nothing reports. A consumer already running
identity/grpc passes its MembershipAuthorizer straight in.

And a refusal is answered two ways rather than one. Where the account is named in
the request, a refusal is codes.PermissionDenied, exactly as the directory
answers. Where the row was read first and the account came off it — the three
keyed reads — a refusal is codes.NotFound, the same status a row that is not
there gets, because answering anything else would tell a caller walking
transaction ids which of them are real. The chain returned is still the refusal
and the log and the span record it; only the status differs, and
ErrTargetNotPermitted is not a client-safe sentinel, so its wording does not
travel.

# Errors

This package registers nothing. billing.HTTPMapper and billing.GRPCMapper live
beside the sentinels they map, and installing them is the composition root's one
call — errormappers.Register, which service.Register makes for a service built
from a service.Config and a service assembled by hand makes itself. Without it
every refusal this service returns arrives as codes.Unknown, a redelivered
webhook included.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel. billing's
client-safe sentinels matter more here than on the other surfaces: seven of its
refusals are InvalidArgument, five are AlreadyExists and two are
FailedPrecondition, and inside each family the remedy differs.
*/
package grpc

//platform:transport resource surface: the catalog, the agreements, the sales and the ledger, read-biased — over `billing.Store`
