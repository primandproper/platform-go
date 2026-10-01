/*
Package oauth2server is the OAuth 2.1 authorization server's storage promises,
assertable against any subject that serves it.

The protocol is primitives-go's: PKCE, redirect matching, the discovery
document's shape and which grants exist are decided there, and asserted there
by oauth2servertest against every store. What no test in either module proves is
the assembled chain — this module's store, behind the registry's decorator,
behind the primitive server, behind somebody's router — and the promises that
live in that chain are the ones a wiring mistake breaks:

  - an authorization code is redeemed once;
  - a code presented twice ends the tokens its first redemption issued;
  - a refresh token presented after it rotated ends its whole family;
  - a revocation ends a token for the client it was issued to, and for no
    other client.

Each of those is a row this module's oauth2serverstore writes and a read that
has to find it, through a client the registry issued. So every client here is
registered through the oauth2clients surface, in the registry of the person who
authorizes it — the server admits only a person in the registry that issued the
client — and each assertion is a code or a token redeemed against it.

# Observed through /token alone

Every promise is asserted by what /token and /revoke answer next, never by
reading a table or a metric. A replayed code is shown to have ended its family
by the refresh token it issued being refused, and a revocation by the refresh
that follows it. Whether the access token stops working too is a resource
server's question, and this module mounts none: a deployment that serves one
asserts that itself.

Every refusal has a control beside it, in the same subtest, for the same client
and person: a pair nobody replayed refreshes, a family nobody attacked rotates,
a token another client tried to revoke still works. A refresh grant that did not
work here would otherwise pass every assertion that a refresh is refused.

# What the subject supplies

HTTPSurfaces.OAuth2Server, saying the router serves the server at the paths
oauth2server.Server.Mount fixes, under BaseURL. Seams.AnonymousHTTP, because a
client application reaches /token and /revoke with nobody signed in: it
authenticates as itself, with client_secret_post, and a person's credential on
that request would be one more thing for the server to misread. The
oauth2clients surface, where the clients are registered. And
Actions.Authorized, for the one step a client cannot take: a person approving
the request at /authorize. How a person proves who they are there is the
deployment's, so the suite builds the URL — state, PKCE and the exact redirect
URI — and hands it over, and reads the code off the Location the action returns.

The discovery check needs only the first two, and the client check needs no
person; everything about codes and tokens skips without the action.

# What stayed behind

The protocol's own refusals — a PKCE method other than S256, a redirect URI one
byte off, a GET at /token, the password grant — have no platform storage in
their path, so they are primitives-go's to assert. So is a code's expiry: the
only way a deployment reaches it in a test's lifetime is by rewriting the code's
row, and the expiry check is the primitive's code.
*/
package oauth2server
