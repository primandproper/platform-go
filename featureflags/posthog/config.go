package posthog

import (
	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
)

type (
	Config struct {
		ProjectAPIKey  string `env:"PROJECT_API_KEY"  json:"projectAPIKey"`
		PersonalAPIKey string `env:"PERSONAL_API_KEY" json:"personalAPIKey"`
		// Endpoint is the PostHog host. Leave empty for PostHog US Cloud (the SDK
		// default); set it for EU Cloud (https://eu.posthog.com) or self-hosted.
		Endpoint             string                    `env:"ENDPOINT"                json:"endpoint"`
		CircuitBreakerConfig circuitbreakingcfg.Config `envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig"`
	}
)
