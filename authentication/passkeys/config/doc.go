/*
Package passkeyscfg assembles a passkeys Store and Service from environment
configuration.

One setting: the table prefix, which has to match the one the migrations were
rendered with. The dialect comes from the database.Client, so it cannot disagree
with the database the statements run against.

Everything else the Service is built from is not configuration, and the absence
of a field for each is the reason this package resolves it from the injector:

  - The relying party is the *webauthn.RelyingParty the WebAuthn block
    registers, so the ceremony state a passkey sign-in keeps between its two
    requests is the store every other ceremony uses.
  - The passkeys.UserResolver is the application's: which of its users a
    WebAuthn handle names is its directory's answer, and passkeys never imports
    one. It is required.
  - The passkeys.EnrollmentGate is the application's too, and required with no
    default, for the reason passkeys.ErrNoEnrollmentGate gives: whether a live
    session is proof enough to add a way into an account is a question only
    the application's sessions can answer. A deployment that has decided it is
    registers passkeys.AdmitEveryEnrollment by name.
  - passkeys.Hooks are the application's and required. A deployment whose
    passkey writes owe no companions registers passkeys.NoopHooks{} by name.
  - A passkeys.UsernameResolver and a passkeys.AlternativeSignIn are the
    application's and optional. Absent, only the discoverable login is offered
    and the last-credential guard assumes nobody has a password.
*/
package passkeyscfg
