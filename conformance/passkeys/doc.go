/*
Package passkeys is the passkey surface, as promises assertable against any
subject that mounts it.

The promise the surface exists for is the first one: a passkey that was
enrolled signs its owner in to the token a password would have — sign-in's
token, naming the registrant, good for every other surface. The rest are what
makes that safe. A login's challenge is spent once, so a replayed assertion is
refused; a challenge nobody issued is refused; a cloned authenticator, whose
counter did not advance past the one the deployment last saw, is refused with
no token issued. A registration's challenge is spent once too, so a replayed
attestation enrolls nothing new. A username nobody holds begins a login exactly
as one somebody holds does. A passkey is its owner's, so a colleague archiving
it finds nothing. And the last passkey of somebody with no password stays.

# The authenticator, and the relying party it answers as

Every ceremony here is answered by primitives-go's virtual authenticator,
answering as Seams.WebAuthn names: the relying party's ID, and the origin a
browser would report signing from. The origin has to match the deployment's
exactly, scheme and port included — a deployed subject behind TLS names an
https origin — and a subject that names neither skips the suite.

The authenticator verifies the person behind every tap, so a sign-in here is
two factors on its own and asks for no second one whatever the deployment's
policy. Whether a key tap alone is asked for one is sign-in's rule, asserted in
authentication/passkeys/grpc's own tests.

# The directory

The login half arrives with nobody on it, so whose directory a login is
against comes off the connection, and service.New leaves that at the global
directory. The callers these assertions enroll are asked for there, the reading
conformance/passwordreset takes of the same question, and a subject with no
callers there declines and the assertions skip.

# What each assertion needs

Most need only the surface. Reading whom a token names needs the sign-in
surface and Seams.SignedIn. A named login needs the sign-in surface to read the
caller's username. The last-passkey assertion needs somebody with no password
at all, which is a registration naming none and a mailed sign-in link, so it
needs the sign-in surface and the MagicLinkToken action, and skips without
either.

What stays out is anything read off a row. dinnerdonebetter asserted durable
single consumption by counting ceremony rows and a sign count by reading its
column; a seam describes an action, not a table, so both are asserted here by
what a client sees instead.
*/
package passkeys
