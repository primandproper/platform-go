package devices

import (
	"context"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// The attribute keys [Device.Attributes] renders, and therefore the keys a
// client reads off each login signin/grpc's listing RPCs return when a server is
// built with [NewAnnotator]. They are exported so a client can name them rather
// than copy them.
const (
	// AttributeIPAddress names the address the login was last renewed from.
	AttributeIPAddress = "ip_address"
	// AttributeUserAgent names the user agent the login was last renewed by.
	AttributeUserAgent = "user_agent"
	// AttributeDeviceName names what the client calls the device holding the
	// login, when it said.
	AttributeDeviceName = "device_name"
)

// MaxFieldLength is the most bytes any recorded value keeps. The values arrive
// from clients, and a user agent a client chose to make a megabyte long is not
// one to store a megabyte of.
//
// It is also the width the MySQL schema declares, so a value the store let
// through is one MySQL stores whole rather than cutting silently.
const MaxFieldLength = 512

// userAgentMetadataKey is the metadata key gRPC carries a client's user agent
// under.
const userAgentMetadataKey = "user-agent"

// Origin is where a request came from, as the deployment reads it: the three
// things a device list shows. Any of them may be empty.
type Origin struct {
	_ struct{} `json:"-"`

	// IPAddress is the address the request came from.
	IPAddress string `json:"ipAddress,omitempty"`

	// UserAgent is the user agent the request came from.
	UserAgent string `json:"userAgent,omitempty"`

	// DeviceName is what the client calls the device it is running on.
	DeviceName string `json:"deviceName,omitempty"`
}

// Extractor reads the [Origin] of the request a sign-in is being minted for.
//
// It is the one decision this package leaves to the consumer, because it is the
// one that depends on the deployment: which headers its edge writes, which
// proxies it trusts, and which clients send a device name. A wrong answer here
// is a client choosing the address it is listed under. See [PeerExtractor] for
// the conservative default.
//
// It is called inside the sign-in's transaction, so it should read the context
// and nothing slower.
type Extractor func(ctx context.Context) Origin

// PeerExtractor reads the connection's own peer address and the user-agent
// gRPC metadata, and nothing else.
//
// It trusts no forwarded header, so behind a proxy it records the proxy's
// address — which is the honest answer when nothing says which proxies are
// trusted. A deployment that knows its edge writes the client's address into a
// header writes an Extractor that reads it. It reads no device name, since no
// standard header carries one.
func PeerExtractor(ctx context.Context) Origin {
	var origin Origin

	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		origin.IPAddress = p.Addr.String()
		if host, _, err := net.SplitHostPort(origin.IPAddress); err == nil {
			origin.IPAddress = host
		}
	}

	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, value := range md.Get(userAgentMetadataKey) {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				origin.UserAgent = trimmed

				break
			}
		}
	}

	return origin
}

// Sighting is one mint of a login: which login, whose, where its request came
// from, and how long it can now live. It is what [Store.Record] writes.
type Sighting struct {
	_ struct{} `json:"-"`

	// ExpiresAt is the latest the login could still be alive. The sweep deletes
	// the row once it passes.
	ExpiresAt time.Time `json:"expiresAt"`

	// Origin is where the request that minted the login's token came from.
	Origin Origin `json:"origin"`

	// FamilyID is which login: signin's family identifier.
	FamilyID string `json:"familyID"`

	// UserID is whose login it is. It is opaque to the store — this package
	// reads no user table — so an application whose users live outside identity
	// uses it unchanged.
	UserID string `json:"userID"`
}

// Device is where one login was last renewed from: the row the store keeps.
type Device struct {
	_ struct{} `json:"-"`

	// FirstSeenAt is when the login was first recorded.
	FirstSeenAt time.Time `json:"firstSeenAt"`

	// LastSeenAt is when the login was last renewed.
	LastSeenAt time.Time `json:"lastSeenAt"`

	// ExpiresAt is the latest the login could still be alive.
	ExpiresAt time.Time `json:"expiresAt"`

	// FamilyID is which login.
	FamilyID string `json:"familyID"`

	// UserID is whose login it is.
	UserID string `json:"belongsToUser"`

	// IPAddress is the address the login was last renewed from.
	IPAddress string `json:"ipAddress,omitempty"`

	// UserAgent is the user agent the login was last renewed by.
	UserAgent string `json:"userAgent,omitempty"`

	// DeviceName is what the client calls the device holding the login.
	DeviceName string `json:"deviceName,omitempty"`

	// Scope is whose directory the login is in.
	Scope tenancy.Scope `json:"scope"`
}

// Attributes renders a device as a listed login's attributes, under the
// Attribute keys, naming only what is known. A device nothing was read for
// renders an empty map.
func (d *Device) Attributes() map[string]string {
	attributes := map[string]string{}

	if d == nil {
		return attributes
	}

	for key, value := range map[string]string{
		AttributeIPAddress:  d.IPAddress,
		AttributeUserAgent:  d.UserAgent,
		AttributeDeviceName: d.DeviceName,
	} {
		if value != "" {
			attributes[key] = value
		}
	}

	return attributes
}

// Store is everything this package's table answers.
type Store interface {
	// Record writes where a login was renewed from, on the caller's
	// transaction: a new row for a login's first mint, and the same row renewed
	// by every one after it. The values on the sighting's Origin are bounded to
	// MaxFieldLength first.
	Record(ctx context.Context, tx database.Tx, scope tenancy.Scope, sighting *Sighting) error

	// ListForFamilies answers with what was recorded for one person's logins,
	// among familyIDs. A login with nothing recorded, or one that is not that
	// person's, is absent from the answer. An empty familyIDs answers nothing
	// without a query.
	ListForFamilies(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
		familyIDs []string,
	) ([]*Device, error)

	// ListForUser answers with every row one person has.
	//
	// It is unpaged, and the bound is structural: a row is one login, deleted
	// when the login is ended ([Hooks.AfterRevokeSignIns]) and swept once it
	// lapses, so what one person has is their live logins rather than their
	// history.
	ListForUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) ([]*Device, error)

	// DeleteForFamilies removes the rows of one person's logins among
	// familyIDs, and reports how many it removed. A login with no row, or one
	// that is not that person's, is left alone and is not an error. An empty
	// familyIDs removes nothing without a query.
	DeleteForFamilies(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		familyIDs []string,
	) (int64, error)

	// DeleteForUser removes every row one person has, and reports how many it
	// removed. Zero is not an error: somebody who never signed in has none.
	DeleteForUser(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
	) (int64, error)
}

// bound is a value as it is stored: trimmed, made valid UTF-8, and cut to
// MaxFieldLength bytes without splitting a character.
//
// Valid UTF-8 is not tidiness. Postgres refuses a text value that is not, so an
// unbounded byte string from a client would be a sign-in that fails on the
// device write; and a cut through the middle of a character would make a valid
// value invalid on the way in.
func bound(value string) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	if len(value) <= MaxFieldLength {
		return value
	}

	cut := MaxFieldLength
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}

	return value[:cut]
}
