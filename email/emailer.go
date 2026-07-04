package email

import (
	"context"
)

type (
	// EmailBranding holds app-specific branding used when building Hermes email templates.
	EmailBranding struct {
		CompanyName string
		LogoURL     string
	}

	// OutboundEmailMessage is a collection of fields that are useful for sending emails.
	OutboundEmailMessage struct {
		UserID      string
		ToAddress   string
		ToName      string
		FromAddress string
		FromName    string
		Subject     string
		HTMLContent string
		TestID      string `json:"testID,omitempty"`
	}

	// Emailer represents a service that can send emails.
	Emailer interface {
		SendEmail(ctx context.Context, details *OutboundEmailMessage) error
	}
)
