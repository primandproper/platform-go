package httpclient

import (
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterHTTPClient registers an *http.Client with the injector, built from the
// injector's *Config. Any opts are applied after the Config and so override it.
//
// Observability comes from the injector's pillars when it has any. A container
// that registers none still wires up — every pillar resolves to its noop — but
// one whose registered provider fails to build reports that rather than
// degrading to a client that looks instrumented and records nowhere.
//
// The resilience and cache collaborators are deliberately not resolved here. A
// RateLimiter or CircuitBreaker in a container is far more often the one
// guarding the service's own inbound API, and silently repurposing it to
// throttle outbound calls would be a surprise nobody asked for. A registered
// cache.Cache is the service's own for the same reason, and filling it with
// third-party HTTP responses would evict what it was built to hold. They pass
// as options like everything else.
func RegisterHTTPClient(i do.Injector, opts ...Option) {
	do.Provide(i, func(i do.Injector) (*http.Client, error) {
		cfg := do.MustInvoke[*Config](i)

		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking observability pillars")
		}

		return NewHTTPClient(append(append(cfg.Options(), WithPillars(pillars)), opts...)...)
	})
}
