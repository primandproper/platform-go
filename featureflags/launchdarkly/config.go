package launchdarkly

import (
	"time"

	circuitbreakingcfg "github.com/primandproper/platform-go/v6/circuitbreaking/config"
)

type (
	Config struct {
		SDKKey               string                    `env:"SDK_KEY"                 json:"sdkKey"               yaml:"sdkKey"`
		CircuitBreakerConfig circuitbreakingcfg.Config `envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig" yaml:"circuitBreakerConfig"`
		InitTimeout          time.Duration             `env:"INIT_TIMEOUT"            json:"initTimeout"          yaml:"initTimeout"`
	}
)
