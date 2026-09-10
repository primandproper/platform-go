// Package webauthncredentialscfg assembles a WebAuthn relying party and its
// ceremony store, choosing between a SQL table and a cache.
//
// It is webauthncfg plus one provider. The Config embeds that package's Config,
// so the relying party's own settings resolve at the environment variables they
// always did, and adds the three this package owns: the Provider that selects a
// store, the Database block the table reads, and the SweepInterval only the
// table has anything to do with.
//
// The default is the table, which is the opposite of sessionscfg's. The reason
// is what the alternative defaults to: an unconfigured cache is a memory cache,
// and a memory cache holds a challenge on the replica that issued it, so the
// login that answers on another replica fails. A database provider with no
// client refuses to start; a cache provider with the wrong cache starts, works
// on one replica, and fails a fraction of logins on two. Between a loud failure
// and a quiet one, the loud one is the default.
//
// That default lives here rather than in webauthncfg because it is a property of
// the choice, and this package is where the choice is. A deployment reaching
// webauthncfg directly has already said it wants the cache.
//
// # Why the provider lives here and not there
//
// authentication/webauthn is a protocol engine — a challenge, an attestation,
// an assertion — and leaves for primitives-go.
// authentication/webauthncredentials owns the ceremony table and stays. A
// config that dispatched on a provider string named both, so it could travel
// with neither, which is the whole reason this package exists.
//
// The line the split runs along is one sentence: the provider string exists
// because a second implementation exists, so it belongs with the
// implementation that created the choice. webauthncfg's own doc.go records the
// same rule from the other side, along with the two exits that were refused.
// The same cut is made in rbaccfg and oauth2serverstorecfg.
//
// # Why the relying party is built here too
//
// webauthncfg.NewRelyingParty takes a webauthn.SessionStore, because a
// constructor that receives a store crosses nothing. That is the honest shape
// and it is the one the primitive half ships. It is also one more line at every
// wiring site that wanted the common case, so NewRelyingParty here does the
// selecting and then delegates, and a caller writes what it wrote before.
package webauthncredentialscfg
