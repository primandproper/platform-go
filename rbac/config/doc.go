// Package rbaccfg selects and builds an authorization.PolicyResolver,
// choosing between policy declared in configuration and policy stored in SQL.
//
// It is authorizationcfg plus one provider. The Config embeds that package's
// Config, so every field an operator sets today resolves at the same
// environment variable it always did, and adds the two this package owns: the
// Provider that selects a backend, and the Database block the SQL one reads.
//
// A deployment that resolves policy from declarations needs none of this and
// should call authorizationcfg directly.
//
// # Why the provider lives here and not there
//
// authorization is a provider behind an interface and leaves for
// primitives-go. rbac owns the roles and permissions tables and stays. A
// config that dispatched on a provider string named both, so it could travel
// with neither — which is the whole reason this package exists.
//
// The line the split runs along is one sentence: the provider string exists
// because a second implementation exists, so it belongs with the implementation
// that created the choice. Before this package there was one resolver a config
// could build without a database, and one it could not; the string that chose
// between them was doing work on behalf of the half that owns a table, and it
// now lives beside it. authorizationcfg's own doc.go records the same rule from
// the other side, along with the two exits that were refused.
//
// The same cut is made in webauthncredentialscfg and oauth2serverstorecfg, and
// in each of the three the primitive half is left with exactly one
// implementation to build, which is nothing to select between.
//
// # What it does not duplicate
//
// The caching decorator is applied to whichever resolver is chosen, and the
// call that applies it is authorizationcfg.NewCachedResolver rather than a
// second copy here. A second copy could drift — a different default, a dropped
// option, a TTL read off the wrong field — and nothing would say so.
package rbaccfg
