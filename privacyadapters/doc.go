/*
Package privacyadapters registers the privacy adapters this module ships into a
dataprivacy.Registry, in one call.

Eleven adapters ship here — ten <pkg>/privacy packages and
dataprivacy/auditerasure — and before this package nothing put them together. A
consumer wired the stores it needed, registered the two or three adapters it had
read about, and its subject access requests were thereafter well-formed,
present, and missing the rest.

That is the failure dataprivacy's design is most anxious about, and the reason
this is a package rather than a paragraph. Skipping an adapter raises nothing.
There is no error, no log and no panic: the fulfiller collects what is
registered, writes an artifact whose manifest lists exactly the sections it
produced, and reports success. An export containing none of the subject's
comments looks precisely like an export of a subject who wrote none, and an
erasure that left them reports Deleted and moves on. Documentation was the
entire mechanism, and the way a consumer found out was a regulator asking about
the rest.

The roster is not written out in this package's prose. A list in prose is
checked against nothing; this one is checked against the module's own source, in
both directions, by walking the tree for packages that declare a
dataprivacy.Collector or dataprivacy.Eraser and requiring every key they ship to
come back from [Register]. A twelfth adapter landing unlisted fails there rather
than in somebody's export.

# What a deployment supplies

[Adapters] is that list as a struct, and a nil field is a domain this deployment
does not run. This is the one way this package differs from errormappers, which
takes no argument at all: a mapper is a package-level value and an adapter is
not. Every one of these needs a store, and every one needs a resolver — the
mapping from a person to the tenants they belong to — which is a judgement about
the consumer's own tenancy model that no environment variable can express. That
is why service.Register wires the ten stores and registers none of these, and
why it still does.

The resolver fields are dataprivacy.ScopeResolver, which every adapter's own
ScopeResolver is an alias of, so one function serves all of them:

	func tenantsOf(ctx context.Context, requestScope tenancy.Scope, subject dataprivacy.Subject) ([]tenancy.Scope, error)

	registered, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
	    Reader:        client.Reader(),
	    Comments:      &privacyadapters.CommentsAdapter{Store: commentStore, Resolve: tenantsOf},
	    Identity:      &privacyadapters.IdentityAdapter{Store: userStore, Resolve: tenantsOf},
	    Notifications: &privacyadapters.NotificationsAdapter{Inbox: inbox, Resolve: tenantsOf},
	    Billing: &privacyadapters.BillingAdapter{
	        Store:   billingStore,
	        Resolve: billingprivacy.FixedAccounts(tenancy.Global()),
	    },
	})

billing/privacy is the one that takes something else, and it is not an
inconsistency to be tidied away: its axis is the account a subject is billed
under, which carries a scope and an id, and neither half is inferable from the
other.

# Why it is not in service

Importing service to register these means paying for the whole config tree —
every sub-config, and every package each one wires, in both modules. A consumer
assembling three packages by hand should not import all of that to put its
comments in a subject access request. This package imports those adapters and
dataprivacy and nothing else, which is the same trade errormappers makes and for
the same reason.

# Why it is not in dataprivacy/config

That package builds a store and a service out of environment configuration, and
it is a field on service.Config. An enumeration of ten domains there would make
every consumer of service.Config, and every consumer of dataprivacycfg alone,
link comments, settings, identity, billing, mediaregistry, notifications,
waitlists, issue reports, oauth2 clients and password resets — which is the
argument above, inverted, inside the config tree the argument is about.

dataprivacycfg.RegisterAuditEraser stays where it is and is the deliberate
exception. "Does this deployment erase its own audit records" is a policy
question with a different answer per jurisdiction and no store behind it, which
makes it the one privacy registration an environment variable genuinely can
express. [AuditErasureAdapter] is the same eraser for a deployment that decides
in Go instead; a deployment uses one or the other, not both, because the second
registration of a key is an error.

# What it does not do

It takes no injector, no context and no config, and is not a do registration.
Nothing here reads a context — every constructor it calls takes a store, a
resolver and at most a dialect — and a ctx parameter nobody could use would be a
signature borrowed from the config subpackages rather than one this call needs.

It registers nothing under a key of the caller's choosing. Each adapter goes in
under its package's DefaultKey, which is the name its section carries in every
artifact; a deployment that wants another one registers that adapter by hand,
and has then said so out loud.

It registers all eleven or none. It builds every adapter before it registers
any, so a nil store in the last field does not leave a registry holding ten of
eleven domains; and it checks the keys against what the registry already holds
before the first one goes in, so a key the caller registered already — which
[Register] reports by name — does not either. Both are the same requirement,
which is that dataprivacy.Registry has no unregister: a registry this call
failed partway through would be one nothing can repair, holding whichever
adapters happened to sort ahead of the collision, and a subject access request
served from it would be well-formed, successful, and missing a domain.

There is no init(). An adapter that installed itself into a registry by being
linked in is a side effect a consumer cannot opt out of, and the choice of which
domains appear in a subject access request is not one to make by import.
*/
package privacyadapters
