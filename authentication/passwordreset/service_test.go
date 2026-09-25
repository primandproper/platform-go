package passwordreset

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewService(T *testing.T) {
	T.Parallel()

	newDeps := func(tb testing.TB) (database.Client, Store, Directory, *fakeAuthenticator, Mailer) {
		tb.Helper()

		client := newTestClient(tb)

		store, err := NewSQLStore(&Config{}, client)
		must.NoError(tb, err)

		return client, store, &testDirectory{}, &fakeAuthenticator{}, &recordingMailer{}
	}

	T.Run("refuses each missing dependency", func(t *testing.T) {
		t.Parallel()

		client, store, directory, auth, mailer := newDeps(t)

		for _, tc := range []struct {
			build func() (*Service, error)
			want  error
			name  string
		}{
			{
				name:  "nil client",
				build: func() (*Service, error) { return NewService(nil, store, directory, auth, mailer) },
				want:  ErrNilDatabaseClient,
			},
			{
				name:  "nil store",
				build: func() (*Service, error) { return NewService(client, nil, directory, auth, mailer) },
				want:  ErrNilStore,
			},
			{
				name:  "nil directory",
				build: func() (*Service, error) { return NewService(client, store, nil, auth, mailer) },
				want:  ErrNilDirectory,
			},
			{
				name:  "nil authenticator",
				build: func() (*Service, error) { return NewService(client, store, directory, nil, mailer) },
				want:  ErrNilAuthenticator,
			},
			{
				name:  "nil mailer",
				build: func() (*Service, error) { return NewService(client, store, directory, auth, nil) },
				want:  ErrNilMailer,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				service, err := tc.build()
				test.Nil(t, service)
				test.ErrorIs(t, err, tc.want)
			})
		}
	})

	T.Run("refuses a lifetime that issues dead links", func(t *testing.T) {
		t.Parallel()

		client, store, directory, auth, mailer := newDeps(t)

		service, err := NewService(client, store, directory, auth, mailer, WithTokenLifetime(0))
		test.Nil(t, service)
		test.ErrorIs(t, err, ErrNonPositiveLifetime)
	})

	T.Run("builds with defaults", func(t *testing.T) {
		t.Parallel()

		client, store, directory, auth, mailer := newDeps(t)

		service, err := NewService(client, store, directory, auth, mailer)
		must.NoError(t, err)
		must.NotNil(t, service)

		test.EqOp(t, DefaultTokenLifetime, service.lifetime)
		test.EqOp(t, DefaultRequestFloor, service.requestFloor)
	})
}

