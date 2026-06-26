package posthog

import (
	circuitbreakingcfg "github.com/primandproper/platform-go/circuitbreaking/config"
)

type (
	Config struct {
		ProjectAPIKey        string                    `env:"PROJECT_API_KEY"         json:"projectAPIKey"`
		PersonalAPIKey       string                    `env:"PERSONAL_API_KEY"        json:"personalAPIKey"`
		CircuitBreakerConfig circuitbreakingcfg.Config `envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig"`
	}
)
