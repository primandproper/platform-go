// Package redisclient builds the go-redis client every Redis-backed package in
// this module talks through.
//
// Five packages — cache, distributedlock, ratelimiting, and the messagequeue
// consumer and publisher — each open their own connection from their own
// address list, and each had written the same single-node-or-cluster dispatch
// by hand. The copies disagreed on the two things that matter: whether a
// connection carries timeouts at all, and what happens when the address list is
// empty. Two of them answered the second question with a nil client, which is
// not a configuration error at startup but a nil-pointer panic on the first
// cache read.
//
// So the dispatch lives here once, with the timeouts as the shared default and
// an empty address list as an error a constructor must handle. What stays with
// each package is the interface it narrows the client to: this returns
// go-redis's UniversalClient, and every caller assigns it to a hand-written
// interface naming only the commands it issues, so a package that never
// publishes cannot publish.
package redisclient

import (
	"time"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/redis/go-redis/v9"
)

// defaultTimeout bounds dialing and each read and write on the connection.
//
// Without it go-redis waits forever, and a Redis that has stopped answering —
// a network partition, a paused instance mid-failover — becomes an unbounded
// stall in whatever called into the cache rather than an error the caller's own
// timeout or circuit breaker can act on. One second is the value the cache and
// distributedlock copies already carried; the packages that carried none were
// the drift, not the default.
const defaultTimeout = time.Second

// Config is the connection half of a Redis-backed package's configuration:
// where the server is and who to authenticate as. Each package keeps its own
// Config with its own env tags and validation, and maps it onto this.
type Config struct {
	// Username and Password authenticate the connection. Both empty means no
	// AUTH is sent.
	Username string
	Password string

	// Addresses is the server or servers to connect to. It must hold at least
	// one entry; New reports an error otherwise.
	Addresses []string

	// Cluster forces Redis Cluster mode even when a single address is given.
	//
	// Two or more addresses already imply a cluster, but a cluster reached
	// through one configuration endpoint — an ElastiCache configuration
	// endpoint, a single seed node — does not, and a client that misreads it as
	// single-node fails multi-key commands with CROSSSLOT rather than routing
	// them.
	Cluster bool
}

// New opens a Redis client for cfg: a cluster client when cfg describes a
// cluster, a single-node client otherwise.
//
// An empty address list is an error rather than a nil client. There is no
// address that means "no Redis", so a config that named none is one nobody
// finished writing, and the honest moment to say so is here — at construction,
// where the caller still has somewhere to return it to.
func New(cfg Config) (redis.UniversalClient, error) {
	if len(cfg.Addresses) == 0 {
		return nil, errors.Wrap(errors.ErrEmptyInputParameter, "at least one redis address is required")
	}

	return redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:         cfg.Addresses,
		Username:      cfg.Username,
		Password:      cfg.Password,
		IsClusterMode: cfg.Cluster,
		DialTimeout:   defaultTimeout,
		ReadTimeout:   defaultTimeout,
		WriteTimeout:  defaultTimeout,
	}), nil
}
