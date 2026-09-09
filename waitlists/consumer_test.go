package waitlists_test

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The consumer's own waitlist repository, transcribed.
//
// This module is sized against a real caller rather than against what a waitlist
// could be, which is the precedent identity set: the interface is the set of
// operations somebody already wrote, and this file is where that claim stops
// being an assertion in a design document. Every method below is spelled the way
// the consumer spells it, and the adapter under them is what a port of that
// repository onto this package would be — so a change here that made one of them
// inexpressible fails this test rather than being discovered during the port.
//
// Two of the consumer's parameters are absent from these signatures, and both
// are absorbed rather than dropped. Its `belongs_to_account` is the tenancy
// scope, which is what a tenant-owned row's owner column is under this module's
// doctrine; its `belongs_to_user` is a waitlists.Subject.
type consumerRepository interface {
	WaitlistIsNotExpired(ctx context.Context, waitlistID string) (bool, error)
	GetWaitlist(ctx context.Context, waitlistID string) (*waitlists.List, error)
	GetWaitlists(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[waitlists.List], error)
	GetActiveWaitlists(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[waitlists.List], error)
	CreateWaitlist(ctx context.Context, name, description string, validUntil time.Time) (*waitlists.List, error)
	UpdateWaitlist(ctx context.Context, waitlist *waitlists.List) error
	ArchiveWaitlist(ctx context.Context, waitlistID string) error

	GetWaitlistSignup(ctx context.Context, waitlistSignupID, waitlistID string) (*waitlists.Signup, error)
	GetWaitlistSignupsForWaitlist(ctx context.Context, waitlistID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[waitlists.Signup], error)
	GetWaitlistSignupsForUser(ctx context.Context, userID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[waitlists.Signup], error)
	CreateWaitlistSignup(ctx context.Context, waitlistID, contact, notes, userID string) (*waitlists.Signup, error)
	UpdateWaitlistSignup(ctx context.Context, waitlistID, waitlistSignupID, notes string) error
	ArchiveWaitlistSignup(ctx context.Context, waitlistID, waitlistSignupID string) error
}

// consumerAdapter is the whole of the port: one scope bound at construction, and
// one call through to the store per method.
//
// Nothing here computes, caches, or compensates for something the store will not
// do, which is the point of writing it out. A method here that needed a second
// query or a pass over a page would be a method the interface does not really
// serve, and it would be visible as one.
//
// It holds the client as well as the store, and that is the port's one visible
// cost: every write takes a database.Tx, so a repository method with nothing to
// commit alongside opens a transaction of its own. It is also the port's
// payoff — the consumer's own audit entry goes inside that closure, which is
// where it could not go before.
type consumerAdapter struct {
	store  waitlists.Store
	client database.Client
	scope  tenancy.Scope
}

var _ consumerRepository = (*consumerAdapter)(nil)

// WaitlistIsNotExpired is answered in Go rather than by a query, because the
// list has to be read to be reported on either way and List.OpenAt is the same
// comparison ListOpenLists pages by.
func (a *consumerAdapter) WaitlistIsNotExpired(ctx context.Context, waitlistID string) (bool, error) {
	list, err := a.store.GetList(ctx, a.client.Reader(), a.scope, waitlistID)
	if err != nil {
		return false, err
	}

	return list.OpenAt(time.Now()), nil
}

func (a *consumerAdapter) GetWaitlist(ctx context.Context, waitlistID string) (*waitlists.List, error) {
	return a.store.GetList(ctx, a.client.Reader(), a.scope, waitlistID)
}

func (a *consumerAdapter) GetWaitlists(
	ctx context.Context,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[waitlists.List], error) {
	return a.store.ListLists(ctx, a.client.Reader(), a.scope, filter)
}

func (a *consumerAdapter) GetActiveWaitlists(
	ctx context.Context,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[waitlists.List], error) {
	return a.store.ListOpenLists(ctx, a.client.Reader(), a.scope, filter)
}

// CreateWaitlist maps the consumer's valid_until onto ClosesAt. The consumer's
// column is NOT NULL too, so nothing about its data needs a state this schema
// declines to hold.
func (a *consumerAdapter) CreateWaitlist(
	ctx context.Context,
	name, description string,
	validUntil time.Time,
) (*waitlists.List, error) {
	var created *waitlists.List

	err := a.client.WithTransaction(ctx, func(tx database.Tx) (createErr error) {
		created, createErr = a.store.CreateList(ctx, tx, a.scope, &waitlists.List{
			Name:        name,
			Description: description,
			ClosesAt:    validUntil,
		})

		return createErr
	})

	return created, err
}

func (a *consumerAdapter) UpdateWaitlist(ctx context.Context, waitlist *waitlists.List) error {
	return a.client.WithTransaction(ctx, func(tx database.Tx) error {
		return a.store.UpdateList(ctx, tx, a.scope, waitlist)
	})
}

func (a *consumerAdapter) ArchiveWaitlist(ctx context.Context, waitlistID string) error {
	return a.client.WithTransaction(ctx, func(tx database.Tx) error {
		return a.store.ArchiveList(ctx, tx, a.scope, waitlistID)
	})
}

func (a *consumerAdapter) GetWaitlistSignup(
	ctx context.Context,
	waitlistSignupID, waitlistID string,
) (*waitlists.Signup, error) {
	return a.store.GetSignup(ctx, a.client.Reader(), a.scope, waitlistID, waitlistSignupID)
}

