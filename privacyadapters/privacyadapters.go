package privacyadapters

import (
	"slices"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsprivacy "github.com/primandproper/platform-go/v14/authentication/oauth2clients/privacy"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetprivacy "github.com/primandproper/platform-go/v14/authentication/passwordreset/privacy"
	"github.com/primandproper/platform-go/v14/billing"
	billingprivacy "github.com/primandproper/platform-go/v14/billing/privacy"
	"github.com/primandproper/platform-go/v14/comments"
	commentsprivacy "github.com/primandproper/platform-go/v14/comments/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/dataprivacy/auditerasure"
	identityprivacy "github.com/primandproper/platform-go/v14/identity/privacy"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsprivacy "github.com/primandproper/platform-go/v14/issuereports/privacy"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryprivacy "github.com/primandproper/platform-go/v14/mediaregistry/privacy"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsprivacy "github.com/primandproper/platform-go/v14/notifications/privacy"
	"github.com/primandproper/platform-go/v14/settings"
	settingsprivacy "github.com/primandproper/platform-go/v14/settings/privacy"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsprivacy "github.com/primandproper/platform-go/v14/waitlists/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// IdentityStore is the directory reach registering identity/privacy needs.
//
// That package declares SubjectReader and SubjectWriter separately and is right
// to: a collector holding a store that could ban a user is a collector with a
// capability its seam cannot express. Both halves come from one store here, so
// the union is what this package asks for, and identity.Store satisfies it.
type IdentityStore interface {
	identityprivacy.SubjectReader
	identityprivacy.SubjectWriter
}

// Adapters names the privacy adapters a deployment has.
//
// A nil field is a domain this deployment does not run, and [Register] skips
// it. They are pointers rather than zero-value structs because "this deployment
// has no comments" and "this deployment has comments and forgot the resolver"
// are two different mistakes, and only the second should be reachable — it is
// the reading service.Config already takes of its own sub-configs.
type Adapters struct {
	_ struct{} `json:"-" yaml:"-"`

	// Reader is the executor every collector's reads run on — Client.Reader()
	// in ordinary wiring, or a database.Tx where an export genuinely has to see
	// writes that transaction has not committed.
	//
	// It is one field rather than eleven because every collector here would be
	// handed the same value, and eleven copies of one value is eleven chances
	// to hand one of them something else. A deployment that needs exactly one
	// collector reading somewhere else builds that one by hand.
	//
	// The erasers take none: dataprivacy.Eraser.Erase is handed the request's
	// transaction, so a subject's whole footprint commits or rolls back
	// together.
	Reader database.SQLQueryExecutor

	Comments      *CommentsAdapter
	IssueReports  *IssueReportsAdapter
	Settings      *SettingsAdapter
	Waitlists     *WaitlistsAdapter
	MediaRegistry *MediaRegistryAdapter
	OAuth2Clients *OAuth2ClientsAdapter
	PasswordReset *PasswordResetAdapter
	Identity      *IdentityAdapter
	Notifications *NotificationsAdapter
	Billing       *BillingAdapter
	AuditErasure  *AuditErasureAdapter
}

// CommentsAdapter registers comments/privacy's collector and eraser.
type CommentsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   comments.Store
	Resolve dataprivacy.ScopeResolver
}

// IssueReportsAdapter registers issuereports/privacy's collector and eraser.
type IssueReportsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   issuereports.Store
	Resolve dataprivacy.ScopeResolver
}

// SettingsAdapter registers settings/privacy's collector and eraser.
type SettingsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   settings.ValueStore
	Resolve dataprivacy.ScopeResolver
}

// WaitlistsAdapter registers waitlists/privacy's collector and eraser. That
// eraser withdraws rather than deletes and reports the contact digests it kept.
type WaitlistsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   waitlists.SignupStore
	Resolve dataprivacy.ScopeResolver
}

// MediaRegistryAdapter registers mediaregistry/privacy's collector and eraser.
// That eraser archives the rows and reports the objects retained, because
// nothing in that package opens the byte path.
type MediaRegistryAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   mediaregistry.Store
	Resolve dataprivacy.ScopeResolver
}

// OAuth2ClientsAdapter registers authentication/oauth2clients/privacy's
// collector and eraser.
type OAuth2ClientsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   oauth2clients.Store
	Resolve dataprivacy.ScopeResolver
}

// PasswordResetAdapter registers authentication/passwordreset/privacy's
// collector and eraser.
type PasswordResetAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   passwordreset.Store
	Resolve dataprivacy.ScopeResolver
}

// IdentityAdapter registers identity/privacy's collector and eraser.
type IdentityAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   IdentityStore
	Resolve dataprivacy.ScopeResolver
}

// NotificationsAdapter registers notifications/privacy, which ships two pairs
// under two keys because it is two tables with two rulings.
//
// The two seams are separate fields, so a deployment with an inbox and no
// device registry leaves Registry nil and gets the inbox pair only. One
// resolver serves both: they are the same subject in the same tenants.
type NotificationsAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Inbox    notifications.Inbox
	Registry notifications.Registry
	Resolve  dataprivacy.ScopeResolver
}

