package authserver

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// The observability keys these seams attach to their operations.
//
// They are the four facts an operator needs to answer "why did this
// application stop working for one team", and they are the reason these seams
// are instrumented at all: every refusal they make is invisible on the wire by
// design — the resolver declines, and the authenticator renders one deliberately
// unspecific sentence into a page — so a refusal that recorded nothing would be
// a check nobody could confirm had ever run.
const (
	clientIDKey = "oauth2clients.client_id"
	scopeKey    = "oauth2clients.scope"
	subjectKey  = "oauth2clients.subject"
	adminKey    = "oauth2clients.administrative_login"
)

// observabilityOptions is what each of this package's three constructors keeps
// from its options until it builds its observer.
//
// It is one struct rather than three sets of fields because the three types take
// the same three pillars, resolve them the same way, and would otherwise differ
// only in which of them somebody forgot to wire.
type observabilityOptions struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// setPillars supplies all three at once, ignoring a nil Pillars so that a
// consumer that built none does not have to branch.
func (o *observabilityOptions) setPillars(pillars *observability.Pillars) {
	if pillars == nil {
		return
	}

	o.logger = pillars.Logger
	o.tracerProvider = pillars.TracerProvider
	o.metricsProvider = pillars.MetricsProvider
}
