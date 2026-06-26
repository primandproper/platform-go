package config

import (
	"github.com/primandproper/platform-go/uploads/objectstorage"

	"github.com/samber/do/v2"
)

// RegisterStorageConfig registers an *objectstorage.Config with the injector,
// extracted from the parent *Config.
// Prerequisite: *Config must be registered in the injector before calling this.
func RegisterStorageConfig(i do.Injector) {
	do.Provide[*objectstorage.Config](i, func(i do.Injector) (*objectstorage.Config, error) {
		cfg := do.MustInvoke[*Config](i)
		return &cfg.Storage, nil
	})
}