// BillingAdapter registers billing/privacy's collector.
//
// There is no eraser field, because that package ships no Eraser: a
// subscription and a ledger row are financial records every jurisdiction
// requires kept, and a seam whose only correct implementation erases nothing
// invites a deployment to register it and believe its history was deleted.
//
// Its resolver answers with accounts rather than scopes — an account is a scope
// and an id, and neither half is inferable from the other.
type BillingAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Store   billing.Store
	Resolve billingprivacy.AccountResolver
}

// AuditErasureAdapter registers dataprivacy/auditerasure's eraser.
//
// It is the one adapter whose seam is not a store: auditerasure.New renders
// audit's own statements, so it takes the dialect they are rendered for and
// everything else as options — including its resolver, which defaults to
// treating the subject's own id as a scope.
//
// A deployment reading the "do we erase our own audit records" policy off the
// environment uses dataprivacycfg.RegisterAuditEraser instead and leaves this
// nil. Doing both is an error, because the second registration of a key is one.
type AuditErasureAdapter struct {
	_ struct{} `json:"-" yaml:"-"`

	Dialect dialect.Dialect
	Options []auditerasure.Option
}

// Register builds every adapter that adapters names and registers it under its
// package's DefaultKey, returning the keys it registered, sorted.
//
// The keys come back so a deployment can log what its subject access requests
// will actually cover. That is the question this package exists for: an export
// that is well-formed and contains none of the subject's comments is
// indistinguishable from a correct one, and startup is the last moment anybody
// can still notice.
//
// It registers all eleven or none of them. Every adapter is built before any is
// registered, so a nil store in the last field does not leave a registry holding
// ten of eleven domains — and the keys are checked against what the registry
// already holds before the first one goes in, so a key the caller registered
// already does not either. Both matter for the same reason the package exists:
// dataprivacy.Registry has no unregister, and a half-registered one is exactly
// the state that produces a well-formed export missing a domain. A caller that
// logs this error and carries on gets a registry it has not touched rather than
// one holding whichever adapters happened to be ahead of the collision.
func Register(registry *dataprivacy.Registry, adapters *Adapters) ([]string, error) {
	if registry == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy registry")
	}

	if adapters == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil privacy adapters")
	}

	built, err := adapters.build()
	if err != nil {
		return nil, err
	}

	if err = checkUnclaimed(registry, built); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(built))

	for i := range built {
		entry := &built[i]

		if entry.collector != nil {
			if err = registry.RegisterCollector(entry.key, entry.collector); err != nil {
				return nil, platformerrors.Wrapf(err, "registering the %s privacy collector", entry.key)
			}
		}

		if entry.eraser != nil {
			if err = registry.RegisterEraser(entry.key, entry.eraser); err != nil {
				return nil, platformerrors.Wrapf(err, "registering the %s privacy eraser", entry.key)
			}
		}

		keys = append(keys, entry.key)
	}

	slices.Sort(keys)

	return keys, nil
}

// checkUnclaimed refuses the whole set if any of it is already spoken for.
//
// It is what makes [Register] all-or-nothing rather than all-or-whatever-was-
// ahead-of-the-collision. dataprivacy.Registry refuses a re-registration and
// has no unregister, so without this the caller's recourse to a collision on
// the seventh adapter is to throw away a registry the first six are in — and
// the failure mode this package exists to close is a registry that is missing a
// domain and cannot say so.
//
// The two namespaces are checked separately because they are separate: a domain
// may ship a collector and no eraser, so a key taken in one is not taken in the
// other. What is not checked is the built set against itself — each key is a
// distinct package constant, and the roster test is what fails if two packages
// ever ship the same one.
//
// After this, the registrations below can still return an error in principle
// and are still checked for one, but nothing reachable produces it: the keys
// are constants that pass validation and the halves are non-nil by
// construction.
func checkUnclaimed(registry *dataprivacy.Registry, built []registration) error {
	collectors := registry.CollectorKeys()
	erasers := registry.EraserKeys()

	for i := range built {
		entry := &built[i]

		if entry.collector != nil && slices.Contains(collectors, entry.key) {
			return platformerrors.Wrapf(dataprivacy.ErrDuplicateKey,
				"dataprivacy collector %q is already registered", entry.key)
		}

		if entry.eraser != nil && slices.Contains(erasers, entry.key) {
			return platformerrors.Wrapf(dataprivacy.ErrDuplicateKey,
				"dataprivacy eraser %q is already registered", entry.key)
		}
	}

	return nil
}

// registration is one key and the halves that go in under it. A domain may ship
// a collector, an eraser, or both, and the asymmetry is the normal case rather
// than a misconfiguration — see dataprivacy.Registry.RegisterEraser.
type registration struct {
	collector dataprivacy.Collector
	eraser    dataprivacy.Eraser
	key       string
}

