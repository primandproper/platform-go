package configroster_test

import (
	"reflect"
	"testing"

	oauth2serverstorecfg "github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/config"
	webauthncredentialscfg "github.com/primandproper/platform-go/v14/authentication/webauthncredentials/config"
	entitlementscfg "github.com/primandproper/platform-go/v14/entitlements/config"
	linkscfg "github.com/primandproper/platform-go/v14/links/config"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	"github.com/primandproper/platform-go/v14/service"
	sessionscfg "github.com/primandproper/platform-go/v14/sessions/config"
	timerscfg "github.com/primandproper/platform-go/v14/timers/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// platformModule is the prefix of every package path this module declares. The
// walk below descends into a nested config only when this module declares its
// type: how primitives-go spells the prefixes inside its own configs is
// primitives-go's to decide, and a test here could only red on it, never fix it.
const platformModule = "github.com/primandproper/platform-go/v14/"

// unreachableRoots are the config subpackages service.Config does not nest.
//
// They are the generic and per-type subsystems its documentation says are
// deliberately absent — registered by a call that supplies a type argument or an
// index name no environment can — plus the two stores that hang off a
// primitives-go engine. An operator still sets their variables, so their nesting
// is still subject to the rule, and listing them here is what keeps the walk
// from being a walk of only half the module.
var unreachableRoots = []any{
	oauth2serverstorecfg.Config{},
	webauthncredentialscfg.Config{},
	entitlementscfg.Config{},
	linkscfg.Config{},
	mediaregistrycfg.Config{},
	sessionscfg.Config{},
	timerscfg.Config{},
}

// nesting is one `envPrefix:` tag found below the top level of service.Config.
type nesting struct {
	// typ is the config the prefix is nested at, pointers dereferenced.
	typ reflect.Type
	// where is the declaring struct and field, as a reader would say it:
	// "webhooks.WorkerConfig.Backoff".
	where string
	// prefix is the tag's value, trailing underscore included.
	prefix string
}

// roleNamed records the nestings that spell a prefix for the role the config
// plays rather than for the subsystem it is, and says why for each.
//
// A row is a decision, not an exemption: the rule below only reaches a nesting
// whose type service.Config also nests, so every row here is a place where an
// operator meets the same config under two names and the second one is the
// better name. Adding a row means arguing that.
var roleNamed = map[string]string{
	// Four components take a retry policy, and it is never the service's retry
	// defaults — it is how that one loop backs off. saga settles it: it holds
	// two of them, so RETRY_ could not name both even if the reading were
	// wrong.
	"outbox.RelayConfig.Backoff":            "the relay's own republish backoff",
	"saga.WorkerConfig.Backoff":             "the step loop's backoff, one of this struct's two",
	"saga.WorkerConfig.CompensationBackoff": "the compensation loop's backoff, the other",
	"metering.FlusherConfig.Backoff":        "the flush protocol's backoff",
	"webhooks.WorkerConfig.Backoff":         "the delivery loop's backoff",

	// QUEUE_ is what a component calls the queue it works through, whichever
	// queue that is: operations nests a workqueue.Config under the same name,
	// and it has no top-level spelling to agree or disagree with.
	"outboxcfg.Config.Queue": "the queue the relay publishes claimed messages to",
}

// TestNestedEnvPrefixesMatchTheTopLevelSpelling asserts that a config nested
// inside another is nested under the prefix service.Config gives it at the top,
// or is declared above as naming a role instead.
//
// The failure this exists for is not a compile break. webhooks nested
// circuitbreaking under CIRCUIT_BREAKER_ and httpclient under HTTP_, where the
// top level spells them CIRCUIT_BREAKING_ and HTTP_CLIENT_, so an operator who
// had learned one of those names got nothing for setting it — no error, just the
// default, in a deployment that looked configured. Renaming them cost every
// environment file that had set them, which is a bill that only grows, so the
// point of this test is that the next one is caught before a tag rather than
// after.
//
// Only a config service.Config also nests is checked. A prefix with no top-level
// counterpart — WORKER_, SWEEPER_, WATCHER_, CHECKER_, FULFILLER_ — has nothing
// to disagree with and names whatever reads best where it is.
func TestNestedEnvPrefixesMatchTheTopLevelSpelling(T *testing.T) {
	T.Parallel()

	topLevel := topLevelPrefixes()
	must.SliceNotEmpty(T, reflect.ValueOf(topLevel).MapKeys(),
		must.Sprint("service.Config nests nothing under an envPrefix, so this test is checking against an empty roster"))

	seen := map[reflect.Type]bool{}
	nestings := []nesting{}

	// service.Config's own fields are the spelling being compared against, so
	// they are descended into but not collected.
	walk(reflect.TypeFor[service.Config](), seen, false, &nestings)

	for _, root := range unreachableRoots {
		walk(reflect.TypeOf(root), seen, true, &nestings)
	}

	claimed := map[string]bool{}

	for _, n := range nestings {
		want, nested := topLevel[n.typ]
		if !nested {
			continue
		}

		if reason, declared := roleNamed[n.where]; declared {
			claimed[n.where] = true
			test.NotEqOp(T, "", reason, test.Sprintf("%s is declared role-named with no reason given", n.where))

			continue
		}

		test.EqOp(T, want, n.prefix, test.Sprintf(
			"%s nests %s under %q, but service.Config spells it %q — either match it, or add a roleNamed row saying why this one reads better",
			n.where, n.typ, n.prefix, want))
	}

	// The other direction: a row that no longer describes a nesting is a
	// decision about code that has moved on, and reading it would mislead.
	for where := range roleNamed {
		test.True(T, claimed[where], test.Sprintf(
			"roleNamed has a row for %s, but no nesting there conflicts with a top-level spelling any more; delete it", where))
	}
}

// topLevelPrefixes reads service.Config: each subsystem's config type against
// the prefix an operator sets it under.
func topLevelPrefixes() map[reflect.Type]string {
	out := map[reflect.Type]string{}

	for field := range reflect.TypeFor[service.Config]().Fields() {
		if prefix, ok := field.Tag.Lookup("envPrefix"); ok {
			out[deref(field.Type)] = prefix
		}
	}

	return out
}

// walk collects every `envPrefix:` tag below t, descending only into configs
// this module declares. collect is false for the one level that is the
// comparand rather than a nesting: service.Config's own fields.
func walk(t reflect.Type, seen map[reflect.Type]bool, collect bool, out *[]nesting) {
	t = deref(t)
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}

	seen[t] = true

	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}

		fieldType := deref(field.Type)

		if prefix, ok := field.Tag.Lookup("envPrefix"); ok && collect {
			*out = append(*out, nesting{
				typ:    fieldType,
				where:  t.String() + "." + field.Name,
				prefix: prefix,
			})
		}

		// An unnamed struct field is a grouping the declaring package wrote
		// inline, so it belongs to whichever module declared the struct holding
		// it, which is one this walk has already accepted.
		if isPlatform(fieldType) || (fieldType.Kind() == reflect.Struct && fieldType.Name() == "") {
			walk(fieldType, seen, true, out)
		}
	}
}

// isPlatform reports whether this module declares t.
func isPlatform(t reflect.Type) bool {
	return len(t.PkgPath()) > len(platformModule) && t.PkgPath()[:len(platformModule)] == platformModule
}

// deref resolves a pointer field to the type an operator actually configures.
func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}
