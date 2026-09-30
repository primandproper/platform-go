/*
Package http is a payment provider's webhook endpoint: verify the delivery,
reconcile it into billing, and answer with the status code the provider acts on.

[github.com/primandproper/primitives-go/v2/capitalism] verifies and parses a
delivery, and [github.com/primandproper/platform-go/v14/billing/sync] reconciles
one. The endpoint between them was the consumer's to write, and the copy this
package was written against shows what that costs. It answered 400 to every
failure, so a database that blinked told the provider the delivery was bad and
not to send it again. And it read the account from an unsigned query parameter
that overrode the one in the signed payload, so anybody who could reach the URL
could file a provider's report against an account of their choosing.

	handler, err := billinghttp.NewWebhookHandler(manager, syncer, client,
		billinghttp.WithScopeResolver(billinghttp.GlobalScope),
	)

	router.Handle(http.MethodPost, "/billing/webhook", handler)

# What the status code says

It is the only thing a provider reads, so it is the whole contract. 400 is given
only for a delivery that failed verification or could not be parsed — the two
failures sending the same bytes again cannot fix. 200 is a delivery reconciled,
a redelivery acknowledged as unchanged, or a verified delivery with nothing to
reconcile. Everything else is 500, and the provider redelivers. See
[WebhookHandler.ServeHTTP].

# What is still the deployment's

Which tenant a delivery is for, and nothing else. [ScopeResolver] is handed the
verified event and not the request, so there is nowhere for it to read a query
parameter from. The account an agreement belongs to is not asked here at all:
billing/sync's Place answers it from the provider's customer on the signed state,
once, when the agreement is opened.

What belongs beside the row — an audit entry, an outbox event — is written by
[AfterApply], on the same transaction the delivery is reconciled on.

# Why the service does not mount it

A surface is mounted by service automatically only if every seam it has carries
a default, and [ScopeResolver] has none. A deployment built from a service.Config
builds this handler and mounts it itself.
*/
package http

//platform:transport binding: a payment provider's callback, whose status code the provider acts on
