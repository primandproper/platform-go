package signin

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The refusals a caller acts on. Each is a sentinel rather than a code so that
// a consumer branching on one is branching on what happened rather than on a
// string, and each has a case in this package's mappers — see errormappers.go
// for the ones deliberately absent from them.
var (
	// ErrInvalidCredentials is every way a password sign-in fails to prove
	// anything: a handle nobody holds, a password that does not match, a
	// second-factor code that does not validate, and a user who holds no
	// password credential at all.
	//
	// It is one sentinel on purpose, and it is the whole reason this package
	// exists rather than a doc comment over the engines. Told apart, the four
	// are an oracle: "no such user" enumerates a directory, and "wrong code"
	// confirms a password to whoever is guessing them. Told apart by a
	// stopwatch they are the same oracle more slowly, which is why an unknown
	// handle costs the same hash a known one does.
	//
	// The one refusal that is not this is ErrSecondFactorRequired, and the
	// package documentation says why it cannot be.
	ErrInvalidCredentials = platformerrors.New("invalid credentials")

	// ErrSecondFactorRequired indicates a user who holds a proven second factor
	// and sent no code with their password.
	//
	// It tells the caller the password was right, which is a disclosure and is
	// unavoidable: a client that cannot be told to ask for a code cannot ask for
	// one. What it does not do is tell them whether a code they sent was right —
	// that is ErrInvalidCredentials, and the two are separate for exactly that
	// reason.
	ErrSecondFactorRequired = platformerrors.New("a second-factor code is required")

	// ErrSecondFactorNotEnrolled indicates a sign-in refused because the user
	// holds no proven second factor and this sign-in required one — either
	// because the service runs SecondFactorRequired, or because it is the
	// administrative door, which requires one always.
	//
	// The remedy is enrollment, which is Service.RefreshTOTPSecret followed by
	// Service.VerifyTOTPSecret, and both of those need a sign-in the user cannot
	// have. Resolving that is the consumer's: a grace period on the policy, an
	// enrollment link, or an operator. It is not resolvable here, and a service
	// that switched to SecondFactorRequired with nobody enrolled has locked
	// everybody out — which is why the policy's own documentation says to enroll
	// first.
	ErrSecondFactorNotEnrolled = platformerrors.New("no proven second factor is enrolled")

	// ErrUserUnverified indicates a user who has not yet proven whatever
	// registration asked of them. It is identity.StatusUnverified, which is the
	// status CreateUser assigns when none is set.
	ErrUserUnverified = platformerrors.New("user has not completed verification")

	// ErrUserBanned indicates a user an operator has suspended. The explanation
	// is on the user's row and is meant to be shown to them; it is wrapped
	// around this sentinel rather than replacing it, so a caller matches the
	// sentinel and reads the explanation off the message.
	ErrUserBanned = platformerrors.New("user is suspended")

	// ErrUserTerminated indicates a user whose access has ended permanently. It
	// is separate from ErrUserBanned because the remedies differ and only one of
	// them exists: a suspension is reversible and a termination is not.
	ErrUserTerminated = platformerrors.New("user access has been terminated")

	// ErrNotAnAdministrator indicates an administrative sign-in by somebody who
	// holds none of the service roles the consumer named as administrative.
	ErrNotAnAdministrator = platformerrors.New("user is not an administrator")

	// ErrAdminLoginDisabled indicates an administrative sign-in against a
	// service that named no administrative roles.
	//
	// It maps to the same code ErrNotAnAdministrator does, so a caller cannot
	// tell a service with no administrative door from one whose door they are
	// not admitted through. The two are separate sentinels anyway, because the
	// second is a consumer's wiring and the first is a user's roles, and a
	// consumer reading their own logs needs to know which.
	ErrAdminLoginDisabled = platformerrors.New("administrative sign-in is not configured")

	// ErrNoPasswordCredential indicates a password change for a user who holds
	// no password — a passkey-only or federated registration.
	//
	// This is the distinction identity.User.HasPassword draws, made where it is
	// safe to make: the caller is signed in and is the subject, so "you have no
	// password to change" tells them nothing they did not already know. The
	// anonymous sign-in path refuses the same user with ErrInvalidCredentials,
	// because there the same sentence names a user to whoever guessed a handle.
	ErrNoPasswordCredential = platformerrors.New("user holds no password credential")

	// ErrRefreshTokenReused indicates a refresh token presented after it had
	// already been exchanged, and it is the one refusal in this package that is
	// also an alarm.
	//
	// A refresh token is single-use, so a second presentation means two parties
	// hold one credential: whoever signed in, and whoever took a copy. Which of
	// them is presenting it now cannot be told from here — a thief's replay and a
	// client's retry are the same request — so the answer is to end the family,
	// which signs both of them out. That is the standard response and the only
	// one that does not leave the theft running.
	//
	// It wraps ErrInvalidCredentials, and that is what makes the collapse real
	// rather than asserted. The two transports answer differently by
	// construction: an HTTPErrorMapper returns the words it wants a client to
	// read, so mapping this case to "invalid credentials" is one line there,
	// while a GRPCErrorMapper returns only a code and the message comes from
	// errors/grpc.ClientSafeMessage — which walks the chain and quotes the first
	// registered sentinel it reaches. A sentinel that is registered says its own
	// words; one that is not falls through to the handler's description, which
	// is a different string from "invalid credentials" and therefore an oracle
	// for exactly the question this sentinel must not answer. Wrapping puts
	// ErrInvalidCredentials in the chain, so the walk reaches it and a replay
	// reads identically to a wrong password on both transports.
	//
	// It is itself deliberately absent from ClientSafeSentinels, which is the
	// other half: registered, it would speak these words, and "that token was
	// already spent" tells a thief their theft was detected. Absent, the walk
	// passes over it and quotes what it wraps. The sentinel stays legible in the
	// consumer's own logs and to errors.Is, which is where it is useful.
	//
	// A caller matching ErrInvalidCredentials therefore matches this too, and
	// that is the intent rather than a side effect: a replayed refresh token is
	// an invalid credential, and a consumer branching on the refusal wants the
	// same branch. Reuse is still reachable on its own for whoever wants to
	// alarm on it.
	ErrRefreshTokenReused = platformerrors.Wrap(ErrInvalidCredentials, "refresh token has already been exchanged")

	// ErrTOTPIssuerNotConfigured indicates Service.RefreshTOTPSecret on a
	// service built without WithTOTPIssuer.
	//
	// It is refused at the call rather than at construction because a consumer
	// who never enrolls a second factor should not have to name a label for one.
	// It is a wiring failure and reads as one: no status is mapped for it, so it
	// is a 500, which is what it is.
	ErrTOTPIssuerNotConfigured = platformerrors.New("no TOTP issuer label is configured")

	// ErrRefreshTokensNotConfigured indicates one of the three refresh doors on
	// a service built without WithRefreshTokenStore.
	//
	// Such a service issues one token per sign-in and holds no table, which is
	// what this package did before rotation existed and is still the right shape
	// for a consumer using Service.Authenticate as a credential check. Reaching
	// for a refresh door from one is a wiring failure and reads as one: no status
	// is mapped for it, so it is a 500, which is what it is.
	ErrRefreshTokensNotConfigured = platformerrors.New("no refresh token store is configured")

	// ErrRefreshTokenTTLTooShort indicates a service whose refresh tokens would
	// die before the access tokens they mint.
	//
	// It is refused at construction because it is not recoverable at a call: a
	// client holding a working access token has no reason to refresh, so by the
	// time it does the family is already gone, and the sign-in ends at a moment
	// nothing chose. It is a wiring failure and no status is mapped for it.
	ErrRefreshTokenTTLTooShort = platformerrors.New("refresh token lifetime is shorter than the token lifetime")
)

