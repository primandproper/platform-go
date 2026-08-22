package memory

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability"
)

// serviceName names the loggers and spans this store emits.
const serviceName = "oauth2server_memory"

var _ oauth2server.Store = (*Store)(nil)

// Store keeps every authorization server record in maps behind one mutex.
//
// One mutex rather than one per map, because the operations that matter span
// maps: revoking a family touches both token maps, and consuming a code has to
// be atomic against everything else. Four locks would make that a lock-ordering
// problem in a type whose entire job is to be the simple one.
type Store struct {
	clock clock.Clock
	o11y  observability.Observer

	clients map[string]*oauth2server.Client
	codes   map[string]*oauth2server.AuthorizationCode
	access  map[string]*oauth2server.AccessToken
	refresh map[string]*oauth2server.RefreshToken
	mu      sync.Mutex
}

// NewStore builds an in-memory Store.
//
// It is for tests, for single-process development, and for nothing else. Two
// replicas do not share it, which under this package's flow means the
// authorization code issued by one replica cannot be redeemed at the other —
// so a login fails whenever the load balancer does its job. A restart loses
// every registered client, which under dynamic registration is every client
// there is. Deployments want oauth2server/database.
func NewStore(opts ...Option) *Store {
	o := newOptions(opts)

	s := &Store{
		clock:   o.clock,
		o11y:    observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		clients: map[string]*oauth2server.Client{},
		codes:   map[string]*oauth2server.AuthorizationCode{},
		access:  map[string]*oauth2server.AccessToken{},
		refresh: map[string]*oauth2server.RefreshToken{},
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s
}

// CreateClient records a registration.
func (s *Store) CreateClient(ctx context.Context, client *oauth2server.Client) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if client == nil {
		return oauth2server.ErrNilRecord
	}
	if client.ID == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[client.ID]; ok {
		return oauth2server.ErrClientExists
	}

	s.clients[client.ID] = client.Clone()

	return nil
}

// GetClient reads a registration.
func (s *Store) GetClient(ctx context.Context, clientID string) (*oauth2server.Client, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if clientID == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[clientID]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	// A zero ExpiresAt is a registration that does not lapse, which is not the
	// same as one that lapsed at the zero time.
	if !client.ExpiresAt.IsZero() && !s.now().Before(client.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	return client.Clone(), nil
}

// DeleteClient removes a registration. Absent is not an error.
func (s *Store) DeleteClient(ctx context.Context, clientID string) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if clientID == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.clients, clientID)

	return nil
}

// CreateAuthorizationCode records an issued code.
func (s *Store) CreateAuthorizationCode(ctx context.Context, code *oauth2server.AuthorizationCode) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if code == nil {
		return oauth2server.ErrNilRecord
	}
	if code.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.codes[code.Hash]; ok {
		return oauth2server.ErrRecordExists
	}

	s.codes[code.Hash] = code.Clone()

	return nil
}

// ConsumeAuthorizationCode marks a code redeemed and returns it.
//
// The whole method runs under the mutex, which is what makes two concurrent
// redemptions of one code resolve to exactly one success — the case the
// conformance suite exists to hold every implementation to.
func (s *Store) ConsumeAuthorizationCode(ctx context.Context, hash string) (*oauth2server.AuthorizationCode, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	code, ok := s.codes[hash]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	// A replay is reported with the record, because the caller's response to it
	// is to revoke what this code issued and it cannot find those without the
	// record. Expiry is checked second: a code that was redeemed and then
	// expired is still a replay, and that is the more actionable answer.
	if !code.RedeemedAt.IsZero() {
		return code.Clone(), oauth2server.ErrAlreadyRedeemed
	}

	if !s.now().Before(code.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	code.RedeemedAt = s.now()

	return code.Clone(), nil
}

// CreateAccessToken records an issued access token.
func (s *Store) CreateAccessToken(ctx context.Context, token *oauth2server.AccessToken) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if token == nil {
		return oauth2server.ErrNilRecord
	}
	if token.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.access[token.Hash]; ok {
		return oauth2server.ErrRecordExists
	}

	s.access[token.Hash] = token.Clone()

	return nil
}

// GetAccessToken reads an access token.
func (s *Store) GetAccessToken(ctx context.Context, hash string) (*oauth2server.AccessToken, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.access[hash]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	if !token.Active(s.now()) {
		return nil, oauth2server.ErrExpired
	}

	return token.Clone(), nil
}

