package signin_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// redeemToken spends the secret of the most recently mailed link.
func redeemToken(tb testing.TB, e *env) string {
	tb.Helper()

	return e.mailer.last(tb).Issuance.Secret
}

// TestRequestMagicLink_mailsALinkForAnAddressSomebodyHolds is the happy path,
// and the assertion that matters most is the last one: the secret that went to
// the mailer is not what the table holds.
func TestRequestMagicLink_mailsALinkForAnAddressSomebodyHolds(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	must.EqOp(t, 1, e.mailer.count())

	mail := e.mailer.last(t)

	test.EqOp(t, e.user.ID, mail.User.ID)
	test.EqOp(t, e.user.ID, mail.Issuance.Link.SubjectID)
	must.StrNotEqFold(t, "", mail.Issuance.Secret)

	// The mailer is handed a redacted user, so a consumer rendering a template
	// from it cannot put a password hash in an email.
	test.EqOp(t, "", mail.User.HashedPassword)
}

// TestRequestMagicLink_foldsTheAddressLikeTheDirectory pins that this door reads
// the row the password door would have read.
//
// A second copy of a normalization is a copy that can disagree with the rows,
// and the symptom would be a person whose address works at one door and not the
// other.
func TestRequestMagicLink_foldsTheAddressLikeTheDirectory(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, "JANE@EXAMPLE.COM"))

	must.EqOp(t, 1, e.mailer.count())
	test.EqOp(t, e.user.ID, e.mailer.last(t).Issuance.Link.SubjectID)
}

// TestRequestMagicLink_answersTheSameWayForEverybody is the enumeration defense,
// and it is the property this door exists under rather than a nicety.
//
// An address nobody holds, a banned owner and a terminated owner are all a nil
// error and no mail — the same answer somebody with a good account gets, minus
// the mail they cannot see.
func TestRequestMagicLink_answersTheSameWayForEverybody(T *testing.T) {
	T.Parallel()

	T.Run("an address nobody holds", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		test.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, "nobody@example.com"))
		test.EqOp(t, 0, e.mailer.count())
	})

	T.Run("a banned owner", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)
		e.setStatus(t, identity.StatusBanned, "for cause")

		test.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))
		test.EqOp(t, 0, e.mailer.count())
	})

	T.Run("a terminated owner", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)
		e.setStatus(t, identity.StatusTerminated, "")

		test.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))
		test.EqOp(t, 0, e.mailer.count())
	})

	T.Run("an address in another directory", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		test.NoError(t, e.svc.RequestMagicLink(t.Context(), tenancy.Of("tenant_b"), e.user.EmailAddress))
		test.EqOp(t, 0, e.mailer.count())
	})
}

// TestRequestMagicLink_mailsAnUnverifiedRegistrant is the case this door exists
// for, and the one place it parts company with the password door.
//
// A registrant has not proven their address, so LoginForToken refuses them. The
// link this mails is how they prove it, so refusing them here would mean the
// mail that proves an address cannot be the mail that signs somebody in.
func TestRequestMagicLink_mailsAnUnverifiedRegistrant(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	registrant := e.registerUnverified(t, "newcomer")

	must.EqOp(t, identity.StatusUnverified, registrant.AccountStatus)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, registrant.EmailAddress))

	must.EqOp(t, 1, e.mailer.count())
	test.EqOp(t, registrant.ID, e.mailer.last(t).Issuance.Link.SubjectID)
}

// TestRequestMagicLink_reportsItsOwnFailures pins the line between "a fact about
// this address", which is silence, and "this service is broken", which is not.
//
// A mailer that will not send is the second: the row is already committed by
// then, so the link exists and nobody has it, and a nil return would tell a
// caller a mail went out that did not.
func TestRequestMagicLink_reportsItsOwnFailures(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	e.mailer.fail(platformerrors.New("the mail server is down"))

	err := e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress)

	test.Error(t, err)
	test.StrContains(t, err.Error(), "mail server is down")
}

// TestRequestMagicLink_refusals pins the two arguments it will not take and the
// wiring failure it reports rather than answering silently.
func TestRequestMagicLink_refusals(T *testing.T) {
	T.Parallel()

	T.Run("an empty address", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		test.ErrorIs(t, e.svc.RequestMagicLink(t.Context(), testScope, ""), signin.ErrEmptyHandle)
	})

	// A service with no store is a misconfiguration, and it is reported rather
	// than answered with the silence a stranger's address gets — otherwise a
	// deployment that forgot to wire the door looks exactly like one that
	// works.
	T.Run("a service with no link store", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		test.ErrorIs(t,
			e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress),
			signin.ErrMagicLinksNotConfigured)
	})

	// And a store with no mailer, which is the half a consumer is likeliest to
	// leave out: the store alone is enough to redeem a link somebody else sent.
	T.Run("a service with a store and no mailer", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
			signin.WithMagicLinkStore(e.magicLinks))
		must.NoError(t, err)

		test.ErrorIs(t,
			svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress),
			signin.ErrMagicLinksNotConfigured)
	})
}

