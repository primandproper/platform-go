package assembled_test

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v15/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// handleReminderMailbox is this harness's signin.HandleReminderMailer: the seam
// a consumer mails somebody their handle through, remembering the handle
// instead. It is handed to the service the composition root mounts, so what it
// sees is exactly what a consumer's mailer would be handed.
type handleReminderMailbox struct {
	handles sync.Map
}

var _ signin.HandleReminderMailer = (*handleReminderMailbox)(nil)

// SendHandleReminder keeps the handle per address it was mailed to.
func (m *handleReminderMailbox) SendHandleReminder(_ context.Context, mail *signin.HandleReminderMail) error {
	if mail == nil || mail.User == nil {
		return errNoHandleMailed
	}

	m.handles.Store(mail.User.EmailAddress, mail.User.Username)

	return nil
}

var errNoHandleMailed = platformerrors.New("conformance harness: no handle reminder reached that address")

// handle is the HandleReminder action.
func (m *handleReminderMailbox) handle(_ context.Context, _ tenancy.Scope, emailAddress string) (string, error) {
	value, ok := m.handles.Load(emailAddress)
	if !ok {
		return "", errNoHandleMailed
	}

	handle, ok := value.(string)
	if !ok {
		return "", errNoHandleMailed
	}

	return handle, nil
}
