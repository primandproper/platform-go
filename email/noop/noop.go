// Package noop is the email.Emailer that sends nothing. SendEmail accepts every
// message and returns nil, so password resets, receipts, and verification links
// are addressed, rendered, and dropped — with no bounce, no provider-side log,
// and no error for anyone to notice.
//
// The absence of a signal is what makes it a test and local-development choice
// and a hazard anywhere else. A deployment that reaches this by accident reports
// itself healthy while every user waiting on a verification link is stuck, and
// nothing in the metrics distinguishes that from a quiet week. email/config
// builds it only for the "noop" provider name; an unrecognized name is
// errors.ErrUnknownProvider.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/email"
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
