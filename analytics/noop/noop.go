package noop

import (
	"context"

	"github.com/primandproper/platform-go/v6/analytics"
)

var _ analytics.EventReporter = (*eventReporter)(nil)

// eventReporter is a no-op EventReporter.
type eventReporter struct{}

// NewEventReporter returns a new no-op EventReporter.
func NewEventReporter() analytics.EventReporter {
	return &eventReporter{}
}

// Close does nothing.
func (c *eventReporter) Close() {}

// AddUser does nothing.
func (c *eventReporter) AddUser(context.Context, string, map[string]any) error {
	return nil
}

// EventOccurred does nothing.
func (c *eventReporter) EventOccurred(context.Context, string, string, map[string]any) error {
	return nil
}

// EventOccurredAnonymous does nothing.
func (c *eventReporter) EventOccurredAnonymous(context.Context, string, string, map[string]any) error {
	return nil
}
