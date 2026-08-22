package oauth2server

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Store sentinels. Every Store implementation reports these and nothing else
// for the four outcomes a caller has to branch on, which is what lets the
// server's grant logic be written once against the interface rather than once
// per backend.
//
// ErrExpired and ErrAlreadyRedeemed wrap ErrNotFound, so a caller that only
// needs "this credential is not usable" checks that one. The distinction
// matters in exactly one place and it is the important one: a code or refresh
// token that comes back ErrAlreadyRedeemed is a replay, and a replay is the
// signal that revokes a token family. An ErrNotFound is a typo.
var (
	// ErrNotFound indicates no record is stored under the given identifier.
	ErrNotFound = platformerrors.New("oauth2 record not found")

	// ErrExpired indicates a record was found but is past its deadline. It
	// wraps ErrNotFound.
	//
	// A store reports it rather than pretending the record is absent so that
	// the expiry is decided against one clock — the store's — instead of being
	// re-derived by every caller that reads the record. See Store for why the
	// check has to happen inside the same statement that consumes it.
	ErrExpired = platformerrors.Wrap(ErrNotFound, "oauth2 record expired")

	// ErrAlreadyRedeemed indicates a one-time credential was presented twice.
	// It wraps ErrNotFound.
	//
	// Consuming methods return the record alongside this error, deliberately.
	// The record names the token family, and revoking that family is the whole
	// point of detecting the replay: without it, rotation rejects the copy the
	// attacker holds and leaves the copy the victim holds working, so nobody
	// finds out.
	ErrAlreadyRedeemed = platformerrors.Wrap(ErrNotFound, "oauth2 credential already redeemed")

	// ErrRecordExists indicates a create was given an identifier already in
	// use.
	//
	// Every identifier this package mints carries 256 bits of entropy, so this
	// means a store was handed one it did not mint rather than that two
	// credentials collided. It is an error rather than an overwrite because the
	// overwrite would be silent, and what it would silently discard is a live
	// credential somebody is holding.
	ErrRecordExists = platformerrors.New("oauth2 record identifier already in use")

	// ErrClientExists indicates a client was registered under an identifier
	// already in use. It wraps ErrRecordExists.
	//
	// It has its own sentinel because registration is the one create an
	// anonymous caller drives, so it is the one a caller is likely to branch
	// on: a silent overwrite there would let one anonymous caller take over
	// another's client by guessing an identifier.
	ErrClientExists = platformerrors.Wrap(ErrRecordExists, "oauth2 client identifier already in use")
)

// Construction and input sentinels.
var (
	// ErrNilStore indicates NewServer was called without a Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 store")

	// ErrNilAuthenticator indicates NewServer was called without a
	// SubjectAuthenticator. There is no default: an authorization server that
	// cannot tell who the human is would issue codes to anybody who asked.
	ErrNilAuthenticator = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 subject authenticator")

	// ErrEmptyIssuer indicates a Server was built without an issuer URL. Every
	// metadata document and every audience check is derived from it.
	ErrEmptyIssuer = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 issuer")

	// ErrInvalidIssuer indicates an issuer that is not an https URL with no
	// query or fragment, as RFC 8414 §2 requires.
	ErrInvalidIssuer = platformerrors.New("oauth2 issuer must be an https URL with no query or fragment")

	// ErrEmptyIdentifier indicates an empty client identifier or credential
	// hash reached a Store.
	ErrEmptyIdentifier = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 identifier")

	// ErrNilRecord indicates a nil record reached a Store's create method.
	ErrNilRecord = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 record")
)

// Protocol sentinels. These are the failures the server renders as an OAuth
// error response rather than as a platform error envelope, and they are
// exported so an application's own SubjectAuthenticator or RegistrationPolicy
// can return the same ones.
var (
	// ErrInvalidRedirectURI indicates a redirect_uri that is not among the
	// ones the client registered.
	//
	// It is never sent to the redirect_uri, because the whole point is that
	// this one is not the client's. It renders as a 400 in the browser, which
	// is the only safe place left to put it.
	ErrInvalidRedirectURI = platformerrors.New("redirect_uri is not registered for this client")

	// ErrUnknownClient indicates a client_id no registration matches.
	ErrUnknownClient = platformerrors.New("unknown oauth2 client")

	// ErrClientAuthenticationFailed indicates a client that presented no
	// credential, or the wrong one, at an endpoint that requires one.
	ErrClientAuthenticationFailed = platformerrors.New("oauth2 client authentication failed")

	// ErrPKCERequired indicates an authorization request with no S256 code
	// challenge. OAuth 2.1 requires PKCE on every authorization code request,
	// and this server has no switch to turn it off.
	ErrPKCERequired = platformerrors.New("authorization request requires an S256 code_challenge")

	// ErrPKCEVerificationFailed indicates a code_verifier whose S256 digest is
	// not the challenge the code was issued against.
	ErrPKCEVerificationFailed = platformerrors.New("code_verifier does not match the code_challenge")

	// ErrUnsupportedGrantType indicates a grant_type this server does not
	// implement. It implements authorization_code and refresh_token; OAuth 2.1
	// removes implicit and resource-owner-password, and this package does not
	// bring them back.
	ErrUnsupportedGrantType = platformerrors.New("unsupported oauth2 grant type")

	// ErrUnsupportedResponseType indicates a response_type other than "code".
	ErrUnsupportedResponseType = platformerrors.New("unsupported oauth2 response type")

	// ErrInvalidScope indicates a requested scope the client is not registered
	// for.
	ErrInvalidScope = platformerrors.New("requested scope is not registered for this client")

	// ErrInvalidResource indicates a "resource" indicator (RFC 8707) that is
	// not one this server mints tokens for.
	ErrInvalidResource = platformerrors.New("requested resource is not served by this authorization server")

	// ErrCodeClientMismatch indicates an authorization code redeemed by a
	// client other than the one it was issued to.
	ErrCodeClientMismatch = platformerrors.New("authorization code was issued to a different client")

	// ErrRedirectURIMismatch indicates a token request whose redirect_uri is
	// not the one the code was issued against.
	ErrRedirectURIMismatch = platformerrors.New("redirect_uri does not match the one the code was issued against")

	// ErrRegistrationRejected indicates a client registration a
	// RegistrationPolicy refused. It is what a policy returns when it wants a
	// 400 with an invalid_client_metadata code; wrap it to say why.
	ErrRegistrationRejected = platformerrors.New("client registration rejected")

	// ErrRegistrationNotServed indicates a registration request reaching a
	// server built with WithDynamicRegistration(false). It renders as a 404,
	// which is what the discovery document already said by leaving
	// registration_endpoint out.
	ErrRegistrationNotServed = platformerrors.New("this authorization server does not serve dynamic client registration")

	// ErrLoginFailed indicates a SubjectAuthenticator that could not identify
	// the human. The server re-renders the login form rather than failing the
	// request; wrap it to choose the message the form shows.
	ErrLoginFailed = platformerrors.New("could not authenticate the resource owner")
)