func TestServiceRequest(T *testing.T) {
	T.Parallel()

	T.Run("mails a link a known address can spend", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		must.NoError(t, env.service.Request(t.Context(), testScope(), testEmailAddress))
		must.EqOp(t, 1, env.mailer.count())

		mail := env.mailer.last()
		must.NotNil(t, mail)
		must.NotNil(t, mail.Issuance)
		test.NotEq(t, "", mail.Issuance.Secret)
		test.EqOp(t, testUserID, mail.Issuance.Token.UserID)
		test.EqOp(t, testScope(), mail.Issuance.Token.Scope)

		// The user handed over is redacted, which is the whole of what a mailer
		// is entitled to know about them.
		must.NotNil(t, mail.User)
		test.EqOp(t, testEmailAddress, mail.User.EmailAddress)
		test.EqOp(t, "", mail.User.HashedPassword)

		// The lifetime the service was built with is what the row carries.
		test.EqOp(t, env.storeClock.Now().UTC().Add(DefaultTokenLifetime), mail.Issuance.Token.ExpiresAt.UTC())

		token, err := env.service.Verify(t.Context(), testScope(), mail.Issuance.Secret)
		must.NoError(t, err)
		test.EqOp(t, mail.Issuance.Token.ID, token.ID)
	})

	T.Run("honors a configured lifetime", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t, WithTokenLifetime(15*time.Minute))

		must.NoError(t, env.service.Request(t.Context(), testScope(), testEmailAddress))

		mail := env.mailer.last()
		must.NotNil(t, mail)
		test.EqOp(t, env.storeClock.Now().UTC().Add(15*time.Minute), mail.Issuance.Token.ExpiresAt.UTC())
	})

	T.Run("answers success and mails nothing for an address nobody holds", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		// The same answer a known address gets, which is the account
		// enumeration position: a handler that could tell the two apart is a
		// handler that answers "does this person have an account".
		must.NoError(t, env.service.Request(t.Context(), testScope(), "nobody@example.com"))
		test.EqOp(t, 0, env.mailer.count())

		tokens, err := env.store.ListForUser(t.Context(), env.client.Writer(), testScope(), testUserID)
		must.NoError(t, err)
		test.SliceEmpty(t, tokens)
	})

	T.Run("holds a known and an unknown address to the same deadline", func(t *testing.T) {
		t.Parallel()

		known := newTestService(t)
		// The known path does real work — a transaction, a commit and a mail
		// send — and this is that work taking time the unknown path does not.
		known.mailer.inspect = func(*Mail) { known.clock.advance(200 * time.Millisecond) }

		unknown := newTestService(t)

		start := known.clock.Now()
		test.EqOp(t, start, unknown.clock.Now())

		must.NoError(t, known.service.Request(t.Context(), testScope(), testEmailAddress))
		must.NoError(t, unknown.service.Request(t.Context(), testScope(), "nobody@example.com"))

		// Both return at the same instant, not after the same sleep: the pad is
		// the remainder of a floor rather than a constant added to whatever the
		// work cost.
		want := start.Add(DefaultRequestFloor)
		test.EqOp(t, want, known.clock.Now())
		test.EqOp(t, want, unknown.clock.Now())

		test.Eq(t, []time.Duration{300 * time.Millisecond}, known.clock.sleeps())
		test.Eq(t, []time.Duration{DefaultRequestFloor}, unknown.clock.sleeps())
	})

	T.Run("stops padding when the caller has gone", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		env.mailer.inspect = func(*Mail) { cancel() }

		start := env.clock.Now()

		// The reset itself succeeded, so the request did too — there is just
		// nobody left to hold the answer back from.
		must.NoError(t, env.service.Request(ctx, testScope(), testEmailAddress))

		test.SliceLen(t, 1, env.clock.sleeps())
		test.EqOp(t, start, env.clock.Now())
		test.EqOp(t, 1, env.liveTokens(t))
	})

	T.Run("pads nothing when the floor is turned off", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t, WithRequestFloor(0))

		must.NoError(t, env.service.Request(t.Context(), testScope(), testEmailAddress))
		test.SliceEmpty(t, env.clock.sleeps())
	})

	T.Run("refuses an empty address without arming the floor", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		err := env.service.Request(t.Context(), testScope(), "")
		test.ErrorIs(t, err, ErrEmptyEmailAddress)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		// Nothing to learn from the timing of a request that named nobody, so
		// nothing is spent holding it.
		test.SliceEmpty(t, env.clock.sleeps())
		test.EqOp(t, 0, env.mailer.count())
	})

	T.Run("sends nothing when the issuing transaction rolls back", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		env.tokens.issueErr = stderrors.New("the database went away")

		err := env.service.Request(t.Context(), testScope(), testEmailAddress)
		test.ErrorContains(t, err, "the database went away")

		// A mail sent from inside the callback would be a link delivered for a
		// reset that then rolled back, and nothing can take it back.
		test.EqOp(t, 0, env.mailer.count())
	})

	T.Run("returns a mailer failure over a token that is already committed", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		env.mailer.err = stderrors.New("the mail provider is down")

		err := env.service.Request(t.Context(), testScope(), testEmailAddress)
		test.ErrorContains(t, err, "the mail provider is down")

		// This is the other half of "the mail goes after the commit", and the
		// half a rollback test cannot reach: a send from inside the callback
		// would make this failure abort the transaction, and the surviving row
		// is what says the commit had already happened. It is also the behavior
		// a caller wants — issuing again does not invalidate what is
		// outstanding, so their retry costs a row rather than a broken flow.
		test.EqOp(t, 1, env.liveTokens(t))
	})

	T.Run("passes the scope it was given to the directory", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		must.NoError(t, env.service.Request(t.Context(), testScope(), testEmailAddress))
		test.Eq(t, []tenancy.Scope{testScope()}, env.directory.scopes)
	})

	T.Run("reports a directory failure that is not an absence", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		env.directory.byAddress = nil
		env.directory.readErr = stderrors.New("the directory is unwell")

		err := env.service.Request(t.Context(), testScope(), testEmailAddress)
		test.ErrorContains(t, err, "the directory is unwell")
		test.EqOp(t, 0, env.mailer.count())
	})
}

