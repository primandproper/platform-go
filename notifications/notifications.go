package notifications

import (
	"strings"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The keys this package attaches to spans and log lines. Declared once so a
// trace and a log line name the same fact the same way.
const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "notifications"

	scopeKey          = serviceName + ".scope"
	principalKey      = serviceName + ".principal"
	notificationIDKey = serviceName + ".notification_id"
	deviceIDKey       = serviceName + ".device_id"
	platformKey       = serviceName + ".platform"
	countKey          = serviceName + ".count"
)

// PrincipalAttributeKey is the metric and span attribute a caller labels its own
// instruments with when the thing being measured is about one recipient. It is
// exported so a consumer's attributes agree with this package's rather than
// merely resembling them.
const PrincipalAttributeKey = principalKey

// Platform is which push provider a device token is addressed through.
//
// It is a type rather than a string because it is half of a device's natural key
// and because the set is closed by what notifications/mobile can actually route
// to: a token stored under a platform no sender serves is a row that can never
// be delivered to and will never be pruned, since the feedback that prunes rows
// comes from the provider that rejected them.
type Platform string

const (
	// PlatformIOS is APNs. notifications/mobile spells it "ios".
	PlatformIOS Platform = "ios"
	// PlatformAndroid is FCM. notifications/mobile spells it "android".
	PlatformAndroid Platform = "android"
)

// ParsePlatform reads a platform off the wire — a request body, a provider
// callback, notifications/mobile's own platform argument — normalizing case and
// surrounding space, and reports whether it names one this package serves.
//
// It normalizes rather than merely comparing because the string reaching it was
// typed by a mobile client. "iOS" and "ios" are one platform, and a registry
// that stored both would hold two rows for one handset and prune neither when
// the provider rejected the token.
func ParsePlatform(s string) (Platform, bool) {
	p := Platform(strings.ToLower(strings.TrimSpace(s)))

	return p, p.Valid()
}

// Valid reports whether p is a platform this package serves.
func (p Platform) Valid() bool {
	switch p {
	case PlatformIOS, PlatformAndroid:
		return true
	default:
		return false
	}
}

// String renders the platform as the senders spell it.
func (p Platform) String() string { return string(p) }

// Notification is one row of somebody's inbox: a thing they were told, and
// whether they have read it.
//
// It is deliberately app-independent. Topic is the consumer's own category and
// nothing here validates it; Title, Body and Link are what a client renders.
// What this package owns is the lifecycle around them — created, read, archived
// — which is the half that is the same in every application and the half that
// every application otherwise writes again.
type Notification struct {
	// CreatedAt is when the notification was filed, assigned by the database.
	CreatedAt time.Time `json:"createdAt"`
	// LastUpdatedAt is when the row last changed, which for this table means
	// when it was read.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// ArchivedAt is when the notification was dismissed, and nil while it is
	// still in the inbox.
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	// ReadAt is when the recipient read it, and nil until they do.
	//
	// A timestamp rather than a boolean, because "when" answers "whether" and a
	// boolean does not answer "when" — which is what a digest that skips what
	// somebody has already seen, and a re-notify that does not, are both about.
	ReadAt *time.Time `json:"readAt,omitempty"`
	// ID identifies the notification.
	ID string `json:"id"`
	// Principal is whose inbox it is in — a user id in every deployment this
	// module has, but a string here, because notifications does not own the
	// directory.
	Principal string `json:"principal"`
	// Topic is the application's own category: order.shipped, invite.received.
	// A client groups, mutes and routes by it.
	Topic string `json:"topic"`
	// Title is the headline.
	Title string `json:"title"`
	// Body is the rest, and may be empty.
	Body string `json:"body,omitempty"`
	// Link is where reading it takes somebody, as the application spells it.
	// Empty is a notification with nowhere to go.
	Link string `json:"link,omitempty"`
	// Scope is whose data this is.
	Scope tenancy.Scope `json:"scope"`
}

// Read reports whether the recipient has read the notification.
func (n *Notification) Read() bool { return n != nil && n.ReadAt != nil }

// Device is one handset a push can be addressed to.
//
// Token is the provider's, minted by the device and meaningful only to the
// provider that issued it, which is why (Platform, Token) is the row's natural
// key rather than (Principal, Platform). A handset that one person signs out of
// and another signs into presents the same token under a new principal, and the
// registry has to move the row rather than keep both — the alternative is the
// previous owner's notifications arriving on somebody else's lock screen.
type Device struct {
	// CreatedAt is when the token was first registered, assigned by the
	// database. A re-registration keeps it: the handset is the same handset.
	CreatedAt time.Time `json:"createdAt"`
	// LastSeenAt is when the device last announced itself. It is what a
	// stale-token sweep reads, and it is not a last-modified stamp: nothing
	// else about the row is mutable except who owns it.
	LastSeenAt time.Time `json:"lastSeenAt"`
	// ID identifies the registration. It survives a re-registration, so a
	// revocation issued against a device somebody is still holding finds it.
	ID string `json:"id"`
	// Principal is whose handset it is.
	Principal string `json:"principal"`
	// Token is the provider's device token.
	Token string `json:"token"`
	// Platform is which provider the token is addressed through.
	Platform Platform `json:"platform"`
	// Scope is whose data this is.
	Scope tenancy.Scope `json:"scope"`
}
