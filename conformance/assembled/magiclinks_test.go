package assembled_test

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// magicLinkMailbox is this harness's signin.MagicLinkMailer: the seam a
// consumer mails a sign-in link through, remembering the secret instead. It is
// handed to the service the composition root mounts, so what it sees is exactly
// what a consumer's mailer would be handed, after the transaction that minted
// the link has committed.
type magicLinkMailbox struct {
	secrets sync.Map
}

var _ signin.MagicLinkMailer = (*magicLinkMailbox)(nil)

// SendMagicLink keeps the latest secret per address, for resetMailbox's reason:
// the latest is what a person with several links in their inbox clicks.
func (m *magicLinkMailbox) SendMagicLink(_ context.Context, mail *signin.MagicLinkMail) error {
	if mail == nil || mail.User == nil || mail.Issuance == nil {
		return errNoMagicLinkMailed
	}

	m.secrets.Store(mail.User.EmailAddress, mail.Issuance.Secret)

	return nil
}

var errNoMagicLinkMailed = platformerrors.New("conformance harness: no sign-in link reached that address")

// token is the MagicLinkToken action.
func (m *magicLinkMailbox) token(_ context.Context, _ tenancy.Scope, emailAddress string) (string, error) {
	value, ok := m.secrets.Load(emailAddress)
	if !ok {
		return "", errNoMagicLinkMailed
	}

	secret, ok := value.(string)
	if !ok {
		return "", errNoMagicLinkMailed
	}

	return secret, nil
}