func TestServiceVerify(T *testing.T) {
	T.Parallel()

	T.Run("resolves a live token without spending it", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		first, err := env.service.Verify(t.Context(), testScope(), secret)
		must.NoError(t, err)
		test.Nil(t, first.RedeemedAt)

		second, err := env.service.Verify(t.Context(), testScope(), secret)
		must.NoError(t, err)
		test.EqOp(t, first.ID, second.ID)
	})

	T.Run("refuses a secret nobody issued", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		token, err := env.service.Verify(t.Context(), testScope(), "not-a-token")
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("refuses a token past its deadline", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		env.storeClock.advance(DefaultTokenLifetime + time.Second)

		token, err := env.service.Verify(t.Context(), testScope(), secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenExpired)
	})

	T.Run("refuses a token in another scope", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		token, err := env.service.Verify(t.Context(), tenancy.Of("tenant_b"), secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})
}

func TestServiceComplete(T *testing.T) {
	T.Parallel()

	T.Run("spends the link, writes the password and revokes the rest", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		stale := env.request(t)
		secret := env.request(t)
		must.EqOp(t, 2, env.liveTokens(t))

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "correct horse")
		must.NoError(t, err)
		must.NotNil(t, spent)
		test.NotNil(t, spent.RedeemedAt)
		test.EqOp(t, testUserID, spent.UserID)

		test.EqOp(t, "hashed:correct horse", env.storedPassword(t))

		// The link that was just spent answers "already used" for the rest of
		// its life, and the one that was outstanding beside it is gone.
		_, err = env.service.Verify(t.Context(), testScope(), secret)
		test.ErrorIs(t, err, ErrTokenRedeemed)

		_, err = env.service.Verify(t.Context(), testScope(), stale)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("rolls the password change back when the revoke fails", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		env.tokens.revokeErr = stderrors.New("the revoke statement failed")

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "correct horse")
		test.Nil(t, spent)
		test.ErrorContains(t, err, "the revoke statement failed")

		// This is the hole the flow exists to close. The hand-written version
		// acknowledges this failure and reports success, which leaves the
		// password changed and the links that were outstanding still working.
		test.EqOp(t, "hashed:original", env.storedPassword(t))

		// And the link is unspent, so the user can try again rather than
		// holding a redeemed token over a password that never moved.
		token, verifyErr := env.service.Verify(t.Context(), testScope(), secret)
		must.NoError(t, verifyErr)
		test.Nil(t, token.RedeemedAt)
	})

	T.Run("rolls the redemption back when the password write fails", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		env.directory.updateErr = stderrors.New("the directory is unwell")

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "correct horse")
		test.Nil(t, spent)
		test.ErrorContains(t, err, "the directory is unwell")

		test.EqOp(t, "hashed:original", env.storedPassword(t))

		token, verifyErr := env.service.Verify(t.Context(), testScope(), secret)
		must.NoError(t, verifyErr)
		test.Nil(t, token.RedeemedAt)
	})

	T.Run("refuses an empty password before the link is spent", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "")
		test.Nil(t, spent)
		test.ErrorIs(t, err, ErrEmptyNewPassword)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		test.EqOp(t, "hashed:original", env.storedPassword(t))
		test.EqOp(t, 1, env.liveTokens(t))
	})

	T.Run("refuses what the password policy refuses, before the link is spent", func(t *testing.T) {
		t.Parallel()

		errTooShort := platformerrors.New("use at least twelve characters")

		var seen []string

		env := newTestService(t, WithPasswordPolicy(func(_ context.Context, password string) error {
			seen = append(seen, password)
			if len(password) < 12 {
				return errTooShort
			}

			return nil
		}))
		secret := env.request(t)

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "short")
		test.Nil(t, spent)
		test.ErrorIs(t, err, ErrPasswordRefused)
		test.ErrorIs(t, err, errTooShort)

		// The refusal cost the caller nothing: the password did not move and the
		// link is still live, so the same link with another password succeeds.
		test.EqOp(t, "hashed:original", env.storedPassword(t))
		test.EqOp(t, 1, env.liveTokens(t))

		_, err = env.service.Complete(t.Context(), testScope(), secret, "long enough to pass")
		must.NoError(t, err)
		test.EqOp(t, "hashed:long enough to pass", env.storedPassword(t))
		test.Eq(t, []string{"short", "long enough to pass"}, seen)
	})

	T.Run("hashes nothing for a refused password", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t, WithPasswordPolicy(func(context.Context, string) error {
			return platformerrors.New("never")
		}))
		secret := env.request(t)

		// A hashing engine that would fail is never reached, so the refusal is
		// the policy's rather than the engine's.
		env.auth.err = stderrors.New("the hashing engine was reached")

		_, err := env.service.Complete(t.Context(), testScope(), secret, "anything at all")
		test.ErrorIs(t, err, ErrPasswordRefused)
		test.False(t, stderrors.Is(err, env.auth.err))
	})

	T.Run("an empty password is refused before the policy sees it", func(t *testing.T) {
		t.Parallel()

		calls := 0
		env := newTestService(t, WithPasswordPolicy(func(context.Context, string) error {
			calls++
			return nil
		}))
		secret := env.request(t)

		_, err := env.service.Complete(t.Context(), testScope(), secret, "")
		test.ErrorIs(t, err, ErrEmptyNewPassword)
		test.EqOp(t, 0, calls)
	})

	T.Run("a nil policy is ignored", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t, WithPasswordPolicy(nil))
		secret := env.request(t)

		_, err := env.service.Complete(t.Context(), testScope(), secret, "x")
		must.NoError(t, err)
	})

	T.Run("leaves the link alone when hashing fails", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		env.auth.err = stderrors.New("the hashing engine failed")

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "correct horse")
		test.Nil(t, spent)
		test.ErrorContains(t, err, "the hashing engine failed")
		test.EqOp(t, 1, env.liveTokens(t))
	})

	T.Run("refuses a secret nobody issued", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)

		spent, err := env.service.Complete(t.Context(), testScope(), "not-a-token", "correct horse")
		test.Nil(t, spent)
		test.ErrorIs(t, err, ErrTokenNotFound)
		test.EqOp(t, "hashed:original", env.storedPassword(t))
	})

	T.Run("refuses a link that has already been spent", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		_, err := env.service.Complete(t.Context(), testScope(), secret, "correct horse")
		must.NoError(t, err)

		spent, err := env.service.Complete(t.Context(), testScope(), secret, "second try")
		test.Nil(t, spent)
		test.ErrorIs(t, err, ErrTokenRedeemed)

		// The second redemption wrote nothing, which is what the transaction
		// buys: the refusal comes before the password write, not after it.
		test.EqOp(t, "hashed:correct horse", env.storedPassword(t))
	})

	T.Run("refuses a link presented in another scope", func(t *testing.T) {
		t.Parallel()

		env := newTestService(t)
		secret := env.request(t)

		spent, err := env.service.Complete(t.Context(), tenancy.Of("tenant_b"), secret, "correct horse")
		test.Nil(t, spent)
		test.ErrorIs(t, err, ErrTokenNotFound)
		test.EqOp(t, "hashed:original", env.storedPassword(t))
	})
}
