package assembled_test

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// resetMailbox is this harness's passwordreset.Mailer: the seam a consumer
// mails the reset link through, remembering the secret instead. It is handed
// to the service the composition root mounts, so what it sees is exactly what
// a consumer's mailer would be handed, after the transaction that issued the
// link has committed.
type resetMailbox struct {
	secrets sync.Map
}

var _ passwordreset.Mailer = (*resetMailbox)(nil)

// SendPasswordReset keeps the latest secret per address. The latest rather than
// every one, because that is what a person with several links in their inbox
// clicks, and a suite that wants an earlier one reads it before asking again.
func (m *resetMailbox) SendPasswordReset(_ context.Context, mail *passwordreset.Mail) error {
	if mail == nil || mail.User == nil || mail.Issuance == nil {
		return errNoResetMailed
	}

	m.secrets.Store(mail.User.EmailAddress, mail.Issuance.Secret)

	return nil
}

var errNoResetMailed = platformerrors.New("conformance harness: no password reset reached that address")

// token is the PasswordResetToken action.
func (m *resetMailbox) token(_ context.Context, _ tenancy.Scope, emailAddress string) (string, error) {
	value, ok := m.secrets.Load(emailAddress)
	if !ok {
		return "", errNoResetMailed
	}

	secret, ok := value.(string)
	if !ok {
		return "", errNoResetMailed
	}

	return secret, nil
}
