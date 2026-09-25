package signin_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The password policy seam. Every door that writes a password consults it, and
// what each case pins is the half the issue asked for: a refusal costs the
// caller nothing, so the same request with another password goes through.

// errTooShort is the consumer's own refusal, which the service must hand back
// rather than swallow.
var errTooShort = platformerrors.New("use at least twelve characters")

// minimumLength is a consumer's policy, counting how often it was asked.
type minimumLength struct {
	seen  []string
	calls int
}

func (p *minimumLength) policy(_ context.Context, password string) error {
	p.calls++
	p.seen = append(p.seen, password)

	if len(password) < 12 {
		return errTooShort
	}

	return nil
}

func TestPasswordPolicy_Register(T *testing.T) {
	T.Parallel()

	T.Run("a refused password registers nobody", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t, signin.WithPasswordPolicy(p.policy))

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("short")))
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.ErrorIs(t, err, errTooShort)
		test.EqOp(t, 1, p.calls)
		test.Eq(t, []string{"short"}, p.seen)

		// Nothing was written, which is what lets the same registration go
		// through with a password the policy admits: the username is still free.
		registered, err := e.svc.Register(t.Context(), testScope,
			newRegistration("ada", signin.Password("long enough to pass")))
		must.NoError(t, err)
		test.EqOp(t, "ada", registered.User.Username)
	})

	T.Run("a passwordless registration is not asked about", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t, signin.WithPasswordPolicy(p.policy))

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.NoError(t, err)
		test.EqOp(t, 0, p.calls)
	})

	T.Run("an empty password is refused before the policy sees it", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t, signin.WithPasswordPolicy(p.policy))

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("")))
		test.ErrorIs(t, err, signin.ErrEmptyPassword)
		test.EqOp(t, 0, p.calls)
	})

	T.Run("nothing is hashed for a refused password", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t)
		authenticator := &stubAuthenticator{}

		svc, err := signin.NewService(e.client, e.store, authenticator, e.issuer,
			signin.WithRegistrar(e.directory),
			signin.WithPasswordPolicy(p.policy),
		)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("short")))
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.EqOp(t, 0, authenticator.hashes)
	})
}

func TestPasswordPolicy_UpdatePassword(T *testing.T) {
	T.Parallel()

	T.Run("a refused password leaves the old one standing", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t, signin.WithPasswordPolicy(p.policy))

		err := e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "short",
		})
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.ErrorIs(t, err, errTooShort)
		test.EqOp(t, 0, e.hooks.passwords)

		_, err = e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.NoError(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "long enough to pass",
		}))
		test.EqOp(t, 1, e.hooks.passwords)
		test.Eq(t, []string{"short", "long enough to pass"}, p.seen)
	})

	// The policy runs ahead of reauthentication, so a refused password is
	// refused before the current one is spent on a hash comparison.
	T.Run("before the current password is checked", func(t *testing.T) {
		t.Parallel()

		p := &minimumLength{}
		e := newEnv(t)
		authenticator := &stubAuthenticator{result: true}

		svc, err := signin.NewService(e.client, e.store, authenticator, e.issuer,
			signin.WithPasswordPolicy(p.policy),
		)
		must.NoError(t, err)

		err = svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: "not it",
			NewPassword:     "short",
		})
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.EqOp(t, 0, authenticator.matches)
		test.EqOp(t, 0, authenticator.hashes)
	})
}

func TestPasswordPolicy_AttachPassword(T *testing.T) {
	T.Parallel()

	p := &minimumLength{}
	e := newEnv(T, signin.WithPasswordPolicy(p.policy))

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       registered.EmailAddressVerificationToken,
		NewPassword: "short",
	})
	test.ErrorIs(T, err, signin.ErrPasswordRefused)
	test.ErrorIs(T, err, errTooShort)
	test.SliceEmpty(T, e.hooks.attached)

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.False(T, stored.HasPassword())

	// The link is exactly as live as it was.
	must.NoError(T, e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       registered.EmailAddressVerificationToken,
		NewPassword: "long enough to pass",
	}))
	must.SliceLen(T, 1, e.hooks.attached)
}

func TestWithPasswordPolicy_nilIsIgnored(T *testing.T) {
	T.Parallel()

	e := newEnv(T, signin.WithPasswordPolicy(nil))

	must.NoError(T, e.svc.UpdatePassword(T.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
		CurrentPassword: e.password,
		NewPassword:     "x",
	}))
}

// TestPasswordPolicy_onTheWire pins the order the refusal is joined in, which
// is what decides whose words a gRPC client reads.
func TestPasswordPolicy_onTheWire(T *testing.T) {
	T.Parallel()

	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)
	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	refuse := func(t *testing.T, policyErr error) error {
		t.Helper()

		e := newEnv(t, signin.WithPasswordPolicy(func(context.Context, string) error { return policyErr }))

		err := e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "anything",
		})
		must.ErrorIs(t, err, signin.ErrPasswordRefused)

		return err
	}

	T.Run("a policy's own error is passed over unless the consumer registered it", func(t *testing.T) {
		t.Parallel()

		err := refuse(t, platformerrors.New("the consumer's internal detail"))

		msg, ok := grpcerrors.ClientSafeMessage(err)
		must.True(t, ok)
		test.EqOp(t, signin.ErrPasswordRefused.Error(), msg)

		reason, ok := grpcerrors.ClientSafeReason(err)
		must.True(t, ok)
		test.EqOp(t, "PASSWORD_REFUSED", reason.Reason)
	})

	T.Run("a registered one is quoted, and the identifier stays", func(t *testing.T) {
		t.Parallel()

		consumers := platformerrors.New("choose a password of at least twelve characters")
		grpcerrors.RegisterClientSafeSentinels(consumers)

		err := refuse(t, consumers)

		msg, ok := grpcerrors.ClientSafeMessage(err)
		must.True(t, ok)
		test.EqOp(t, consumers.Error(), msg)

		reason, ok := grpcerrors.ClientSafeReason(err)
		must.True(t, ok)
		test.EqOp(t, "PASSWORD_REFUSED", reason.Reason)
	})
}
