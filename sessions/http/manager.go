package http

import (
	"context"
	stderrors "errors"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/cookies"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/sessions"
)

// serviceName names the loggers and spans this package emits. The counters live
// on the Store, which sees every operation whether or not it came over HTTP.
const serviceName = "sessions_http"

// DefaultCookieName is the cookie a session identifier travels in when none is
// configured. The __Host- prefix is deliberately absent: browsers reject that
// prefix on a cookie with a Domain attribute, and cookies.Config has one.
const DefaultCookieName = "session"

// Manager binds a sessions.Store to a signed cookie and to net/http.
//
// It is the layer that knows a session identifier is a bearer credential: the
// identifier goes out through cookies.Manager, so it inherits that package's
// signing, encryption, HttpOnly, Secure, and SameSite handling, and it never
// appears in a URL, a header, or a body.
type Manager[T any] struct {
	store   sessions.Store[T]
	cookies cookies.Manager
	o11y    observability.Observer

	cookieName string
}

// NewManager builds a Manager over a store and a cookie manager.
//
// Both are required. In particular there is no default cookie manager: an
// unsigned cookie would let a client present any identifier it liked, and while
// a 256-bit identifier is not guessable, "the store is the only thing standing
// between an attacker and a valid session" is not a property to acquire by
// omission.
func NewManager[T any](store sessions.Store[T], cookieManager cookies.Manager, opts ...Option) (*Manager[T], error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if cookieManager == nil {
		return nil, ErrNilCookieManager
	}

	o := newOptions(opts)

	return &Manager[T]{
		store:      store,
		cookies:    cookieManager,
		cookieName: o.cookieName,
		o11y:       observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}, nil
}

// Issue establishes a session and writes its cookie to res.
//
// Call it after authenticating, and only then. Issuing a session before the
// user has proved anything is what gives a fixation attack something to plant —
// if there is state to carry across the sign-in, Renew is the way to carry it.
func (m *Manager[T]) Issue(
	ctx context.Context,
	res http.ResponseWriter,
	data *T,
) (*sessions.Session[T], error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	session, err := m.store.New(ctx, data)
	if err != nil {
		return nil, op.Error(err, "establishing session")
	}

	if err = m.write(ctx, res, session); err != nil {
		// The session exists and the client will never learn its identifier, so
		// it is removed rather than left to idle out. Leaving it would be a
		// live session nobody can reach and nobody can revoke.
		m.discard(ctx, op, session.ID)

		return nil, op.Error(err, "writing session cookie")
	}

	return session, nil
}

// Load reads the session named by req's cookie.
//
// A request with no session cookie, or one carrying a value that does not
// verify, reports sessions.ErrNotFound — the same answer as an identifier that
// names nothing, deliberately. A client cannot learn from the response whether
// it forged the cookie badly or merely presented an expired one.
func (m *Manager[T]) Load(ctx context.Context, req *http.Request) (*sessions.Session[T], error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	id, err := m.identifier(ctx, req)
	if err != nil {
		return nil, err
	}

	session, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading session")
	}

	return session, nil
}

// Save replaces the payload of the session named by req's cookie. The cookie
// itself does not change, so nothing needs to be written to the response.
func (m *Manager[T]) Save(ctx context.Context, req *http.Request, data *T) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	id, err := m.identifier(ctx, req)
	if err != nil {
		return err
	}

	if err = m.store.Save(ctx, id, data); err != nil {
		return platformerrors.Wrap(err, "saving session")
	}

	return nil
}

// Renew rotates the identifier of the session named by req's cookie and writes
// the new one to res.
//
// This is the call that belongs immediately after a sign-in, a step-up
// authentication, or any other privilege change — see sessions.Store.Renew for
// what it defends against. It returns the renewed session so the caller can go
// on using it within the same request.
//
// An error here means the old identifier may still resolve, and the privilege
// change should be refused rather than completed.
func (m *Manager[T]) Renew(
	ctx context.Context,
	res http.ResponseWriter,
	req *http.Request,
) (*sessions.Session[T], error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	oldID, err := m.identifier(ctx, req)
	if err != nil {
		return nil, err
	}

	newID, err := m.store.Renew(ctx, oldID)
	if err != nil {
		return nil, platformerrors.Wrap(err, "renewing session")
	}

	session, err := m.store.Get(ctx, newID)
	if err != nil {
		return nil, op.Error(err, "reading renewed session")
	}

	if err = m.write(ctx, res, session); err != nil {
		// Not discarded, unlike Issue's failure. The old identifier is already
		// gone, so removing this one too would sign the user out of a session
		// they are entitled to; letting it idle out costs one unreachable
		// record. The caller still gets an error and still refuses the
		// privilege change.
		return nil, op.Error(err, "writing renewed session cookie")
	}

	return session, nil
}

