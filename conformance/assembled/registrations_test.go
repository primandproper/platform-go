package assembled_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/cockroachdb/errors"
)

// requireAgreements is this harness's signin.RegistrationPolicy: a consumer who
// insists on the terms of service and the privacy policy being accepted, which
// is the policy the seam was added for. Installing it is what proves the
// sign-in suite registers somebody such a deployment admits, rather than
// somebody only a deployment requiring nothing does.
func requireAgreements(_ context.Context, registration *signin.Registration) error {
	for _, required := range []identity.Agreement{identity.TermsOfService, identity.PrivacyPolicy} {
		if !slices.Contains(registration.Agreements, required) {
			return fmt.Errorf("the %s must be accepted to register", required)
		}
	}

	return nil
}

var _ signin.RegistrationPolicy = requireAgreements

// switchedRegistrations is requireAgreements, and while a switch reports true
// also another consumer's policy. With admitting on, it is the policy of a
// consumer whose product gates nothing on a proven address: a registrant is
// written in good standing, and issued a second-factor secret with the
// registration — the two policies Seams.RegistrantsAdmittedUnverified and
// Seams.RegistrationIssuesSecondFactor declare. With passwordRequired on, it is
// the policy of a consumer with no passwordless arrival, which refuses a
// registrant who names no password — what
// Seams.PasswordlessRegistrationRefused declares. The run that declares each
// is the one that turns it on.
//
// Switches on one policy rather than a second server, because the runs against
// a server are sequential and the policy is the only thing the declaring runs
// change about it.
func switchedRegistrations(admitting, passwordRequired *atomic.Bool) signin.RegistrationPolicy {
	return func(ctx context.Context, registration *signin.Registration) error {
		if err := requireAgreements(ctx, registration); err != nil {
			return err
		}

		if passwordRequired.Load() && registration.Credential == signin.NoPassword() {
			return errors.New("this service has no passwordless registration")
		}

		if admitting.Load() {
			registration.User.AccountStatus = identity.StatusGood
			registration.EnrollTOTP = true
		}

		return nil
	}
}

// provenSecondFactor wraps a subject factory so that every caller it mints
// holds a proven second factor nobody but the deployment knows the secret of,
// which is the ordinary state of somebody in a deployment whose policy issues
// one at registration and who finished enrolling.
func provenSecondFactor(
	identitySvc *identity.Service,
	newSubject func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error),
) func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
	return func(ctx context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
		sub, err := newSubject(ctx, opts...)
		if err != nil {
			return nil, err
		}

		raw := make([]byte, 20)
		if _, err = rand.Read(raw); err != nil {
			return nil, err
		}

		secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

		if _, err = identitySvc.UpdateUserTwoFactorSecret(ctx, sub.Scope, sub.UserID, secret); err != nil {
			return nil, err
		}

		if _, err = identitySvc.MarkUserTwoFactorSecretVerified(ctx, sub.Scope, sub.UserID); err != nil {
			return nil, err
		}

		return sub, nil
	}
}
