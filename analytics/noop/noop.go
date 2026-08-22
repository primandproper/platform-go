// Package noop is the analytics.EventReporter for a deployment that measures
// nothing. Every AddUser and EventOccurred call is accepted and discarded: no
// identify or track call reaches a vendor, and no funnel, cohort, or retention
// figure counts this process. Nothing here fails, so every error return is nil
// and a caller's failure branch is unreachable.
//
// analytics/config selects it only when the provider is named "noop". A name it
// does not recognize is errors.ErrUnknownProvider, so a typo cannot quietly
// turn analytics off — the difference matters here, because a reporter that
// silently drops events looks exactly like one whose users are not doing
// anything.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/analytics"
)

var _ analytics.EventReporter = (*EventReporter)(nil)

// EventReporter is a no-op EventReporter.
type EventReporter struct{}

// NewEventReporter returns a new no-op EventReporter.
func NewEventReporter() *EventReporter {
	return &EventReporter{}
}

// Close does nothing.
func (c *EventReporter) Close(context.Context) error {
	return nil
}

// AddUser does nothing.
func (c *EventReporter) AddUser(context.Context, string, map[string]any) error {
	return nil
}

// EventOccurred does nothing.
func (c *EventReporter) EventOccurred(context.Context, string, string, map[string]any) error {
	return nil
}

// EventOccurredAnonymous does nothing.
func (c *EventReporter) EventOccurredAnonymous(context.Context, string, string, map[string]any) error {
	return nil
}
