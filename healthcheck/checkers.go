package healthcheck

import (
	"context"

	"github.com/primandproper/platform-go/v6/database"
	"github.com/primandproper/platform-go/v6/errors"
)

// DatabaseReadyChecker checks if a database client is ready.
type DatabaseReadyChecker interface {
	IsReady(ctx context.Context) bool
}

// NewDatabaseChecker returns a Checker that uses the given client's IsReady method.
func NewDatabaseChecker(name string, client DatabaseReadyChecker) Checker {
	return &databaseChecker{name: name, client: client}
}

type databaseChecker struct {
	client DatabaseReadyChecker
	name   string
}

func (d *databaseChecker) Name() string {
	return d.name
}

func (d *databaseChecker) Check(ctx context.Context) error {
	if d.client == nil {
		return errors.New("database client is nil")
	}
	if !d.client.IsReady(ctx) {
		return database.ErrDatabaseNotReady
	}
	return nil
}

// CacheReadyChecker checks if a cache client is ready.
type CacheReadyChecker interface {
	Ping(ctx context.Context) error
}

// NewCacheChecker returns a Checker that pings the given cache client.
func NewCacheChecker(name string, client CacheReadyChecker) Checker {
	return &cacheChecker{name: name, client: client}
}

type cacheChecker struct {
	client CacheReadyChecker
	name   string
}

func (c *cacheChecker) Name() string {
	return c.name
}

func (c *cacheChecker) Check(ctx context.Context) error {
	if c.client == nil {
		return errors.New("cache client is nil")
	}
	return c.client.Ping(ctx)
}

// MessageQueueReadyChecker checks if a message queue client is ready.
type MessageQueueReadyChecker interface {
	Ping(ctx context.Context) error
}

// NewMessageQueueChecker returns a Checker that pings the given message queue client.
func NewMessageQueueChecker(name string, client MessageQueueReadyChecker) Checker {
	return &messageQueueChecker{name: name, client: client}
}

type messageQueueChecker struct {
	client MessageQueueReadyChecker
	name   string
}

func (m *messageQueueChecker) Name() string {
	return m.name
}

func (m *messageQueueChecker) Check(ctx context.Context) error {
	if m.client == nil {
		return errors.New("message queue client is nil")
	}
	return m.client.Ping(ctx)
}
