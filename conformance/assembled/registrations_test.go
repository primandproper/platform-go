package assembled_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity"
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

// admittingRegistrations is requireAgreements, and while admitting reports true
// also the policy of a consumer whose product gates nothing on a proven
// address: a registrant is written in good standing, and issued a
// second-factor secret with the registration. Those are the two policies
// Seams.RegistrantsAdmittedUnverified and Seams.RegistrationIssuesSecondFactor
// declare, and the run that declares them is the one that turns this on.
//
// A switch on one policy rather than a second server, because the runs against
// a server are sequential and the policy is the only thing the declaring run
// changes about it.
func admittingRegistrations(admitting *atomic.Bool) signin.RegistrationPolicy {
	return func(ctx context.Context, registration *signin.Registration) error {
		if err := requireAgreements(ctx, registration); err != nil {
			return err
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
