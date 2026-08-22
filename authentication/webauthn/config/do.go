package webauthncfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterSessionStore registers a webauthn.SessionStore with the injector.
//
// Prerequisites: context.Context and *Config must be registered. A
// database.Client is resolved only if one is registered, so a container running
// the cache provider needs none — and one whose registered client fails to
// build still hears about it.
func RegisterSessionStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webauthn.SessionStore, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewSessionStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			WithPillars(pillars),
		)
	})
}

// RegisterRelyingParty registers a *webauthn.RelyingParty with the injector,
// along with the ceremony store behind it.
//
// It does not resolve a registered webauthn.SessionStore. The store is built
// from the same *Config as the relying party, so resolving one would let a
// container hold two stores — the registered one and this one's — with only the
// second in the ceremonies. Register this alone unless something else needs the
// store on its own, and then register both from the same Config.
//
// Prerequisites are RegisterSessionStore's.
func RegisterRelyingParty(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*webauthn.RelyingParty, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewRelyingParty(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			db,
			WithPillars(pillars),
		)
	})
}