// TestRequestMagicLink_padsItsOwnTiming is the other half of the enumeration
// defense, and the half a response shape cannot provide.
//
// The floor is measured from the moment the call begins, so the path that found
// nobody is held to the same deadline as the path that minted, committed and
// mailed. The value here is large enough to be unambiguous against a SQLite
// round trip and small enough not to make the suite slow.
func TestRequestMagicLink_padsItsOwnTiming(T *testing.T) {
	T.Parallel()

	const floor = 150 * time.Millisecond

	for name, address := range map[string]string{
		"an address somebody holds": "jane@example.com",
		"an address nobody holds":   "nobody@example.com",
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			e := newMagicLinkEnv(t, signin.WithMagicLinkRequestFloor(floor))

			started := time.Now()
			must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, address))

			test.True(t, time.Since(started) >= floor,
				test.Sprintf("answered in %v, floor is %v", time.Since(started), floor))
		})
	}
}

// TestRedeemMagicLink_signsSomebodyIn is the door's happy path: the same token,
// the same family and the same refresh token a password sign-in produces.
func TestRedeemMagicLink_signsSomebodyIn(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})

	must.NoError(t, err)
	must.NotNil(t, signedIn)

	test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
	test.EqOp(t, e.accountID, signedIn.Principal.ActiveAccountID)
	test.False(t, signedIn.Administrative)
	test.EqOp[any](t, false, e.issuer.claims[signin.ClaimAdministrative])
	must.StrNotEqFold(t, "", signedIn.Token)

	// A refresh token is minted because a store is configured, which is the
	// property that makes this a sign-in rather than a one-off token.
	must.StrNotEqFold(t, "", signedIn.RefreshToken)

	// And the family names this login, so a consumer's hook and the refresh
	// token's row agree about which sign-in they belong to.
	must.StrNotEqFold(t, "", signedIn.FamilyID)
}

// TestRedeemMagicLink_isSingleUse is the guarantee the store's guarded write
// buys, asserted through the door that depends on it.
func TestRedeemMagicLink_isSingleUse(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	token := redeemToken(t, e)

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})
	must.NoError(t, err)

	_, err = e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})

	test.ErrorIs(t, err, signin.ErrInvalidMagicLink)

	// And it reads on both transports exactly as a wrong password does, which is
	// what the wrapping is for.
	test.ErrorIs(t, err, signin.ErrInvalidCredentials)
}

// TestRedeemMagicLink_promotesAnUnverifiedRegistrant is the ruling this door was
// built to carry: one mail, not two.
//
// Following the link proves the address and promotes the registrant in the
// transaction that signs them in, so somebody who registered without a password
// gets in on their first click rather than answering a verification link and
// then asking for a second mail.
func TestRedeemMagicLink_promotesAnUnverifiedRegistrant(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	registrant := e.registerUnverified(t, "newcomer")

	must.EqOp(t, identity.StatusUnverified, registrant.AccountStatus)
	must.Nil(t, registrant.EmailAddressVerifiedAt)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, registrant.EmailAddress))

	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})

	must.NoError(t, err)
	test.EqOp(t, registrant.ID, signedIn.Principal.User.ID)

	// The row says both things afterwards: proven, and in good standing.
	after, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registrant.ID)
	must.NoError(t, err)

	test.EqOp(t, identity.StatusGood, after.AccountStatus)
	test.NotNil(t, after.EmailAddressVerifiedAt)

	// And the verification link minted at registration is burned with it. The
	// proof and the outstanding token are one state, so a row holding both would
	// be a row whose verification status depends on which column a reader looked
	// at.
	test.EqOp(t, "", after.EmailAddressVerificationTokenDigest)
}

