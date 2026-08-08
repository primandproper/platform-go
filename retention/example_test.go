package retention_test

import (
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v10/audit"
	"github.com/primandproper/platform-go/v10/retention"
)

// A policy set is data: what to delete from, how old it has to be, and why.
//
// The first entry is the case this package was written for — the one-off
// expired-OAuth2-token cleanup script every application eventually grows. As a
// policy it batches, it is accounted for in the audit log, and it stops being a
// script that only its author knows exists.
func ExamplePolicy() {
	policies := []retention.Policy{
		{
			Name:   "expired-oauth2-tokens",
			Target: retention.Table{Name: "oauth2_client_tokens", Column: "expires_at"},
			Age:    24 * time.Hour,
			Basis:  "an expired access token cannot authorize anything; kept a day for support",
		},
		{
			Name:      "delivered-webhook-attempts",
			Target:    retention.Table{Name: "webhook_delivery_attempts", Column: "created_at"},
			Age:       30 * 24 * time.Hour,
			Basis:     "delivery history is operational data, useful for a month",
			BatchSize: 5000,
		},
		{
			Name:   "request-captures",
			Target: retention.Table{Name: "request_captures", Column: "created_at"},
			Age:    7 * 24 * time.Hour,
			Basis:  "captures hold request bodies and may contain personal data",
		},
	}

	for i := range policies {
		policy := &policies[i]

		fmt.Printf("%s: %s after %s\n", policy.Name, policy.Target.Describe(), policy.Age)
	}

	// Output:
	// expired-oauth2-tokens: oauth2_client_tokens after 24h0m0s
	// delivered-webhook-attempts: webhook_delivery_attempts after 720h0m0s
	// request-captures: request_captures after 168h0m0s
}

// Target is an interface so that a policy whose notion of expiry cannot be
// written as a predicate is still a policy rather than a script.
//
// The audit log is that case. Its entries chain per scope, so it prunes each
// scope as a prefix and records a watermark the survivors anchor against —
// behavior no declarative target could express, in a policy that otherwise sits
// in the same slice as every other one, and is scheduled, bounded, and
// accounted for the same way.
func ExampleTarget() {
	policies := []retention.Policy{
		{
			Name:   "expired-oauth2-tokens",
			Target: retention.Table{Name: "oauth2_client_tokens", Column: "expires_at"},
			Age:    24 * time.Hour,
			Basis:  "an expired access token cannot authorize anything; kept a day for support",
		},
		{
			Name:   audit.DefaultRetentionPolicyName,
			Target: audit.PruneTarget{},
			Age:    audit.DefaultRetention,
			Basis:  audit.DefaultRetentionBasis,
		},
	}

	for i := range policies {
		policy := &policies[i]

		fmt.Printf("%s: %s after %s\n", policy.Name, policy.Target.Describe(), policy.Age)
	}

	// Output:
	// expired-oauth2-tokens: oauth2_client_tokens after 24h0m0s
	// audit-log: audit_log_entries after 61320h0m0s
}

// Age is measured back from the column the Target names, so the same field
// carries two readings. Against an expires_at the row was already dead at the
// instant recorded, and Age is the grace period after it — which is why zero is
// meaningful here and nowhere else.
func ExampleTable() {
	immediate := retention.Policy{
		Name:   "expired-sessions",
		Target: retention.Table{Name: "sessions", Column: "expires_at"},
	}

	// A retention window, measured from when the row was written.
	window := retention.Policy{
		Name:   "old-sessions",
		Target: retention.Table{Name: "sessions", Column: "created_at"},
		Age:    90 * 24 * time.Hour,
	}

	for _, policy := range []*retention.Policy{&immediate, &window} {
		fmt.Printf("%s: %s, age %s\n", policy.Name, policy.Target.Describe(), policy.Age)
	}

	// Output:
	// expired-sessions: sessions, age 0s
	// old-sessions: sessions, age 2160h0m0s
}
