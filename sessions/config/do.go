package sessionscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"

	"github.com/samber/do/v2"
)

// RegisterStore registers a sessions.Store[T] with the injector. It is generic
// because a store holds one concrete payload type; an application with two
// kinds of session registers each separately.
//
// Prerequisites: context.Context and *Config must be registered. A
// database.Client is resolved only if one is registered, so a container running
// the cache provider needs none — and one whose registered client fails to
// build still hears about it.
func RegisterStore[T any](i do.Injector) {
	do.Provide(i, func(i do.Injector) (sessions.Store[T], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewStore[T](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			WithPillars(pillars),
		)
	})
}

// RegisterManager registers a *sessions/http.Manager[T] with the injector,
// along with the store behind it.
//
// Prerequisites: context.Context, *Config, and cookies.Manager must be
// registered. As with RegisterStore, a database.Client is resolved only if one
// is registered.
func RegisterManager[T any](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*sessionshttp.Manager[T], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewManager[T](
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			do.MustInvoke[cookies.Manager](i),
			WithPillars(pillars),
		)
	})
}
