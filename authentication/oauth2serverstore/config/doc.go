// Package oauth2serverstorecfg assembles an OAuth 2.1 authorization server and
// its Store, choosing between SQL tables and memory.
//
// It is oauth2servercfg plus one provider. The Config embeds that package's
// Config, so the issuer, the lifetimes, the scopes and the two disable switches
// resolve at the environment variables they always did, and adds the two this
// package owns: the Provider that selects a store and the Database block the
// SQL one reads.
//
// The default is the tables, and the choice is not a performance one: with the
// memory provider an authorization code issued by one replica cannot be redeemed
// at another, so a fleet fails logins in proportion to how well its load
// balancer works. That default lives here rather than in oauth2servercfg because
// it is a property of the choice, and this package is where the choice is. A
// deployment reaching oauth2servercfg directly has already said it wants memory.
//
// # Why the provider lives here and not there
//
// authentication/oauth2server is a protocol implementation — grants, codes,
// tokens, discovery — and leaves for primitives-go.
// authentication/oauth2serverstore owns the client and token tables and stays.
// A config that dispatched on a provider string named both, so it could travel
// with neither, which is the whole reason this package exists.
//
// The line the split runs along is one sentence: the provider string exists
// because a second implementation exists, so it belongs with the implementation
// that created the choice. oauth2servercfg's own doc.go records the same rule
// from the other side, along with the two exits that were refused. The same cut
// is made in rbaccfg and webauthncredentialscfg.
//
// SweepInterval stayed behind, because both stores sweep. A field's home is
// decided by which packages read it, not by which provider is the default.
//
// # Why the server is built here too
//
// oauth2servercfg.NewServer takes an oauth2server.Store, because a constructor
// that receives a store crosses nothing. That is the honest shape and it is the
// one the primitive half ships. It is also one more line at every wiring site
// that wanted the common case, so NewServer here does the selecting and then
// delegates, and a caller writes what it wrote before.
package oauth2serverstorecfg
