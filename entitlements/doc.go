/*
Package entitlements answers "may this account use this feature, and how much is
left".

Four packages already hold pieces of that answer. capitalism knows what the
account bought, metering knows what it has consumed and can enforce a limit,
featureflags knows about rollouts and overrides, and authorization owns the
yes/no seam every handler already gates on. Every SaaS built on them glues the
four together by hand, in a function called something like canUse, once per
service, differently each time.

The metering documentation frames the first gap — capitalism can charge, it
cannot count. This is the third leg: capitalism charges, metering counts, and
this gates.

# The question

	decision, err := checker.Check(ctx, accountID, "advanced_search")
	if err != nil {
	    return err
	}

	if !decision.Allowed {
	    return decision.Err()   // 402, with a code saying which denial it was
	}

One call, whatever kind of feature it is. A boolean feature — SSO, an export
button, a support tier — is answered from the plan and from feature flags, with
no I/O beyond a cached plan lookup. A quota feature — seats, API calls, tokens —
adds metering's cached read and comes back with Used, Limit, Remaining, and
ResetsAt.

	decision, err := checker.CheckQuantity(ctx, accountID, "llm_tokens", estimated)

is the same question for a caller that knows how much it is about to consume.
Check asks for one unit, because "may I consume nothing" is true at exactly the
moment a quota is spent.

# What is configuration and what is code

Features are declared in Go:

	catalog := entitlements.NewCatalog()

	_ = catalog.RegisterFeature(entitlements.Feature{
	    Key:  "advanced_search",
	    Kind: entitlements.KindBoolean,
	    GrantFlag: "advanced-search-rollout",
	})

	_ = catalog.RegisterFeature(entitlements.Feature{
	    Key:   "llm_tokens",
	    Kind:  entitlements.KindQuota,
	    Meter: "llm_tokens",
	})

Which features exist and what kind each one is are facts about the code that
reads them: a quota feature names a meter the application registered, a boolean
one names a permission its handlers check. Neither is spellable in an
environment variable, and a catalog that discovered its features from
configuration would let a typo silently create a feature nobody gates on.

What each plan includes is the part that varies:

	_ = catalog.RegisterPlan(entitlements.Plan{
	    Name: "pro",
	    Includes: []entitlements.Grant{
	        {Feature: "advanced_search"},
	        {Feature: "llm_tokens", Limit: 5_000_000, Behavior: metering.BehaviorAllowOverage},
	    },
	})

Pricing changes more often than a deploy, and by people who do not ship one, so
plans are readable from the environment — see entitlements/config.

# Which plan an account is on

That is a PlanSource, and it is the one seam this package cannot fill:

	plans := entitlements.PlanSourceFunc(func(ctx context.Context, account string) (string, error) {
	    plan, err := db.PlanForAccount(ctx, account)
	    if errors.Is(err, sql.ErrNoRows) {
	        return "", entitlements.ErrNoPlan
	    }

	    return plan, err
	})

It is deliberately not read from capitalism. capitalism talks to the payment
provider, and the provider's API has no business on a request path: a Stripe
round trip per feature check spends a latency budget on a fact that changes a
few times a year, and an outage there would take the product down rather than
the billing. The provider tells you when a subscription changes — that is what
capitalism's webhook handler is for — and what it changed to belongs in your
database by the time anybody asks.

The webhook that writes it should also call Checker.Invalidate, so the customer
who just upgraded is not told for another cache TTL that they have not.

# One limit, not two

The arrangement worth wiring is this one:

	quotas, err := entitlements.NewQuotaSource(catalog, plans, registry)
	enforcer, err := metering.NewQuotaEnforcer(ctx, meterCfg, store, registry,
	    metering.WithEnforcerQuotaSource(quotas))
	checker, err := entitlements.NewPlanChecker(ctx, cfg, catalog, plans,
	    entitlements.WithEnforcer(enforcer),
	    entitlements.WithFeatureFlags(flags),
	    entitlements.WithCache(assignments))

metering asks a QuotaSource what a subject's limit is and enforces whatever it
is told. This package holds the catalog that knows. Wired as above there is
exactly one number, and the limit an account is shown is by construction the
limit enforced against it. Wired any other way there are two, and a Checker that
notices they disagree reports what metering enforced — the number the customer
actually experiences — and counts it under entitlements_limit_mismatches.

Deriving each quota's period from the meter's registration, rather than taking
it, means a quota this package serves cannot mismatch the meter it is about.
That is metering's ErrPeriodMismatch — a wiring-time mistake it can only raise
on the request path — moved to wiring time.

# Flags grant and flags kill; neither ever decides alone

	GrantFlag  true -> allow a boolean feature the plan excludes
	KillFlag   true -> deny any feature, whatever the plan says

Both are false by default, and false in both cases means "defer to the plan".
That symmetry is the whole design of this part.

Three different things produce that false and only one of them is a decision. A
flag that is off answers (false, nil). A flag that was never created answers
(false, error) — the providers here report an unresolvable flag as a resolution
failure rather than as a default. A provider that cannot be reached answers
(false, error) too, indistinguishably from the previous case. This package
treats all three alike and lets the plan answer, so a flag nobody has created is
inert, a provider nobody can reach is inert, and only a flag somebody
deliberately turned on changes anything.

The alternative — letting a false answer decide — would have an unconfigured
flag, or a five-minute provider outage, silently revoke every entitlement in the
catalog. A kill flag beats a grant flag, because a kill switch a grant can beat
is not a kill switch.

Grant flags are refused on quota features (ErrGrantFlagNotAllowed). Opening a
quota feature the plan excludes leaves nowhere for the limit to come from — the
plan is the only thing that knows it — and defaulting to unlimited is how a
support override becomes an unbounded bill. Raising one account's limit is a
plan change, and PlanSource is the seam: it may return a plan name nobody else
is on.

# Speaking authorization's vocabulary

A boolean entitlement is a permission, and this package says so:

	entitlementPerms, err := checker.Permissions(ctx, accountID)
	...
	grants := authorization.NewGrants(rolePerms, entitlementPerms)

which is the same shape authorization already uses to merge service-wide and
per-tenant authority. A handler then gates on entitlement and on role with one
grants.Has, and neither knows which of the two put the permission there.

Quota features are absent from that set, and not by oversight. A permission set
is checked without I/O and without an error — that is the property authorization
is built to defend — and a quota answer that was true when the session was built
can be false by the time it is read. A permission meaning "was entitled a moment
ago" would be read by every caller as "may proceed". Quota features are asked
about with Check, which is the only way to get an answer that is about now.

Denials carry the vocabulary too. Decision.Err returns errors.ErrNotEntitled or
errors.ErrQuotaExhausted, which errors/http maps to 402 with codes E118 and E119
and errors/grpc maps to PermissionDenied and ResourceExhausted. The two are
separate because the remedies are: "upgrade your plan" and "wait until the first
of the month" are not the same instruction, and a client told the wrong one
either retries forever or gives up on a request that would have succeeded.
Neither message names the feature — which features exist is not something to
disclose to a caller who just failed to reach one.

# What is cached, and what is not

Only the account-to-plan assignment, with a thirty-second default TTL. It is the
one lookup on the path that touches a database, and the row it reads changes a
few times a year.

Feature flags are not cached. Every provider here evaluates locally against
rules it already holds, so a cache would add staleness to something already
fast, and would freeze a percentage rollout's answer for an account the provider
means to re-evaluate.

Usage totals are not cached here either. metering caches its own, with a
staleness budget set per meter by whoever knows what that meter is worth — see
metering's documentation on Check and Consume. Caching them a second time would
compound two staleness budgets into one nobody has reasoned about.

The TTL is short because the trade is not symmetric. Long, and a customer who
has just paid keeps being told they have not, which is the worst half-minute in
a signup funnel. Short, and one indexed read of a hot row happens more often.

# Fail closed, with a named degraded state

An account whose plan cannot be resolved is denied. That is the right answer
whenever the features being gated are the ones being sold: an outage that hands
every account the top tier costs the operator money for as long as nobody
notices.

CheckerConfig.FallbackPlan is the other answer, and it is deliberately a plan
name rather than the boolean "fail open" it replaces. The degraded state is then
a tier somebody chose and can read in the catalog, rather than "everything".
Naming the free plan means a plan-store outage degrades paying customers to free
instead of locking them out, which for a product whose core is free is the
difference between a slow morning and an incident. It is validated against the
catalog at construction, because a fallback naming a plan nobody registered
would deny everything during exactly the outage it was configured to survive.

It applies only to a failed resolution. An account the PlanSource
authoritatively says has no plan is denied whatever the fallback says: that is
not an outage, that is a customer who has not paid, and the two are told apart
by Decision.Reason (ReasonPlanUnavailable against ReasonNoPlan) so the right
person is woken up.

It also applies only to boolean features. A quota feature's limit reaches
metering through QuotaSource, which has no fallback, so a quota Check during a
plan-store outage errors rather than degrading — because the same source answers
Consume, and an exact path that enforced a guessed limit would record
consumption against a plan the customer is not on. See
CheckerConfig.FallbackPlan.

# What is not modeled here

Prices. This package knows that the pro plan includes five million tokens; it
does not know what the pro plan costs, what the overage rate is, or what
currency either is in. All three live in the billing provider's product catalog,
which is the system of record for them, and mirroring them here would be
duplicating that catalog in a second place that can disagree with it — the same
argument metering makes for not modeling plans, applied one level up.

Trials, proration, and plan-change timing. When a plan starts and stops is a
question the provider answers, with anniversary dates and mid-period upgrades
this package would have to reimplement badly. It asks a PlanSource what the plan
is right now, and whoever answers that question already has to have solved this
one.

Per-resource entitlements — "may this account use feature X on project Y". That
is authorization's out-of-scope note in a different hat: it needs either a
mirrored ownership graph or SQL fragments, and today the answer is a predicate
in a query that already runs.
*/
package entitlements
