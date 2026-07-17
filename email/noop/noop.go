package noop

import (
	"context"

	"github.com/primandproper/platform-go/v5/email"
)

var _ email.Emailer = (*emailer)(nil)

// emailer doesn't send emails.
type emailer struct{}

// NewEmailer returns a new no-op Emailer.
func NewEmailer() (email.Emailer, error) {
	return &emailer{}, nil
}

// SendEmail is a no-op.
func (*emailer) SendEmail(context.Context, *email.OutboundEmailMessage) error {
	return nil
}