// The nil-argument and empty-argument refusals. Each wraps a platform sentinel,
// so the platform mappers answer them and this package's own mappers say
// nothing about them — a case here would be a second copy of a decision
// errors/http and errors/grpc already make.
var (
	// ErrNilDatabaseClient indicates a nil database.Client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the sign-in service")

	// ErrNilDirectory indicates a nil Directory.
	ErrNilDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil directory for the sign-in service")

	// ErrNilAuthenticator indicates a nil authentication.Authenticator. There is
	// no default: which engine hashes a password is the one thing this package
	// must not choose on a consumer's behalf.
	ErrNilAuthenticator = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil authenticator for the sign-in service")

	// ErrNilTokenIssuer indicates a nil tokens.Issuer.
	ErrNilTokenIssuer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil token issuer for the sign-in service")

	// ErrNilCredentials indicates a nil *Credentials.
	ErrNilCredentials = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in credentials")

	// ErrNilPasswordUpdate indicates a nil *PasswordUpdate.
	ErrNilPasswordUpdate = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password update")

	// ErrNilSecretRefresh indicates a nil *SecretRefresh.
	ErrNilSecretRefresh = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil second-factor secret refresh")

	// ErrEmptyUserID indicates an operation on nobody.
	ErrEmptyUserID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty user ID")

	// ErrEmptyHandle indicates credentials naming neither a username nor an
	// email address.
	//
	// It is not ErrInvalidCredentials: an empty form is a client that did not
	// submit rather than a guess that missed, and answering it with a refusal
	// would put a hash comparison behind every empty request a bot sends.
	ErrEmptyHandle = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "credentials name neither a username nor an email address")

	// ErrEmptyPassword indicates credentials with no password on them. See
	// ErrEmptyHandle for why it is not a refusal.
	ErrEmptyPassword = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty password")

	// ErrAmbiguousHandle indicates credentials naming both a username and an
	// email address.
	//
	// Refused rather than resolved by precedence, which is the reading this
	// module takes of a request whose two halves disagree: a client sending
	// both has a bug, and picking one for them makes it a bug that signs
	// somebody in.
	ErrAmbiguousHandle = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "credentials name both a username and an email address")

	// ErrEmptyRefreshToken indicates an exchange that presented nothing.
	//
	// It is not ErrInvalidCredentials, for the reason ErrEmptyHandle is not: an
	// empty request is a client that did not submit rather than a guess that
	// missed, and answering it with a refusal would put a database round trip
	// behind every empty request a bot sends.
	ErrEmptyRefreshToken = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty refresh token")

	// ErrEmptyFamilyID indicates a revocation that named no login.
	ErrEmptyFamilyID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty refresh token family ID")
)
