package oauth2server_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// revocationCall is one thing a RevocationObserver was told.
type revocationCall struct {
	subject  oauth2server.Subject
	familyID string
}

// revocationRecorder collects what the observer was called with.
//
// Under a mutex, because the observer runs on the httptest server's goroutine
// and the assertions run on the test's: the response arriving is what orders
// them, and that is not an ordering the race detector can see.
type revocationRecorder struct {
	calls []revocationCall

	mu sync.Mutex
}

// observe is the RevocationObserver itself.
func (r *revocationRecorder) observe(_ context.Context, subject oauth2server.Subject, familyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, revocationCall{subject: subject, familyID: familyID})
}

// recorded hands back a copy of what was seen.
func (r *revocationRecorder) recorded() []revocationCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

// The endpoint answers the same empty 200 whatever happened, so the observer is
// the only thing that can tell a deployment a session actually ended. Every
// case here is about which of those 200s it fires for.
func TestServer_RevocationObserver(T *testing.T) {
	T.Parallel()

	T.Run("reports the subject and family a sign-out ended", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t, oauth2server.WithRevocationObserver(observer.observe))
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		must.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.RefreshToken, "refresh_token"))

		calls := observer.recorded()
		must.SliceLen(t, 1, calls)

		// The identifier and the claims both, which is the whole reason the
		// callback carries a Subject rather than a subject ID: a consumer's
		// sign-out event is usually keyed on something in the claims.
		test.EqOp(t, testSubject().ID, calls[0].subject.ID)
		test.Eq(t, testSubject().Claims, calls[0].subject.Claims)
		test.NotEq(t, "", calls[0].familyID)
	})

	T.Run("names the family an access token belonged to", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t, oauth2server.WithRevocationObserver(observer.observe))
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		must.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, tokens.AccessToken, ""))

		calls := observer.recorded()
		must.SliceLen(t, 1, calls)
		test.EqOp(t, testSubject().ID, calls[0].subject.ID)

		// The family is still live — an access token revocation is not a
		// sign-out — so the identifier is what lets a consumer tell the two
		// apart rather than an invitation to treat them the same.
		test.NotEq(t, "", calls[0].familyID)

		refreshed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		test.EqOp(t, http.StatusOK, refreshed.status)
	})

	T.Run("says nothing about a token nobody issued", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t, oauth2server.WithRevocationObserver(observer.observe))
		reg := h.registerConfidential()

		// The 200 RFC 7009 §2.2 requires, and the silence that is the whole
		// point: an observer that fired here would have a consumer publishing a
		// sign-out for a token that never existed.
		must.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, "not-a-token", ""))

		test.SliceEmpty(t, observer.recorded())
	})

	T.Run("says nothing about another client's token", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t, oauth2server.WithRevocationObserver(observer.observe))
		victim := h.registerConfidential()
		attacker := h.registerConfidential()

		tokens := h.exchange(victim)

		must.EqOp(t, http.StatusOK, h.revoke(attacker.ClientID, attacker.ClientSecret, tokens.AccessToken, ""))

		test.SliceEmpty(t, observer.recorded())
	})

	T.Run("says nothing when the store refused the write", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}
		store := newFaultStore()

		h := newStoreHarness(t, store, oauth2server.WithRevocationObserver(observer.observe))
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		store.breaks(methodRevokeFamily, errStoreDown)

		// Answered 200 either way, because the RFC gives it no other answer.
		// A deployment that emitted "this user signed out" here would be
		// reporting a session that is still going.
		must.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.RefreshToken, "refresh_token"))

		test.SliceEmpty(t, observer.recorded())

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		test.NotNil(t, access)
	})

	T.Run("says nothing about a family reuse detection killed", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t, oauth2server.WithRevocationObserver(observer.observe))
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		refresh := url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		}

		must.EqOp(t, http.StatusOK, h.token(reg.ClientID, reg.ClientSecret, refresh).status)

		// The replay, which revokes the whole family. It is not a sign-out, and
		// a consumer told about it through this callback would log a theft as
		// one.
		replayed := h.token(reg.ClientID, reg.ClientSecret, refresh)
		must.EqOp(t, http.StatusBadRequest, replayed.status)

		test.SliceEmpty(t, observer.recorded())
	})

	T.Run("a panicking observer does not undo the revocation", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithRevocationObserver(
			func(context.Context, oauth2server.Subject, string) { panic("the analytics pipeline is down") }))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		// The records are gone before the callback runs, so a consumer's
		// failure must not turn a sign-out that worked into a dropped
		// connection the client retries.
		test.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.RefreshToken, "refresh_token"))

		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("a nil observer leaves whatever was already set in place", func(t *testing.T) {
		t.Parallel()

		observer := &revocationRecorder{}

		h := newHarness(t,
			oauth2server.WithRevocationObserver(observer.observe),
			oauth2server.WithRevocationObserver(nil))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		must.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.RefreshToken, "refresh_token"))

		test.SliceLen(t, 1, observer.recorded())
	})
}