// RevokeAccessToken marks one access token revoked. Absent is not an error.
func (s *Store) RevokeAccessToken(ctx context.Context, hash string) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if token, ok := s.access[hash]; ok && token.RevokedAt.IsZero() {
		token.RevokedAt = s.now()
	}

	return nil
}

// CreateRefreshToken records an issued refresh token.
func (s *Store) CreateRefreshToken(ctx context.Context, token *oauth2server.RefreshToken) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if token == nil {
		return oauth2server.ErrNilRecord
	}
	if token.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.refresh[token.Hash]; ok {
		return oauth2server.ErrRecordExists
	}

	s.refresh[token.Hash] = token.Clone()

	return nil
}

// ConsumeRefreshToken marks a refresh token redeemed and returns it.
func (s *Store) ConsumeRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.refresh[hash]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	if !token.RedeemedAt.IsZero() {
		return token.Clone(), oauth2server.ErrAlreadyRedeemed
	}

	// A revoked token is reported as expired rather than as a replay. It was
	// never exchanged, so revoking its family a second time would tell the
	// caller a reuse happened when what happened is that somebody signed out.
	if !token.RevokedAt.IsZero() || !s.now().Before(token.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	token.RedeemedAt = s.now()

	return token.Clone(), nil
}

// GetRefreshToken reads a refresh token without consuming it.
func (s *Store) GetRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.refresh[hash]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	// Redeemed is deliberately not a reason to withhold it — see the interface.
	if !token.RevokedAt.IsZero() || !s.now().Before(token.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	return token.Clone(), nil
}

// RevokeRefreshToken marks one refresh token revoked. Absent is not an error.
func (s *Store) RevokeRefreshToken(ctx context.Context, hash string) error {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if token, ok := s.refresh[hash]; ok && token.RevokedAt.IsZero() {
		token.RevokedAt = s.now()
	}

	return nil
}

// RevokeFamily revokes every access and refresh token in a family.
func (s *Store) RevokeFamily(ctx context.Context, familyID string) (int64, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	if familyID == "" {
		return 0, oauth2server.ErrEmptyIdentifier
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	var revoked int64

	for _, token := range s.access {
		if token.FamilyID == familyID && token.RevokedAt.IsZero() {
			token.RevokedAt = now
			revoked++
		}
	}

	for _, token := range s.refresh {
		if token.FamilyID == familyID && token.RevokedAt.IsZero() {
			token.RevokedAt = now
			revoked++
		}
	}

	return revoked, nil
}

// Sweep removes records whose deadlines have passed.
//
// Revoked-but-unexpired tokens are deliberately kept: a resource server holding
// one is entitled to be told "no" rather than to have its request treated as
// carrying a token nobody ever issued, and the difference shows up in whichever
// log line an operator reads when a user complains that signing out did not
// work.
func (s *Store) Sweep(ctx context.Context, now time.Time) (int64, error) {
	_, op := s.o11y.Begin(ctx)
	defer op.End()

	s.mu.Lock()
	defer s.mu.Unlock()

	var swept int64

	maps.DeleteFunc(s.codes, func(_ string, c *oauth2server.AuthorizationCode) bool {
		dead := !now.Before(c.ExpiresAt)
		if dead {
			swept++
		}

		return dead
	})

	maps.DeleteFunc(s.access, func(_ string, t *oauth2server.AccessToken) bool {
		dead := !now.Before(t.ExpiresAt)
		if dead {
			swept++
		}

		return dead
	})

	maps.DeleteFunc(s.refresh, func(_ string, t *oauth2server.RefreshToken) bool {
		dead := !now.Before(t.ExpiresAt)
		if dead {
			swept++
		}

		return dead
	})

	maps.DeleteFunc(s.clients, func(_ string, c *oauth2server.Client) bool {
		dead := !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
		if dead {
			swept++
		}

		return dead
	})

	return swept, nil
}

// Close releases nothing. The maps go when the Store does.
func (s *Store) Close() error { return nil }

// now reads the clock at the resolution every stamped time uses, so that a
// record written here and one written by the database store compare the same
// way.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// sweepEvery sweeps on every tick until ctx is done. Ticks come from the
// injected clock, so a synctest bubble advances it without a test double.
func (s *Store) sweepEvery(ctx context.Context, interval time.Duration) {
	ticker := s.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			// Nothing is waiting on this goroutine, and this Sweep cannot fail
			// — the error is in the signature because the interface has a
			// backend that can.
			//nolint:errcheck // this Sweep cannot fail; the error is in the signature because the interface has a backend that can.
			_, _ = s.Sweep(ctx, s.now())
		}
	}
}
