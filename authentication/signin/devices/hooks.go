package devices

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks are a sign-in service's hooks with the device recorded beside every
// token issued. Every method but AfterIssueToken is the wrapped hooks'.
type Hooks struct {
	signin.Hooks

	store   Store
	extract Extractor
}

var _ signin.Hooks = (*Hooks)(nil)

// NewHooks wraps a sign-in service's hooks so that every token issued for a
// login records where its request came from, as extract reads it.
//
// A nil inner wraps signin.NoopHooks, for a deployment whose only hook is this
// one. The store and the extractor are required: see [ErrNilExtractor] for why
// the extractor has no default.
func NewHooks(inner signin.Hooks, store Store, extract Extractor) (*Hooks, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if extract == nil {
		return nil, ErrNilExtractor
	}

	if inner == nil {
		inner = signin.NoopHooks{}
	}

	return &Hooks{Hooks: inner, store: store, extract: extract}, nil
}

// AfterIssueToken implements signin.Hooks.
//
// It runs the wrapped hooks first, so a sign-in they refuse records no device,
// and then records on the token's own transaction — so a device that could not
// be recorded fails the sign-in rather than leaving a login the screen cannot
// place. See the package documentation for both.
//
// It records nothing for an impersonation, which is every sign-in with an
// actor: the request behind one is the operator's, and the login it begins is
// listed to the subject. It records nothing either for a sign-in that names no
// login or nobody, which signin never hands it, because there is nothing to
// key a row on.
//
// The row lives as long as the login could: until its refresh token would
// lapse, or, for a login minted without one, until its access token does.
func (h *Hooks) AfterIssueToken(ctx context.Context, tx database.Tx, scope tenancy.Scope, signIn *signin.SignIn) error {
	if err := h.Hooks.AfterIssueToken(ctx, tx, scope, signIn); err != nil {
		return err
	}

	if signIn == nil || signIn.ActorID != "" || signIn.FamilyID == "" ||
		signIn.Principal == nil || signIn.Principal.User == nil || signIn.Principal.User.ID == "" {
		return nil
	}

	expiresAt := signIn.RefreshTokenExpiresAt
	if signIn.ExpiresAt.After(expiresAt) {
		expiresAt = signIn.ExpiresAt
	}

	if err := h.store.Record(ctx, tx, scope, &Sighting{
		FamilyID:  signIn.FamilyID,
		UserID:    signIn.Principal.User.ID,
		Origin:    h.extract(ctx),
		ExpiresAt: expiresAt,
	}); err != nil {
		return platformerrors.Wrap(err, "recording a sign-in's device")
	}

	return nil
}