// End deletes the session named by req's cookie and clears the cookie on res.
//
// A request with no session is not an error: sign-out is idempotent, and a
// client that has already signed out has nothing to be told. The cookie is
// cleared either way, so a cookie naming a session that no longer exists does
// not survive the request.
func (m *Manager[T]) End(ctx context.Context, res http.ResponseWriter, req *http.Request) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	// Cleared first and unconditionally. Whatever happens to the record, the
	// client must not leave holding a usable cookie.
	if err := m.clear(ctx, res); err != nil {
		return op.Error(err, "clearing session cookie")
	}

	id, err := m.identifier(ctx, req)
	if err != nil {
		if stderrors.Is(err, sessions.ErrNotFound) {
			return nil
		}

		return err
	}

	if err = m.store.Delete(ctx, id); err != nil {
		return op.Error(err, "ending session")
	}

	return nil
}

// identifier reads and verifies the session identifier carried by req.
func (m *Manager[T]) identifier(ctx context.Context, req *http.Request) (string, error) {
	if req == nil {
		return "", platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil request")
	}

	cookie, err := req.Cookie(m.cookieName)
	if err != nil {
		return "", platformerrors.Wrap(sessions.ErrNotFound, "request carries no session cookie")
	}

	var id string
	if err = m.cookies.Decode(ctx, m.cookieName, cookie.Value, &id); err != nil {
		// Reported as an absent session rather than as a decoding failure. A
		// cookie that does not verify is a forgery, an old signing key, or a
		// truncated value, and none of those are worth telling the client apart
		// — nor worth logging at error level once per request when a key
		// rotation makes every live cookie fail at once.
		return "", platformerrors.Wrap(sessions.ErrNotFound, "session cookie did not verify")
	}

	if id == "" {
		return "", platformerrors.Wrap(sessions.ErrNotFound, "session cookie carries no identifier")
	}

	return id, nil
}

// write puts a session's identifier into a cookie on res.
func (m *Manager[T]) write(ctx context.Context, res http.ResponseWriter, session *sessions.Session[T]) error {
	if res == nil {
		return platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil response writer")
	}

	cookie, err := m.cookies.BuildCookie(ctx, m.cookieName, session.ID)
	if err != nil {
		return err
	}

	m.applyLifetime(cookie, session)
	http.SetCookie(res, cookie)

	return nil
}

// applyLifetime sets how long the browser should keep the cookie.
//
// It is derived from the store's absolute timeout, not from the session's
// ExpiresAt. ExpiresAt moves with the idle deadline, so a cookie cut to it
// would expire in the browser after one idle window even for a user who never
// stopped clicking — the server would still have the session and the client
// would have thrown away the only way to name it.
//
// With no absolute timeout there is nothing to derive, and the cookie manager's
// own configured lifetime stands. Note that a cookie manager whose lifetime is
// shorter than the absolute timeout caps the session regardless: securecookie
// refuses to decode a value older than that, and no amount of MaxAge here
// changes it.
func (m *Manager[T]) applyLifetime(cookie *http.Cookie, session *sessions.Session[T]) {
	absolute := m.store.Policy().Absolute
	if absolute <= 0 {
		return
	}

	deadline := session.CreatedAt.Add(absolute)

	// Measured against the session's own LastSeenAt rather than time.Now. A
	// cookie is only written for a session that was just created or just
	// renewed, so LastSeenAt is "now" as read from the store's clock — which is
	// the clock the deadline was computed on, and the one a test controls.
	remaining := deadline.Sub(session.LastSeenAt)
	if remaining <= 0 {
		return
	}

	cookie.Expires = deadline
	cookie.MaxAge = int(remaining.Seconds())
}

// clear writes a cookie that removes the session cookie from the client.
//
// It is built through the cookie manager rather than by hand so that Domain,
// Path, Secure, and SameSite match the cookie being replaced exactly. A browser
// treats a deletion cookie whose attributes differ as a different cookie, and
// silently keeps the one it was meant to remove.
func (m *Manager[T]) clear(ctx context.Context, res http.ResponseWriter) error {
	if res == nil {
		return platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil response writer")
	}

	cookie, err := m.cookies.BuildCookie(ctx, m.cookieName, "")
	if err != nil {
		return err
	}

	cookie.Value = ""
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(0, 0)

	http.SetCookie(res, cookie)

	return nil
}

// discard removes a session the client will never be able to name.
func (m *Manager[T]) discard(ctx context.Context, op observability.Operation, id string) {
	if err := m.store.Delete(ctx, id); err != nil {
		op.Acknowledge(err, "discarding session whose cookie could not be written")
	}
}
