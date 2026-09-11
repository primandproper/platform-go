package identity

import (
	"net/mail"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// The keys this package attaches to spans and log lines. Declared once so a
// trace and a log line name the same fact the same way.
const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "identity"

	scopeKey        = serviceName + ".scope"
	userIDKey       = serviceName + ".user_id"
	usernameKey     = serviceName + ".username"
	accountIDKey    = serviceName + ".account_id"
	invitationIDKey = serviceName + ".invitation_id"
	countKey        = serviceName + ".count"

	// operationKey labels an instrument with the operation it was recorded in:
	// the store's unmatched-write counter with the write that matched no row,
	// the service's request, error and latency instruments with the operation
	// they measured. One name because it is one fact, on instruments that are
	// already told apart by their own.
	operationKey = serviceName + ".operation"
)

// UserAttributeKey is the metric and span attribute a caller labels its own
// instruments with when the thing being measured is about one user. It is
// exported so a consumer's attributes agree with this package's rather than
// merely resembling them.
const UserAttributeKey = userIDKey

// AccountAttributeKey is UserAttributeKey for accounts.
const AccountAttributeKey = accountIDKey

// timeZoneRule validates an IANA time zone name by loading it.
//
// Loading rather than pattern-matching, because the only useful definition of a
// valid zone name is one the runtime can turn into a *time.Location — and a
// name that looks right but does not load ("America/Chicagoo") renders every
// date on the account wrong, forever, without anything saying so. Failing the
// write is how that stays a typo instead of becoming data.
//
// It has a runtime cost worth knowing about: any zone but UTC needs the
// zoneinfo database, which scratch and distroless images do not carry, and
// without it this rejects names that are perfectly good elsewhere. That is the
// same trade the jobs package documents at length for cron schedules, and the
// same fix applies — `import _ "time/tzdata"` in the binary's main package
// embeds it.
//
// "Local" is refused rather than loaded. Go builds time.Local from the
// process's TZ environment variable, so a stored "Local" means whatever the
// reader's host happens to think — which makes two replicas of one service
// disagree about when an account's day starts, and makes a value written on a
// laptop mean something else in production.
var timeZoneRule = validation.By(func(value any) error {
	name, ok := value.(string)
	if !ok || name == "" {
		// Empty is "not stated", which is a legitimate answer — see
		// Account.Location for what reads it.
		return nil
	}

	if name == "Local" {
		return platformerrors.Wrap(ErrInvalidTimeZone, `"Local" names the reader's host rather than a zone`)
	}

	if _, err := time.LoadLocation(name); err != nil {
		return platformerrors.Wrapf(ErrInvalidTimeZone, "%q: %v", name, err)
	}

	return nil
})

// emailAddressRule is the validation both a User and an Invitation apply to an
// address, written once because it is a rule that can be got wrong twice — and
// the copy that drifts is the one that lets an unreachable address into a
// directory where it is also a unique key.
//
// It parses with net/mail rather than matching a pattern. A regular expression
// for RFC 5322 is either wrong or unreadable, and the standard library already
// holds the grammar that the email package formats addresses against — so an
// address this accepts is one email.FormatAddress can render.
//
// It deliberately does not check that the domain resolves. That is a network
// call on a validation path, it is wrong the moment DNS changes, and the only
// proof an address is reachable is having sent to it — which is what the
// verification token exists for.
var emailAddressRule = validation.By(func(value any) error {
	address, ok := value.(string)
	if !ok || address == "" {
		// Emptiness is validation.Required's to report, so that a missing
		// address and a malformed one do not both come back as "invalid".
		return nil
	}

	parsed, err := mail.ParseAddress(address)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidEmailAddress, "%q: %v", address, err)
	}

	// ParseAddress accepts a display name — `Ada <ada@example.com>` — and this
	// column holds an address alone. Accepting the longer form would store a
	// value that is a unique key, is compared against what a sign-in form
	// submits, and does not equal it.
	if parsed.Address != address {
		return platformerrors.Wrapf(ErrInvalidEmailAddress, "%q is not a bare address", address)
	}

	return nil
})
