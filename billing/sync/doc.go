/*
Package sync reconciles a payment processor's deliveries into the billing store:
the webhook handler every consumer writes, with the two things they get wrong.

[github.com/primandproper/primitives-go/v2/capitalism] hands a verified webhook
endpoint a [capitalism.Event], and
[github.com/primandproper/platform-go/v14/billing] stores a
[billing.Subscription]. Joining the two is a couple of hundred lines — look the
agreement up by the provider's identifier, open one if this is the first time
anybody has heard of it, move its status if it changed, leave it alone if the
delivery is a redelivery of one already applied, and record what the account's
standing now is. billing's own documentation named that join the consumer's, and
every consumer wrote it.

The copy this package was written against is what the join costs when it is
written once per deployment. Two of its lines are worth naming, because they are
what this package exists to make unwritable:

	CurrentPeriodEnd: now.AddDate(0, 1, 0), // approximate

is a period invented because capitalism's subscription state did not carry one.
It is wrong for every plan that is not monthly and for every renewal that did not
land today, and what it is wrong about is when entitlement runs out. capitalism
carries the paid period now, so this package copies it and there is nothing left
to approximate — see [Syncer.Apply] for what happens when a delivery genuinely
reports no period, which is a refusal rather than a smaller guess.

	if state.Status == "" { status = Active }

is a delivery whose status no adapter could place, read as the most permissive
standing there is. A sync that cannot determine a status must not infer one, and
least of all that one: it is the reading that keeps a lapsed account paid, and it
is applied exactly when the deployment has least idea what happened. This package
acknowledges such a delivery and changes nothing.

# The shape

One call, on the caller's transaction:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		result, syncErr := syncer.Apply(ctx, tx, scope, event)
		if syncErr != nil {
			return syncErr
		}

		return recordAudit(ctx, tx, scope, result)
	})

The transaction is the caller's for the reason every write in billing takes one:
a processor callback almost never writes alone. The subscription row, the audit
entry that says a delivery moved it, the outbox event an application publishes
off the back of it and the account's standing are one fact, and a sync that
opened a transaction of its own would put the row in one and its companions in
another. It is the shape webhooks.Emitter takes, for the same reason.

# What a delivery cannot say, and who says it

[Place] is the one seam here, and it is required.

capitalism's subscription state carries the provider's subscription identifier
and the provider's customer identifier. It does not carry an account id or a
product id, because it cannot: those are this deployment's own words for who is
paying and what they bought, and the join from a processor's customer to an
account is a column in a schema capitalism has never seen. So a create — and only
a create — asks for them.

It is handed the executor the sync is running on, which is the caller's
transaction, so the account lookup it makes sees everything that transaction has
written. A checkout flow that wrote the account's customer id moments earlier, in
the same transaction, is one this resolves against rather than one it cannot
find.

# The standing write, which is optional and belongs in the same transaction

[WithStanding] adds the second half of the composition: the account's coarse
[identity.BillingStatus], written through [identity.BillingWriter] on the same
transaction as the subscription row, with the deployment's own reading of what a
processor status means supplied as a
[github.com/primandproper/platform-go/v14/billing/standing.Classify].

It is optional because a deployment that gates on billing/plans reads the
subscription table directly and stores no coarse standing at all. It is not
defaulted, for the reason standing.Strict and plans.Entitled are not: which
statuses leave an account paid is policy, and a sync that picked one would be
picking it for a deployment that had not been asked.

A Classify reporting false leaves the account's standing exactly where it was,
which is what standing's documentation asks a handler to do with a status nobody
has ruled on. The subscription row still moves — the fact is stored either way;
it is the reading that is missing.

# What is acknowledged and what is refused

Acknowledged, because the delivery is not a failure and a webhook endpoint that
answered 500 to it would be asking the provider to send it again forever:

  - An event carrying no subscription at all — a succeeded payment intent, an
    updated customer. [OutcomeIgnored].
  - A status no adapter could place. [OutcomeUnplaced], nothing written, and
    capitalism's ProviderStatus recorded on the span so the fix is one entry in
    an adapter's table rather than a bisect through provider JSON.
  - A redelivery of an event already applied. [OutcomeUnchanged]. The store
    answers billing.ErrStatusUnchanged, which this treats as the acknowledgement
    it is rather than as the error it is spelled as.

Refused, because a delivery this cannot store is a fact the deployment has to
see:

  - A subscription state naming no provider-side identifier. There is nothing to
    look the agreement up by, and inventing a row keyed on nothing is how a
    table ends up with an agreement nothing can ever reconcile.
  - A create whose delivery reports no bounded paid period. The row's period is
    what ListCurrentSubscriptions pages by and what billing/plans draws an
    invoice window from, and there is no honest value to put there. A perpetual
    entitlement — RevenueCat's lifetime purchase, which reports a start and no
    end — is refused here deliberately and written through the store by whoever
    decided to grant it.
  - A delivery for an agreement somebody archived administratively. The archived
    row still holds the provider's identifier, so the create loses to it and
    answers billing.ErrSubscriptionExists. That is a deployment and a processor
    disagreeing about whether a subscription exists, which is worth an alert
    rather than a silent second row.

# The name

The directory is billing/sync and the package is sync, which shadows the standard
library's. A consumer importing both aliases this one — billingsync is the
conventional spelling, and it is what the examples here use.
*/
package sync
