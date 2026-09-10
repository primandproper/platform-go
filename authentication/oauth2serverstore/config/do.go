package oauth2serverstorecfg

import (
	"context"

	"github.com/primandproper/primitives-go/authentication/oauth2server"
	"github.com/primandproper/primitives-go/config/injection"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an oauth2server.Store with the injector.
//
// It provides the same key as oauth2servercfg.RegisterStore, so exactly one of
// the two belongs in any given injector: this one where records may live in SQL,
// that one where they never do.
//
// Prerequisites: context.Context and *Config must be registered. A
// database.Client is resolved only if one is registered, so a container running
// the memory provider needs none — and one whose registered client fails to
// build still hears about it.
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
// the Store behind it.
//
// The oauth2server.SubjectAuthenticator is a hard prerequisite rather than an
// optional resolution: it is how this deployment identifies a human, and a
// container that has not registered one should fail to come up rather than
// build a server that issues authorization codes to whoever asks.
//
// A container that would rather hold one store and read it registers
// RegisterStore here and oauth2servercfg.RegisterServer over it: that
// registration takes the store as a dependency rather than building one.
//
// Prerequisites are RegisterStore's, plus an oauth2server.SubjectAuthenticator.
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