func (a *consumerAdapter) GetWaitlistSignupsForWaitlist(
	ctx context.Context,
	waitlistID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
	return a.store.ListSignups(ctx, a.client.Reader(), a.scope, waitlistID, filter)
}

func (a *consumerAdapter) GetWaitlistSignupsForUser(
	ctx context.Context,
	userID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
	return a.store.ListSignupsForSubject(ctx, a.client.Reader(), a.scope,
		waitlists.Subject{Type: waitlists.SubjectUser, ID: userID}, filter)
}

// CreateWaitlistSignup takes a contact the consumer's own table does not have,
// which is the one place the port adds a column rather than mapping one. Its
// signups hang off a user row and reach the address through it; this table holds
// the address, because a pre-launch list has nothing else to write to.
func (a *consumerAdapter) CreateWaitlistSignup(
	ctx context.Context,
	waitlistID, contact, notes, userID string,
) (*waitlists.Signup, error) {
	var joined *waitlists.Signup

	err := a.client.WithTransaction(ctx, func(tx database.Tx) (joinErr error) {
		joined, joinErr = a.store.Join(ctx, tx, a.scope, waitlistID, &waitlists.Signup{
			Contact: contact,
			Notes:   notes,
			Subject: waitlists.Subject{Type: waitlists.SubjectUser, ID: userID},
		})

		return joinErr
	})

	return joined, err
}

func (a *consumerAdapter) UpdateWaitlistSignup(ctx context.Context, waitlistID, waitlistSignupID, notes string) error {
	return a.client.WithTransaction(ctx, func(tx database.Tx) error {
		return a.store.UpdateSignupNotes(ctx, tx, a.scope, waitlistID, waitlistSignupID, notes)
	})
}

func (a *consumerAdapter) ArchiveWaitlistSignup(ctx context.Context, waitlistID, waitlistSignupID string) error {
	return a.client.WithTransaction(ctx, func(tx database.Tx) error {
		return a.store.ArchiveSignup(ctx, tx, a.scope, waitlistID, waitlistSignupID)
	})
}

// TestConsumerOperationsAreExpressible runs the transcribed repository end to
// end, so that "expressible" means the calls return what the consumer's own
// tests expect rather than merely that the adapter compiles.
func TestConsumerOperationsAreExpressible(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	// The consumer is single-tenant and its belongs_to_account is the scope, so
	// a port names one and passes it everywhere.
	store, client := exampleWiring()
	repo := &consumerAdapter{store: store, client: client, scope: tenancy.Of("account-1")}

	list, err := repo.CreateWaitlist(ctx, "Launch", "early access", time.Now().Add(24*time.Hour))
	must.NoError(t, err)
	must.NotNil(t, list)

	open, err := repo.WaitlistIsNotExpired(ctx, list.ID)
	must.NoError(t, err)
	test.True(t, open)

	read, err := repo.GetWaitlist(ctx, list.ID)
	must.NoError(t, err)
	test.EqOp(t, list.ID, read.ID)

	all, err := repo.GetWaitlists(ctx, nil)
	must.NoError(t, err)
	test.SliceLen(t, 1, all.Data)

	active, err := repo.GetActiveWaitlists(ctx, nil)
	must.NoError(t, err)
	test.SliceLen(t, 1, active.Data)

	read.Name = "Beta"
	must.NoError(t, repo.UpdateWaitlist(ctx, read))

	signup, err := repo.CreateWaitlistSignup(ctx, list.ID, "ada@example.com", "met at the conference", "user-1")
	must.NoError(t, err)
	must.NotNil(t, signup)

	byID, err := repo.GetWaitlistSignup(ctx, signup.ID, list.ID)
	must.NoError(t, err)
	test.EqOp(t, signup.ID, byID.ID)

	forList, err := repo.GetWaitlistSignupsForWaitlist(ctx, list.ID, nil)
	must.NoError(t, err)
	test.SliceLen(t, 1, forList.Data)

	forUser, err := repo.GetWaitlistSignupsForUser(ctx, "user-1", nil)
	must.NoError(t, err)
	test.SliceLen(t, 1, forUser.Data)

	must.NoError(t, repo.UpdateWaitlistSignup(ctx, list.ID, signup.ID, "invited to the demo"))
	must.NoError(t, repo.ArchiveWaitlistSignup(ctx, list.ID, signup.ID))
	must.NoError(t, repo.ArchiveWaitlist(ctx, list.ID))

	// And the lifecycle the consumer's implementation does not have, which is
	// the half of this package it would be adopting rather than porting.
	second, err := repo.CreateWaitlist(ctx, "Launch", "", time.Now().Add(24*time.Hour))
	must.NoError(t, err)

	joined, err := repo.CreateWaitlistSignup(ctx, second.ID, "grace@example.com", "", "user-2")
	must.NoError(t, err)

	must.NoError(t, repo.client.WithTransaction(ctx, func(tx database.Tx) error {
		if err = repo.store.Invite(ctx, tx, repo.scope, second.ID, joined.ID); err != nil {
			return err
		}

		if err = repo.store.Convert(ctx, tx, repo.scope, second.ID, joined.ID); err != nil {
			return err
		}

		return repo.store.Withdraw(ctx, tx, repo.scope, second.ID, joined.ID)
	}))
}