// TestRedeemMagicLink_recordsTheVerification pins what a consumer's hook is told
// about a promotion this door performed.
//
// EmailAddressProven is true, because a link answered out of the registrant's
// own inbox proves exactly what the verification link proves — which is the
// whole premise this door rests on.
func TestRedeemMagicLink_recordsTheVerification(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	registrant := e.registerUnverified(t, "newcomer")

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, registrant.EmailAddress))

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})
	must.NoError(t, err)

	must.SliceNotEmpty(t, e.hooks.verifieds)

	verification := e.hooks.verifieds[len(e.hooks.verifieds)-1]

	test.True(t, verification.EmailAddressProven)
	test.True(t, verification.Promoted)
	test.EqOp(t, registrant.ID, verification.User.ID)
}

// TestRedeemMagicLink_promotesNobodyAnOperatorMoved is the limit on the ruling
// above, and the sentence that keeps an email from overturning a decision.
//
// A banned or terminated user who follows an outstanding link is refused and
// left exactly where they were put.
func TestRedeemMagicLink_promotesNobodyAnOperatorMoved(T *testing.T) {
	T.Parallel()

	for name, status := range map[string]identity.AccountStatus{
		"banned":     identity.StatusBanned,
		"terminated": identity.StatusTerminated,
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			e := newMagicLinkEnv(t)

			// The link is minted while they are in good standing, which is the
			// only way to have an outstanding one: the request door mails
			// nothing to somebody an operator has moved.
			must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

			token := redeemToken(t, e)
			e.setStatus(t, status, "")

			_, err := e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})

			test.Error(t, err)

			after, readErr := e.store.GetUser(t.Context(), e.client.Reader(), testScope, e.user.ID)
			must.NoError(t, readErr)
			test.EqOp(t, status, after.AccountStatus)
		})
	}
}

// TestRedeemMagicLink_demandsASecondFactor is the refusal that keeps this door
// from being a way around somebody's second factor.
//
// A user who holds a proven TOTP secret must send a code. Without this, whoever
// controls the inbox has a route in that skips the thing the second factor was
// enrolled against.
func TestRedeemMagicLink_demandsASecondFactor(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	secret := e.enrollTOTP(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	token := redeemToken(t, e)

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})
	test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

	// A wrong code is the collapsed refusal rather than the specific one, which
	// is the password door's reading: told apart, they say the first factor was
	// right.
	_, err = e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: token, TOTPCode: "000000"})
	test.ErrorIs(t, err, signin.ErrInvalidCredentials)

	// And the right one gets in.
	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: token, TOTPCode: code(t, secret)})

	must.NoError(t, err)
	test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
}

// TestRedeemMagicLink_survivesAWrongSecondFactor is the consequence the door's
// documentation states, asserted so that it is a decision rather than an
// accident.
//
// A failed second factor rolls the spend back, so the link still works and the
// person can try the code again. Burning it instead would mean a mistyped digit
// costs a fresh mail — and would hand anybody who intercepted a link a way to
// spend every link the person is sent.
func TestRedeemMagicLink_survivesAWrongSecondFactor(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	secret := e.enrollTOTP(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	token := redeemToken(t, e)

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: token, TOTPCode: "000000"})
	must.Error(t, err)

	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: token, TOTPCode: code(t, secret)})

	must.NoError(t, err)
	test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
}

// TestRedeemMagicLink_collapsesEveryRefusal is the posture the whole package
// takes, held to at this door.
func TestRedeemMagicLink_collapsesEveryRefusal(T *testing.T) {
	T.Parallel()

	T.Run("a token nobody minted", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
			&signin.MagicLinkCredentials{Token: "nothing-was-ever-minted-for-this"})

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})

	T.Run("a link presented in another directory", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

		_, err := e.svc.RedeemMagicLink(t.Context(), tenancy.Of("tenant_b"),
			&signin.MagicLinkCredentials{Token: redeemToken(t, e)})

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})
}

// TestRedeemMagicLink_refusals pins the arguments and the two wiring failures.
//
// An empty token is not the collapsed refusal, for the reason an empty handle is
// not: a client that did not submit is not a guess that missed, and answering it
// with a refusal would put a database round trip behind every empty request.
func TestRedeemMagicLink_refusals(T *testing.T) {
	T.Parallel()

	T.Run("nil credentials", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		_, err := e.svc.RedeemMagicLink(t.Context(), testScope, nil)

		test.ErrorIs(t, err, signin.ErrNilCredentials)
	})

	T.Run("an empty token", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		_, err := e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{})

		test.ErrorIs(t, err, signin.ErrEmptyMagicLinkToken)
		test.False(t, platformerrors.Is(err, signin.ErrInvalidMagicLink))
	})

	T.Run("a service with no link store", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
			&signin.MagicLinkCredentials{Token: "anything"})

		test.ErrorIs(t, err, signin.ErrMagicLinksNotConfigured)
	})

	// The redemption needs a verifications directory as well, because promoting
	// a registrant is half of what it does.
	T.Run("a service with no verifications directory", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
			signin.WithMagicLinkStore(e.magicLinks))
		must.NoError(t, err)

		_, err = svc.RedeemMagicLink(t.Context(), testScope,
			&signin.MagicLinkCredentials{Token: "anything"})

		test.ErrorIs(t, err, signin.ErrVerificationsNotConfigured)
	})
}

