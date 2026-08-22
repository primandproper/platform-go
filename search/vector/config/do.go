package vectorsearchcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"

	"github.com/samber/do/v2"
)

// RegisterIndex registers a vectorsearch.Index[T] with the injector. It is
// generic because an index holds documents of one concrete type; each indexed
// type is registered separately. The index name is passed here rather than
// injected, because string is too generic a type to resolve unambiguously.
//
// Prerequisites: *Config must be registered in the injector before the Index
// is invoked. A database.Client is only required when the config's provider
// is pgvector, so a qdrant-backed service can build without one.
func RegisterIndex[T any](i do.Injector, indexName string) {
	do.Provide(i, func(i do.Injector) (vectorsearch.Index[T], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		var db database.Client
		if cfg.Provider == PGvectorProvider {
			client, clientErr := do.Invoke[database.Client](i)
			if clientErr != nil {
				return nil, clientErr
			}
			db = client
		}

		return NewIndex[T](
			do.MustInvoke[context.Context](i),
			cfg,
			db,
			indexName,
			WithPillars(pillars),
		)
	})
}
