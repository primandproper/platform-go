package textsearchcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/samber/do/v2"
)

// RegisterIndex registers a textsearch.Index[T] with the injector. It is
// generic because an index holds documents of one concrete type; each indexed
// type is registered separately. The index name is passed here rather than
// injected, because string is too generic a type to resolve unambiguously.
//
// Prerequisites: *Config must be registered in the injector before the Index
// is invoked.
func RegisterIndex[T any](i do.Injector, indexName string) {
	do.Provide(i, func(i do.Injector) (textsearch.Index[T], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewIndex[T](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			indexName,
			WithPillars(pillars),
		)
	})
}
