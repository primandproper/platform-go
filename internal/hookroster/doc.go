/*
Package hookroster is where every store write in this module is checked against
the hook it owes, and it holds nothing else.

A store that takes Hooks calls one After method per write that takes the
caller's database.Tx, and the pairing is what a consumer's audit trail rests
on: a write added to a Store later that nobody gave a hook still compiles,
still passes every test its own package has, and commits its row with no
audit entry and no outbox event beside it. The hooks suites in each package
cannot catch that, because they name the writes they exercise by hand — the
write somebody forgot to hook is the write nobody remembered to add there.

So the pairing is read off the interfaces rather than listed. Every method of
a rostered Store whose second parameter is a database.Tx must have an After
method of the same name on that package's Hooks, every After method must have
the write it is named for, and a write that takes a Tx and owes no hook is an
exemption that says why. A package that declares a Hooks interface and is on
neither list fails here too, so a new one is classified rather than missed.
*/
package hookroster
