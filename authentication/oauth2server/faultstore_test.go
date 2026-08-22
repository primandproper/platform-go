package oauth2server_test

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// errStoreDown is what a broken store returns here. It is deliberately none of
// this package's sentinels: every handler below has a branch for ErrNotFound and
// a branch for everything else, and this exercises the second one.
var errStoreDown = platformerrors.New("the database is on fire")

// storeMethod names one Store method, so a test naming a fault cannot misspell
// it.
type storeMethod string

const (
	methodCreateClient       storeMethod = "CreateClient"
	methodGetClient          storeMethod = "GetClient"
	methodCreateCode         storeMethod = "CreateAuthorizationCode"
	methodConsumeCode        storeMethod = "ConsumeAuthorizationCode"
	methodCreateAccessToken  storeMethod = "CreateAccessToken"
	methodGetAccessToken     storeMethod = "GetAccessToken"
	methodRevokeAccessToken  storeMethod = "RevokeAccessToken"
	methodCreateRefreshToken storeMethod = "CreateRefreshToken"
	methodGetRefreshToken    storeMethod = "GetRefreshToken"
	methodConsumeRefresh     storeMethod = "ConsumeRefreshToken"
	methodRevokeFamily       storeMethod = "RevokeFamily"
)

// faultStore is a working store with one or more methods broken on demand.
//
// A store that fails from the first call cannot get a test as far as the branch
// worth reading — most of these paths are reached only after a registration, an
// authorization, and a redemption have all succeeded. So the faults are declared
// mid-flow, under a mutex: the httptest server reads them from its own
// goroutines.
type faultStore struct {
	oauth2server.Store

	faults map[storeMethod]error

	mu sync.RWMutex
}

var _ oauth2server.Store = (*faultStore)(nil)

// newFaultStore wraps a working memory store.
func newFaultStore() *faultStore {
	return &faultStore{Store: memory.NewStore(), faults: map[storeMethod]error{}}
}

// breaks makes one method fail from here on.
func (f *faultStore) breaks(method storeMethod, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.faults[method] = err
}

// heals makes one method work again, for the cases that need a second call to
// succeed after the first has failed.
func (f *faultStore) heals(method storeMethod) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.faults, method)
}

// fault reports what a method should fail with, if anything.
func (f *faultStore) fault(method storeMethod) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return f.faults[method]
}

func (f *faultStore) CreateClient(ctx context.Context, client *oauth2server.Client) error {
	if err := f.fault(methodCreateClient); err != nil {
		return err
	}

	return f.Store.CreateClient(ctx, client)
}

func (f *faultStore) GetClient(ctx context.Context, clientID string) (*oauth2server.Client, error) {
	if err := f.fault(methodGetClient); err != nil {
		return nil, err
	}

	return f.Store.GetClient(ctx, clientID)
}

func (f *faultStore) CreateAuthorizationCode(ctx context.Context, code *oauth2server.AuthorizationCode) error {
	if err := f.fault(methodCreateCode); err != nil {
		return err
	}

	return f.Store.CreateAuthorizationCode(ctx, code)
}

func (f *faultStore) ConsumeAuthorizationCode(ctx context.Context, hash string) (*oauth2server.AuthorizationCode, error) {
	if err := f.fault(methodConsumeCode); err != nil {
		return nil, err
	}

	return f.Store.ConsumeAuthorizationCode(ctx, hash)
}

func (f *faultStore) CreateAccessToken(ctx context.Context, token *oauth2server.AccessToken) error {
	if err := f.fault(methodCreateAccessToken); err != nil {
		return err
	}

	return f.Store.CreateAccessToken(ctx, token)
}

func (f *faultStore) GetAccessToken(ctx context.Context, hash string) (*oauth2server.AccessToken, error) {
	if err := f.fault(methodGetAccessToken); err != nil {
		return nil, err
	}

	return f.Store.GetAccessToken(ctx, hash)
}

func (f *faultStore) RevokeAccessToken(ctx context.Context, hash string) error {
	if err := f.fault(methodRevokeAccessToken); err != nil {
		return err
	}

	return f.Store.RevokeAccessToken(ctx, hash)
}

func (f *faultStore) CreateRefreshToken(ctx context.Context, token *oauth2server.RefreshToken) error {
	if err := f.fault(methodCreateRefreshToken); err != nil {
		return err
	}

	return f.Store.CreateRefreshToken(ctx, token)
}

func (f *faultStore) GetRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	if err := f.fault(methodGetRefreshToken); err != nil {
		return nil, err
	}

	return f.Store.GetRefreshToken(ctx, hash)
}

func (f *faultStore) ConsumeRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	if err := f.fault(methodConsumeRefresh); err != nil {
		return nil, err
	}

	return f.Store.ConsumeRefreshToken(ctx, hash)
}

func (f *faultStore) RevokeFamily(ctx context.Context, familyID string) (int64, error) {
	if err := f.fault(methodRevokeFamily); err != nil {
		return 0, err
	}

	return f.Store.RevokeFamily(ctx, familyID)
}
