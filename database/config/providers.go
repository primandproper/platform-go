package databasecfg

import (
	"github.com/primandproper/platform-go/v2/database"
)

// ProvideClientConfig converts Config to database.ClientConfig.
//
//nolint:gocritic // hugeParam: intentionally accepts value for compatibility
func ProvideClientConfig(cfg Config) database.ClientConfig {
	return &cfg
}