// TestRedeemMagicLink_recordsARefusal pins that a dead link reaches the hook a
// consumer counts failures with.
//
// The handle is empty and says so on the field: a bearer following a URL typed
// no handle, so a consumer bucketing failures by handle would otherwise be
// silently lumping every dead link together. What names the person is the
// subject, which is known whenever the token resolved to a row.
func TestRedeemMagicLink_recordsARefusal(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	token := redeemToken(t, e)

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})
	must.NoError(t, err)

	before := len(e.hooks.failures)

	_, err = e.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{Token: token})
	must.Error(t, err)

	must.EqOp(t, before+1, len(e.hooks.failures))

	attempt := e.hooks.failures[len(e.hooks.failures)-1]

	test.EqOp(t, "", attempt.Handle)
	test.False(t, attempt.Administrative)
	test.ErrorIs(t, attempt.Reason, signin.ErrInvalidMagicLink)
}

// TestRedeemMagicLink_takesTheAccountTheCallerNamed pins the one field on the
// credentials that is not the token.
//
// Empty takes their default, which is what a link followed out of an inbox
// almost always wants; a named account the subject is a live member of is
// honored, and one they are not is the directory's refusal rather than this
// package's.
func TestRedeemMagicLink_takesTheAccountTheCallerNamed(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)
	second := e.addAccount(t, "Jane's Other")

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e), ActiveAccountID: second})

	must.NoError(t, err)
	test.EqOp(t, second, signedIn.Principal.ActiveAccountID)
}

// TestMagicLinkOptions_nilIsIgnored pins the convention every option in this
// package follows: a nil is ignored rather than installed, so a caller who
// resolved one conditionally does not end up with a service that panics on the
// path they thought they had disabled.
func TestMagicLinkOptions_nilIsIgnored(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
		signin.WithMagicLinkStore(nil),
		signin.WithMagicLinkMailer(nil),
		signin.WithMagicLinkTTL(0),
		signin.WithMagicLinkRequestFloor(-time.Second),
	)
	must.NoError(t, err)

	// Nothing was installed, so the door is not configured rather than half
	// configured.
	test.ErrorIs(t,
		svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress),
		signin.ErrMagicLinksNotConfigured)
}

// TestMagicLinkTTL_reachesTheStore pins that the lifetime is the service's
// policy rather than a store default, which is the split WithMagicLinkTTL is
// written under.
func TestMagicLinkTTL_reachesTheStore(t *testing.T) {
	t.Parallel()

	const ttl = 90 * time.Second

	e := newMagicLinkEnv(t, signin.WithMagicLinkTTL(ttl))

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))

	link := e.mailer.last(t).Issuance.Link

	test.EqOp(t, ttl, link.ExpiresAt.Sub(link.IssuedAt))
}

// TestRedeemMagicLink_refusesALinkTheAddressMovedAwayFrom is the whole of what
// binds a redemption's proof to a fact.
//
// A link demonstrates control of the inbox it was mailed to. If the subject has
// moved to another address in the minutes it is live, that demonstration says
// nothing about where they are now — so the link is refused rather than spent,
// and the new address is left unproven for the mail that will actually reach it.
// The refusal is the one every other way of failing to spend a link gets.
func TestRedeemMagicLink_refusesALinkTheAddressMovedAwayFrom(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))
	token := redeemToken(t, e)

	moved := e.changeEmailAddress(t, e.user, "jane.elsewhere@example.com")
	must.EqOp(t, "jane.elsewhere@example.com", moved.EmailAddress)

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: token})

	test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	test.ErrorIs(t, err, signin.ErrInvalidCredentials)

	// And the address the mail never reached is still unproven, which is the
	// direction this check exists for: the other one marks an inbox nobody has
	// opened as reachable.
	after, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, e.user.ID)
	must.NoError(t, err)

	test.Nil(t, after.EmailAddressVerifiedAt)
}

