package oauth2servercfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an oauth2server.Store with the injector.
//
// Prerequisites: context.Context and *Config must be registered. A
// database.Client is resolved only if one is registered, so a container running
// the memory provider needs none — and one whose registered client fails to
// build still hears about it, rather than silently degrading to maps.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (oauth2server.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			WithPillars(pillars),
		)
	})
}

// RegisterServer registers a *oauth2server.Server with the injector, along with
// the store behind it.
//
// Prerequisites: context.Context, *Config, and an
// oauth2server.SubjectAuthenticator must be registered. The authenticator is a
// hard prerequisite rather than an optional one — unlike the database client —
// because there is no such thing as an authorization server that cannot tell
// who the human is, so a container missing one should fail to build rather than
// come up.
func RegisterServer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*oauth2server.Server, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewServer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			do.MustInvoke[oauth2server.SubjectAuthenticator](i),
			WithPillars(pillars),
		)
	})
}
