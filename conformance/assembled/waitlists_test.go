package assembled_test

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/links"
	linkscfg "github.com/primandproper/platform-go/v15/links/config"
	linksdatabase "github.com/primandproper/platform-go/v15/links/database"
	waitlistsgrpc "github.com/primandproper/platform-go/v15/waitlists/grpc"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// waitlistConfirmation says whether an assembled service runs the waitlist
// confirmation loop.
//
// It is the one thing besides the database that differs between this harness's
// runs, and it differs so that both of the waitlists suite's branches run
// somewhere: the SQLite run confirms, which is the branch that needs the links
// table and the mailbox below, and the container runs do not, which is the
// branch every deployment had before the loop existed.
type waitlistConfirmation bool

const (
	confirmsWaitlists   waitlistConfirmation = true
	waitlistsWaitAtOnce waitlistConfirmation = false
)

// waitlistLinksConfig is the Links block a confirming run adds: the two actions
// the loop mints, pointing at a host that routes nowhere, and no sweeper, since
// nothing in a run outlives a link's lifetime.
func waitlistLinksConfig(prefix string) *linkscfg.Config {
	noSweeper := time.Duration(0)

	return &linkscfg.Config{
		Actions: map[links.Action]links.ActionPolicy{
			waitlistsgrpc.ConfirmAction: {
				URL: "https://conformance.invalid/waitlist/confirm/{token}",
				TTL: links.Duration(time.Hour),
			},
			waitlistsgrpc.UnsubscribeAction: {
				URL: "https://conformance.invalid/waitlist/unsubscribe/{token}",
				TTL: links.Duration(time.Hour),
			},
		},
		SweepInterval: &noSweeper,
		Database:      linksdatabase.Config{TablePrefix: prefix},
	}
}

// waitlistMailbox is this harness's waitlistsgrpc.ConfirmationMailer, keeping
// the tokens instead of sending them. It is handed to the service the
// composition root mounts, so what it sees is exactly what a consumer's mailer
// would be handed, after the join has committed.
type waitlistMailbox struct {
	mailed sync.Map
}

var _ waitlistsgrpc.ConfirmationMailer = (*waitlistMailbox)(nil)

var errNoWaitlistLinksMailed = platformerrors.New("conformance harness: no waitlist links reached that address")

// waitlistMailKey is what a mail is filed under: the tenant, the list and the
// address folded, which is how the store tells one person from another.
func waitlistMailKey(scope tenancy.Scope, listID, contact string) string {
	return scope.Owner() + "\x00" + listID + "\x00" + strings.ToLower(strings.TrimSpace(contact))
}

// SendConfirmation keeps the latest pair per address, for resetMailbox's
// reason: the latest is what a person with several in their inbox follows.
func (m *waitlistMailbox) SendConfirmation(
	_ context.Context,
	scope tenancy.Scope,
	mail *waitlistsgrpc.ConfirmationMail,
) error {
	if mail == nil || mail.Signup == nil || mail.Confirm == nil || mail.Unsubscribe == nil {
		return errNoWaitlistLinksMailed
	}

	m.mailed.Store(waitlistMailKey(scope, mail.Signup.ListID, mail.Signup.Contact), &conformance.WaitlistLinks{
		Confirm:     string(mail.Confirm.Token),
		Unsubscribe: string(mail.Unsubscribe.Token),
	})

	return nil
}

// links is the WaitlistLinks action.
func (m *waitlistMailbox) links(
	_ context.Context,
	scope tenancy.Scope,
	listID, contact string,
) (*conformance.WaitlistLinks, error) {
	value, ok := m.mailed.Load(waitlistMailKey(scope, listID, contact))
	if !ok {
		return nil, errNoWaitlistLinksMailed
	}

	mailed, ok := value.(*conformance.WaitlistLinks)
	if !ok {
		return nil, errNoWaitlistLinksMailed
	}

	return mailed, nil
}

// waitlistLinks is the WaitlistLinks action a run supplies, which is nil on a
// run that does not confirm: the seam's presence is the statement that joins
// are confirmed, so a run that does not must leave it out.
func waitlistLinks(
	waitlists waitlistConfirmation,
	mailbox *waitlistMailbox,
) func(context.Context, tenancy.Scope, string, string) (*conformance.WaitlistLinks, error) {
	if waitlists != confirmsWaitlists {
		return nil
	}

	return mailbox.links
}