// TestRedeemMagicLink_admitsALinkForTheSameAddressSpelledOtherwise is the other
// half of that check, and the reason the comparison is made on the folded form.
//
// A request naming the address in another case is the same address, so the link
// it mints redeems.
func TestRedeemMagicLink_admitsALinkForTheSameAddressSpelledOtherwise(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope,
		strings.ToUpper(e.user.EmailAddress)))

	signedIn, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})

	must.NoError(t, err)
	test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
}

// countingVerifications is a Verifications that records how often the proof
// write was reached.
//
// The assertion it serves cannot be made on the column: this module's stores
// read a real clock, SQLite renders a stamp to whole seconds, and two writes a
// millisecond apart would leave a value that had moved and did not look like it.
// Counting the call says what the column cannot.
type countingVerifications struct {
	signin.Verifications

	proven atomic.Int64
}

func (v *countingVerifications) MarkUserEmailAddressProven(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) error {
	v.proven.Add(1)

	return v.Verifications.MarkUserEmailAddressProven(ctx, tx, scope, userID)
}

// TestRedeemMagicLink_leavesAProvenAddressAlone pins what the proof column
// means.
//
// It records when the address was proven, not when somebody last followed a
// link. So the write is made where there is something to prove and skipped where
// there is not: the registrant's first redemption stamps the address, and the
// second one — by which time they are verified and in good standing — writes
// nothing, leaving an operator reading that column the answer to the question
// its name asks.
func TestRedeemMagicLink_leavesAProvenAddressAlone(t *testing.T) {
	t.Parallel()

	counting := &countingVerifications{}

	e := newMagicLinkEnv(t, signin.WithVerifications(counting))
	counting.Verifications = e.store

	registrant := e.registerUnverified(t, "newcomer")

	// The first redemption is the one the door exists for, and it proves.
	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, registrant.EmailAddress))

	_, err := e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})
	must.NoError(t, err)

	must.EqOp(t, int64(1), counting.proven.Load())

	proven, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registrant.ID)
	must.NoError(t, err)
	must.NotNil(t, proven.EmailAddressVerifiedAt)

	// The second finds the address already proven and writes nothing.
	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, registrant.EmailAddress))

	_, err = e.svc.RedeemMagicLink(t.Context(), testScope,
		&signin.MagicLinkCredentials{Token: redeemToken(t, e)})
	must.NoError(t, err)

	test.EqOp(t, int64(1), counting.proven.Load())

	after, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registrant.ID)
	must.NoError(t, err)

	must.NotNil(t, after.EmailAddressVerifiedAt)
	test.True(t, proven.EmailAddressVerifiedAt.Equal(*after.EmailAddressVerifiedAt),
		test.Sprintf("proof moved from %v to %v", proven.EmailAddressVerifiedAt, after.EmailAddressVerifiedAt))
}

// TestRevokeMagicLinksForSubject_withdrawsWhatIsOutstanding is the door an
// operator disabling an account runs and an erasure calls.
//
// Both outstanding links stop working, and the count says how many there were.
func TestRevokeMagicLinksForSubject_withdrawsWhatIsOutstanding(t *testing.T) {
	t.Parallel()

	e := newMagicLinkEnv(t)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))
	first := redeemToken(t, e)

	must.NoError(t, e.svc.RequestMagicLink(t.Context(), testScope, e.user.EmailAddress))
	second := redeemToken(t, e)

	revoked, err := e.svc.RevokeMagicLinksForSubject(t.Context(), testScope, e.user.ID)
	must.NoError(t, err)
	test.EqOp(t, int64(2), revoked)

	for _, token := range []string{first, second} {
		_, err = e.svc.RedeemMagicLink(t.Context(), testScope,
			&signin.MagicLinkCredentials{Token: token})

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	}
}

// TestRevokeMagicLinksForSubject_refusals covers the three answers that are not
// a count: a service with no store, a scope that will not validate, and a
// revocation naming nobody.
//
// A subject who never asked for a link is deliberately not among them — it is
// zero and no error, because there is nothing wrong with a person holding none.
func TestRevokeMagicLinksForSubject_refusals(T *testing.T) {
	T.Parallel()

	T.Run("no store configured", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RevokeMagicLinksForSubject(t.Context(), testScope, e.user.ID)

		test.ErrorIs(t, err, signin.ErrMagicLinksNotConfigured)
	})

	T.Run("no user", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		_, err := e.svc.RevokeMagicLinksForSubject(t.Context(), testScope, "")

		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})

	T.Run("a subject holding none", func(t *testing.T) {
		t.Parallel()

		e := newMagicLinkEnv(t)

		revoked, err := e.svc.RevokeMagicLinksForSubject(t.Context(), testScope, e.user.ID)

		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})
}