// build constructs every adapter named, and nothing else.
//
// It is separate from the registration walk so that the two phases cannot
// interleave: a constructor that refuses halfway through must leave the
// caller's registry untouched, and the only way to promise that is to finish
// building before registering anything.
//
// It is written out one domain at a time rather than driven off a table. A
// table would have to erase what actually differs — two seams in notifications,
// no eraser in billing, no store at all in auditerasure — into a shape general
// enough to hold all three, and the reader who opens this file has come for
// exactly those differences.
func (a *Adapters) build() ([]registration, error) {
	var built []registration
	if a.Comments != nil {
		collector, eraser, err := a.Comments.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", commentsprivacy.DefaultKey)
		}

		built = append(built, registration{key: commentsprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.IssueReports != nil {
		collector, eraser, err := a.IssueReports.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", issuereportsprivacy.DefaultKey)
		}

		built = append(built, registration{key: issuereportsprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.Settings != nil {
		collector, eraser, err := a.Settings.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", settingsprivacy.DefaultKey)
		}

		built = append(built, registration{key: settingsprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.Waitlists != nil {
		collector, eraser, err := a.Waitlists.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", waitlistsprivacy.DefaultKey)
		}

		built = append(built, registration{key: waitlistsprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.MediaRegistry != nil {
		collector, eraser, err := a.MediaRegistry.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", mediaregistryprivacy.DefaultKey)
		}

		built = append(built, registration{key: mediaregistryprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.OAuth2Clients != nil {
		collector, eraser, err := a.OAuth2Clients.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", oauth2clientsprivacy.DefaultKey)
		}

		built = append(built, registration{key: oauth2clientsprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.PasswordReset != nil {
		collector, eraser, err := a.PasswordReset.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", passwordresetprivacy.DefaultKey)
		}

		built = append(built, registration{key: passwordresetprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.Identity != nil {
		collector, eraser, err := a.Identity.build(a.Reader)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", identityprivacy.DefaultKey)
		}

		built = append(built, registration{key: identityprivacy.DefaultKey, collector: collector, eraser: eraser})
	}
	if a.Notifications != nil {
		// Two seams, two keys, and either may be absent: a deployment with an
		// inbox and no device registry gets the inbox pair only.
		if a.Notifications.Inbox != nil {
			collector, eraser, err := a.Notifications.buildInbox(a.Reader)
			if err != nil {
				return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", notificationsprivacy.DefaultInboxKey)
			}

			built = append(built, registration{key: notificationsprivacy.DefaultInboxKey, collector: collector, eraser: eraser})
		}

		if a.Notifications.Registry != nil {
			collector, eraser, err := a.Notifications.buildDevices(a.Reader)
			if err != nil {
				return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", notificationsprivacy.DefaultDeviceKey)
			}

			built = append(built, registration{key: notificationsprivacy.DefaultDeviceKey, collector: collector, eraser: eraser})
		}
	}

	if a.Billing != nil {
		// The collector alone. billing/privacy ships no Eraser, and the absent
		// field above is the other half of saying so.
		collector, err := billingprivacy.NewCollector(a.Billing.Store, a.Reader, a.Billing.Resolve)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", billingprivacy.DefaultKey)
		}

		built = append(built, registration{key: billingprivacy.DefaultKey, collector: collector})
	}

	if a.AuditErasure != nil {
		// The eraser alone, and the one adapter built from a dialect rather
		// than a store.
		eraser, err := auditerasure.New(a.AuditErasure.Dialect, a.AuditErasure.Options...)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building the %s privacy adapter", auditerasure.DefaultKey)
		}

		built = append(built, registration{key: auditerasure.DefaultKey, eraser: eraser})
	}

	return built, nil
}

func (c *CommentsAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := commentsprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := commentsprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *IssueReportsAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := issuereportsprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := issuereportsprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *SettingsAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := settingsprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := settingsprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *WaitlistsAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := waitlistsprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := waitlistsprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *MediaRegistryAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := mediaregistryprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := mediaregistryprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *OAuth2ClientsAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := oauth2clientsprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := oauth2clientsprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *PasswordResetAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := passwordresetprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := passwordresetprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *IdentityAdapter) build(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := identityprivacy.NewCollector(c.Store, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := identityprivacy.NewEraser(c.Store, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *NotificationsAdapter) buildInbox(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := notificationsprivacy.NewInboxCollector(c.Inbox, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := notificationsprivacy.NewInboxEraser(c.Inbox, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}

func (c *NotificationsAdapter) buildDevices(
	reader database.SQLQueryExecutor,
) (dataprivacy.Collector, dataprivacy.Eraser, error) {
	collector, err := notificationsprivacy.NewDeviceCollector(c.Registry, reader, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	eraser, err := notificationsprivacy.NewDeviceEraser(c.Registry, c.Resolve)
	if err != nil {
		return nil, nil, err
	}

	return collector, eraser, nil
}
