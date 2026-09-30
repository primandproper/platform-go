package assembled_test

import (
	"context"
	"fmt"
	"slices"

	"github.com/primandproper/platform-go/v14/authentication/signin"
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
