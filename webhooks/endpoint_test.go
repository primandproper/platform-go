package webhooks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks/internal/webhooksdb"

	"github.com/primandproper/primitives-go/v2/cryptography/requestsigning"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The event types the unit tests publish, declared the way this package asks an
// application to declare its own: as EventType constants, which is what makes
// the set of them something a tool can find.
const (
	orderCreated EventType = "order.created"
	orderUpdated EventType = "order.updated"

	// Declared but deliberately outside testCatalog, for the rejection paths.
	orderDeleted  EventType = "order.deleted"
	orderExploded EventType = "order.exploded"
)

// testCatalog is the event catalog the unit tests register against.
var testCatalog = Catalog{
	orderCreated: {Description: "an order was created"},
	orderUpdated: {Description: "an order was updated"},
}

func TestCheckEndpointURL(T *testing.T) {
	T.Parallel()

	// Literal IPs throughout, so nothing here depends on a DNS lookup.
	T.Run("accepts a public https URL", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, CheckEndpointURL(t.Context(), "https://93.184.216.34/hooks"))
	})

	T.Run("rejects non-https schemes", func(t *testing.T) {
		t.Parallel()

		for _, rawURL := range []string{
			"http://93.184.216.34/hooks",
			"ftp://93.184.216.34/hooks",
			"file:///etc/passwd",
			"gopher://93.184.216.34/",
		} {
			test.ErrorIs(t, CheckEndpointURL(t.Context(), rawURL), ErrInvalidEndpointURL)
		}
	})

	T.Run("rejects a relative or hostless URL", func(t *testing.T) {
		t.Parallel()

		for _, rawURL := range []string{"/hooks", "https://", "://nope"} {
			test.Error(t, CheckEndpointURL(t.Context(), rawURL))
		}
	})

	T.Run("rejects credentials in the URL", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, CheckEndpointURL(t.Context(), "https://user:pass@93.184.216.34/hooks"), ErrInvalidEndpointURL)
	})

	// The SSRF cases. Each of these is a real target someone has been hit with.
	T.Run("rejects non-routable hosts", func(t *testing.T) {
		t.Parallel()

		for name, rawURL := range map[string]string{
			"loopback v4":             "https://127.0.0.1/hooks",
			"loopback v6":             "https://[::1]/hooks",
			"cloud instance metadata": "https://169.254.169.254/latest/meta-data/",
			"link-local v6":           "https://[fe80::1]/hooks",
			"rfc1918 ten":             "https://10.0.0.5/hooks",
			"rfc1918 172":             "https://172.16.3.4/hooks",
			"rfc1918 192.168":         "https://192.168.1.1/hooks",
			"unique local v6":         "https://[fd00::1]/hooks",
			"unspecified":             "https://0.0.0.0/hooks",
			"multicast":               "https://224.0.0.1/hooks",
			"carrier-grade nat":       "https://100.64.0.1/hooks",
			"benchmarking":            "https://198.18.0.1/hooks",
			"protocol assignments":    "https://192.0.0.171/hooks",
			"nat64 over private":      "https://[64:ff9b::c0a8:101]/hooks",
			"6to4 over private":       "https://[2002:c0a8:101::1]/hooks",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				test.ErrorIs(t, CheckEndpointURL(t.Context(), rawURL), ErrDisallowedEndpointHost)
			})
		}
	})

	// A port does not change the verdict either way.
	T.Run("honors the host regardless of port", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, CheckEndpointURL(t.Context(), "https://93.184.216.34:8443/hooks"))
		test.ErrorIs(t, CheckEndpointURL(t.Context(), "https://127.0.0.1:8443/hooks"), ErrDisallowedEndpointHost)
	})
}

func TestSubscribeTo(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		subscriptions := SubscribeTo(orderCreated, orderUpdated)

		must.SliceLen(t, 2, subscriptions)
		test.EqOp(t, orderCreated, subscriptions[0].EventType)
		test.EqOp(t, orderUpdated, subscriptions[1].EventType)

		// No IDs: the store mints them, because the pair is what identifies a
		// subscription and a caller has nothing to name yet.
		test.EqOp(t, "", subscriptions[0].ID)
	})

	// Not filtered, deliberately: the store reconciles by event type, so a repeat
	// is one row either way, and dropping it silently would hide a caller whose
	// list-building had a bug in it.
	T.Run("keeps a repeated event type", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 2, SubscribeTo(orderCreated, orderCreated))
	})

	T.Run("no events", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, SubscribeTo())
	})
}

func TestEndpoint_EventTypes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{Subscriptions: SubscribeTo(orderCreated, orderUpdated)}

		test.Eq(t, []EventType{orderCreated, orderUpdated}, endpoint.EventTypes())
	})

	// The derivation is what keeps the flat form from being a second copy of the
	// set, so it has to agree with what fan-out will do — and fan-out skips an
	// archived subscription.
	T.Run("skips archived subscriptions", func(t *testing.T) {
		t.Parallel()

		archivedAt := time.Now().UTC()

		endpoint := &Endpoint{Subscriptions: []Subscription{
			{EventType: orderCreated, ArchivedAt: &archivedAt},
			{EventType: orderUpdated},
		}}

		test.Eq(t, []EventType{orderUpdated}, endpoint.EventTypes())
	})

	T.Run("nil endpoint", func(t *testing.T) {
		t.Parallel()

		var endpoint *Endpoint
		test.Nil(t, endpoint.EventTypes())
	})
}

func TestArchived(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		at := time.Now().UTC()

		test.False(t, (&Endpoint{}).Archived())
		test.True(t, (&Endpoint{ArchivedAt: &at}).Archived())

		test.False(t, (&Subscription{}).Archived())
		test.True(t, (&Subscription{ArchivedAt: &at}).Archived())
	})

	T.Run("nil receivers", func(t *testing.T) {
		t.Parallel()

		var (
			endpoint     *Endpoint
			subscription *Subscription
		)

		test.False(t, endpoint.Archived())
		test.False(t, subscription.Archived())
	})
}

func TestEndpoint_Validate(T *testing.T) {
	T.Parallel()

	valid := func() *Endpoint {
		return &Endpoint{
			ID:            "endpoint-1",
			Scope:         testScope,
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		}
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, valid().Validate(t.Context(), testCatalog, nil))
	})

	T.Run("nil endpoint", func(t *testing.T) {
		t.Parallel()

		var endpoint *Endpoint
		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNilEndpoint)
	})

	// An endpoint that does not say whose it is would be registered by an
	// application with tenants by accident, and the account it was meant for
	// would never see a delivery.
	T.Run("without a scope", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Scope = tenancy.Scope{}

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNoScope)
	})

	T.Run("the global scope is a scope", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Scope = tenancy.Global()

		test.NoError(t, endpoint.Validate(t.Context(), testCatalog, nil))
	})

	T.Run("without a signing secret", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Secret = Secret{}

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNoSigningSecret)
	})

	T.Run("subscribing to nothing", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Subscriptions = nil

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNoEvents)
	})

	// An endpoint whose every subscription has been archived is a subscriber
	// that will never receive anything, which is the same mistake as naming none.
	T.Run("subscribing only to archived event types", func(t *testing.T) {
		t.Parallel()

		archivedAt := time.Now().UTC()

		endpoint := valid()
		endpoint.Subscriptions = []Subscription{{EventType: orderCreated, ArchivedAt: &archivedAt}}

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNoEvents)
	})

	T.Run("subscribing to an empty event type", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Subscriptions = SubscribeTo("")

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrEmptyEventType)
	})

	// The typo case the catalog exists to catch. Without this check the endpoint
	// registers cleanly and then never fires.
	T.Run("subscribing to an unknown event", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Subscriptions = SubscribeTo(orderCreated, "odrer.updated")

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrUnknownEventType)
	})

	T.Run("setting a reserved header", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{requestsigning.SignatureHeader, "content-type", "X-PLATFORM-TIMESTAMP"} {
			endpoint := valid()
			endpoint.Headers = map[string]string{name: "attacker-chosen"}

			test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrReservedHeader)
		}
	})

	T.Run("permits ordinary static headers", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Headers = map[string]string{"X-Tenant": "acme"}

		test.NoError(t, endpoint.Validate(t.Context(), testCatalog, nil))
	})

	T.Run("honors a replacement URL checker", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.URL = "http://127.0.0.1:9000/hooks"

		must.Error(t, endpoint.Validate(t.Context(), testCatalog, nil))
		test.NoError(t, endpoint.Validate(t.Context(), testCatalog, func(context.Context, string) error { return nil }))
	})
}

func TestEndpoint_applyHeaders(T *testing.T) {
	T.Parallel()

	T.Run("writes static headers", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{Headers: map[string]string{"X-Tenant": "acme"}}

		header := http.Header{}
		endpoint.applyHeaders(header)

		test.EqOp(t, "acme", header.Get("X-Tenant"))
	})

	// Registration rejects these, but a Store implementation this package did
	// not validate can hand one back — and a subscriber that could set its own
	// signature header would be authenticating against a value it chose.
	T.Run("refuses to overwrite a reserved header", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{Headers: map[string]string{
			"x-platform-signature": "forged",
			"Content-Type":         "text/plain",
			"X-Tenant":             "acme",
		}}

		header := http.Header{}
		endpoint.applyHeaders(header)

		test.EqOp(t, "", header.Get(requestsigning.SignatureHeader))
		test.EqOp(t, "", header.Get("Content-Type"))
		test.EqOp(t, "acme", header.Get("X-Tenant"))
	})
}

func TestEndpoint_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{}
		endpoint.EnsureDefaults()

		test.EqOp(t, DefaultContentType, endpoint.ContentType)
	})

	T.Run("leaves an explicit content type alone", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{ContentType: "application/cloudevents+json"}
		endpoint.EnsureDefaults()

		test.EqOp(t, "application/cloudevents+json", endpoint.ContentType)
	})

	T.Run("nil endpoint does not panic", func(t *testing.T) {
		t.Parallel()

		var endpoint *Endpoint
		endpoint.EnsureDefaults()
	})
}

func TestCatalog(T *testing.T) {
	T.Parallel()

	T.Run("Known", func(t *testing.T) {
		t.Parallel()

		test.True(t, testCatalog.Known(orderCreated))
		test.False(t, testCatalog.Known(orderDeleted))
		test.False(t, Catalog(nil).Known("order.created"))
	})

	T.Run("EventTypes is sorted", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []EventType{orderCreated, orderUpdated}, testCatalog.EventTypes())
	})

	T.Run("EventTypes of an empty catalog", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, Catalog{}.EventTypes())
	})
}

func TestEventType(T *testing.T) {
	T.Parallel()

	T.Run("String", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "order.created", orderCreated.String())
		test.EqOp(t, "", EventType("").String())
	})

	// The type is a compile-time distinction and nothing more: what crosses the
	// wire is the same string it always was. A subscriber, a stored API response,
	// and a client generated from one cannot tell this change happened, which is
	// what makes it safe to make in a major that is already breaking callers.
	T.Run("marshals as a plain string", func(t *testing.T) {
		t.Parallel()

		endpoint, err := json.Marshal(&Endpoint{Subscriptions: SubscribeTo(orderCreated, orderUpdated)})
		must.NoError(t, err)
		test.StrContains(t, string(endpoint), `"eventType":"order.created"`)
		test.StrContains(t, string(endpoint), `"eventType":"order.updated"`)

		delivery, err := json.Marshal(&Delivery{EventType: orderCreated})
		must.NoError(t, err)
		test.StrContains(t, string(delivery), `"eventType":"order.created"`)

		claimed, err := json.Marshal(&ClaimedDispatch{EventType: orderCreated})
		must.NoError(t, err)
		test.StrContains(t, string(claimed), `"eventType":"order.created"`)

		catalog, err := json.Marshal(testCatalog)
		must.NoError(t, err)
		test.StrContains(t, string(catalog), `"order.created":{"description":"an order was created"}`)
	})

	T.Run("unmarshals from a plain string", func(t *testing.T) {
		t.Parallel()

		var endpoint Endpoint
		must.NoError(t, json.Unmarshal([]byte(`{"subscriptions":[{"eventType":"order.created"}]}`), &endpoint))
		test.Eq(t, []EventType{orderCreated}, endpoint.EventTypes())

		var delivery Delivery
		must.NoError(t, json.Unmarshal([]byte(`{"eventType":"order.created"}`), &delivery))
		test.EqOp(t, orderCreated, delivery.EventType)

		var catalog Catalog
		must.NoError(t, json.Unmarshal([]byte(`{"order.created":{"description":"d"}}`), &catalog))
		test.True(t, catalog.Known(orderCreated))
	})
}

// The event type reaches the driver as a string rather than as an EventType.
//
// Both work against most drivers — a defined string type goes through their
// reflective fallback — but "most" is a property of whichever driver a consumer
// wired up, and a Store implementation is allowed to be one this package has
// never seen. So the conversion happens at this package's own boundary, and
// since the port it is the compiler that requires it: every generated params
// struct carrying an event type declares the field as a plain string, so a
// store assigning an EventType to one does not build.
//
// What this pins is that those fields have not quietly become defined types —
// which would make the conversions at the call sites redundant, and the next
// person to add a statement would leave one out without anything saying so.
func TestQueries_BindEventTypesAsStrings(T *testing.T) {
	T.Parallel()

	fields := map[string]any{
		"UpsertSubscriptionParams":    webhooksdb.UpsertSubscriptionParams{EventType: orderCreated.String()}.EventType,
		"GetSubscriptionByPairParams": webhooksdb.GetSubscriptionByPairParams{EventType: orderCreated.String()}.EventType,
		"ListEndpointsForEventParams": webhooksdb.ListEndpointsForEventParams{EventType: orderCreated.String()}.EventType,
		"InsertDeliveryParams":        webhooksdb.InsertDeliveryParams{EventType: orderCreated.String()}.EventType,
	}

	for name, bound := range fields {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, reflect.TypeFor[string](), reflect.TypeOf(bound), test.Sprintf("%s.EventType", name))
			test.EqOp(t, "order.created", bound)
		})
	}
}

func TestAttempt_Succeeded(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.True(t, (&Attempt{StatusCode: 200}).Succeeded())
		test.True(t, (&Attempt{StatusCode: 204}).Succeeded())
		test.False(t, (&Attempt{StatusCode: 500}).Succeeded())
		test.False(t, (&Attempt{StatusCode: 200, Error: "boom"}).Succeeded())
	})

	// A redirect is not success. The client refuses to follow it, so treating it
	// as delivered would silently drop the payload.
	T.Run("a redirect is not success", func(t *testing.T) {
		t.Parallel()

		test.False(t, (&Attempt{StatusCode: 302}).Succeeded())
	})
}

func TestTerminalStatus(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		// The subscriber understood and refused; retrying changes nothing.
		test.True(t, terminalStatus(http.StatusBadRequest))
		test.True(t, terminalStatus(http.StatusUnauthorized))
		test.True(t, terminalStatus(http.StatusNotFound))
		test.True(t, terminalStatus(http.StatusGone))

		// Both of these explicitly invite a later attempt.
		test.False(t, terminalStatus(http.StatusRequestTimeout))
		test.False(t, terminalStatus(http.StatusTooManyRequests))

		// Server-side failures are transient until proven otherwise.
		test.False(t, terminalStatus(http.StatusInternalServerError))
		test.False(t, terminalStatus(http.StatusBadGateway))
		test.False(t, terminalStatus(http.StatusServiceUnavailable))

		test.False(t, terminalStatus(http.StatusOK))
	})
}

// The hostname path, which every other case in this file skips by using literal
// IPs. It is the branch that actually runs at delivery time for a real
// subscriber, so leaving it unexercised would mean the resolver loop was never
// executed by any test.
func TestCheckEndpointURL_resolution(T *testing.T) {
	T.Parallel()

	// localhost resolves without touching the network and lands on loopback,
	// which is exactly what the guard must refuse.
	T.Run("rejects a name that resolves to loopback", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, CheckEndpointURL(t.Context(), "https://localhost/hooks"), ErrDisallowedEndpointHost)
	})

	// A cancelled context fails the lookup deterministically, without depending
	// on a DNS server being unreachable.
	T.Run("surfaces a resolution failure", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.ErrorIs(t, CheckEndpointURL(ctx, "https://example.com/hooks"), ErrInvalidEndpointURL)
	})

	// The DNS lookup is what makes the delivery-time re-check able to hang, so
	// it has to honor the deadline it is given.
	T.Run("honors a deadline", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
		defer cancel()

		test.Error(t, CheckEndpointURL(ctx, "https://example.com/hooks"))
	})
}

func TestCheckIP(T *testing.T) {
	T.Parallel()

	T.Run("accepts a globally routable address", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, checkIP(net.ParseIP("93.184.216.34"), "example.com"))
		test.NoError(t, checkIP(net.ParseIP("2606:2800:220:1:248:1893:25c8:1946"), "example.com"))
	})

	T.Run("rejects everything else", func(t *testing.T) {
		t.Parallel()

		for name, ip := range map[string]string{
			"loopback v4":     "127.0.0.1",
			"loopback v6":     "::1",
			"link-local v4":   "169.254.169.254",
			"link-local v6":   "fe80::1",
			"private 10":      "10.0.0.1",
			"private 172":     "172.20.0.1",
			"private 192.168": "192.168.0.1",
			"unique local v6": "fd12::1",
			"unspecified v4":  "0.0.0.0",
			"unspecified v6":  "::",
			"multicast v4":    "239.0.0.1",
			"multicast v6":    "ff02::1",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				test.ErrorIs(t, checkIP(net.ParseIP(ip), "host"), ErrDisallowedEndpointHost)
			})
		}
	})
}

// The ranges net.IP has no predicate for, checked at both edges of each prefix
// and at the addresses either side of it. The edges are what a wrong mask gets
// wrong: 100.64.0.0/10 written as a /16 still refuses 100.64.0.1 and lets the
// rest of a Tailscale network through, and only 100.127.255.255 says so.
func TestCheckIP_reservedRanges(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		ip       string
		rejected bool
	}{
		// RFC 6598 carrier-grade NAT: 100.64.0.0 – 100.127.255.255.
		"cgnat first":     {ip: "100.64.0.0", rejected: true},
		"cgnat tailscale": {ip: "100.100.100.100", rejected: true},
		"cgnat last":      {ip: "100.127.255.255", rejected: true},
		"cgnat below":     {ip: "100.63.255.255"},
		"cgnat above":     {ip: "100.128.0.0"},

		// RFC 2544 benchmarking: 198.18.0.0 – 198.19.255.255.
		"benchmarking first": {ip: "198.18.0.0", rejected: true},
		"benchmarking last":  {ip: "198.19.255.255", rejected: true},
		"benchmarking below": {ip: "198.17.255.255"},
		"benchmarking above": {ip: "198.20.0.0"},

		// RFC 6890 IETF protocol assignments: 192.0.0.0 – 192.0.0.255, which is
		// where DS-Lite and the NAT64 discovery addresses live.
		"assignments first":   {ip: "192.0.0.0", rejected: true},
		"assignments ds-lite": {ip: "192.0.0.1", rejected: true},
		"assignments nat64":   {ip: "192.0.0.170", rejected: true},
		"assignments last":    {ip: "192.0.0.255", rejected: true},
		"assignments below":   {ip: "191.255.255.255"},
		"assignments above":   {ip: "192.0.1.0"},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tc.ip)
			must.NotNil(t, ip)

			if tc.rejected {
				test.ErrorIs(t, checkIP(ip, "host"), ErrDisallowedEndpointHost)

				return
			}

			test.NoError(t, checkIP(ip, "host"))
		})
	}
}

func TestReservedPrefix(T *testing.T) {
	T.Parallel()

	// The dotted-quad form parses to a 4-in-6 address, so the answer here is
	// what the unmap buys: without it every IPv4 prefix would contain nothing.
	T.Run("reports the range an address falls in", func(t *testing.T) {
		t.Parallel()

		prefix, ok := reservedPrefix(net.ParseIP("100.100.100.100"))

		must.True(t, ok)
		test.EqOp(t, netip.MustParsePrefix("100.64.0.0/10"), prefix)
	})

	// A four-byte net.IP reaches netip.AddrFromSlice as IPv4 and must answer the
	// same way the sixteen-byte one did.
	T.Run("reports the range for a four-byte address", func(t *testing.T) {
		t.Parallel()

		prefix, ok := reservedPrefix(net.IPv4(198, 18, 0, 1).To4())

		must.True(t, ok)
		test.EqOp(t, netip.MustParsePrefix("198.18.0.0/15"), prefix)
	})

	T.Run("reports nothing for a public address", func(t *testing.T) {
		t.Parallel()

		prefix, ok := reservedPrefix(net.ParseIP("93.184.216.34"))

		test.False(t, ok)
		test.EqOp(t, netip.Prefix{}, prefix)
	})

	// An address of no recognizable length is not in a range; it is refused by
	// checkIP's global-unicast arm before it ever gets here.
	T.Run("reports nothing for an unparseable address", func(t *testing.T) {
		t.Parallel()

		prefix, ok := reservedPrefix(net.IP{1, 2, 3})

		test.False(t, ok)
		test.EqOp(t, netip.Prefix{}, prefix)
	})
}

// An IPv6 address that names an IPv4 one is that address to any gateway in
// front of it, so the answer for it is the answer for what it carries — which
// is the whole of checkIP rather than a copy of the interesting half.
//
// The public cases are the point as much as the refusals. Refusing the ranges
// outright would refuse NAT64 itself, which is how a v6-only deployment reaches
// anything at all.
func TestCheckIP_embeddedIPv4(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		ip       string
		rejected bool
	}{
		// RFC 6052 well-known NAT64 prefix: the address is the low 32 bits.
		"nat64 over rfc1918":    {ip: "64:ff9b::c0a8:101", rejected: true},  // 192.168.1.1
		"nat64 over loopback":   {ip: "64:ff9b::7f00:1", rejected: true},    // 127.0.0.1
		"nat64 over link local": {ip: "64:ff9b::a9fe:a9fe", rejected: true}, // 169.254.169.254
		"nat64 over cgnat":      {ip: "64:ff9b::6440:1", rejected: true},    // 100.64.0.1
		"nat64 over public":     {ip: "64:ff9b::5db8:d822"},                 // 93.184.216.34

		// RFC 3056 6to4: the address is the 32 bits after the 2002 prefix.
		"6to4 over rfc1918": {ip: "2002:c0a8:101::1", rejected: true}, // 192.168.1.1
		"6to4 over cgnat":   {ip: "2002:6440:1::1", rejected: true},   // 100.64.0.1
		"6to4 over public":  {ip: "2002:5db8:d822::1"},                // 93.184.216.34

		// RFC 4291 IPv4-compatible, deprecated and still routable at somebody.
		"v4 compatible over loopback": {ip: "::7f00:1", rejected: true}, // 127.0.0.1
		"v4 compatible over rfc1918":  {ip: "::c0a8:101", rejected: true},

		// A v6 address carrying nothing is judged as itself, either way.
		"ordinary public v6": {ip: "2606:4700:4700::1111"},
		"unique local v6":    {ip: "fd00::1", rejected: true},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tc.ip)
			must.NotNil(t, ip)

			if tc.rejected {
				test.ErrorIs(t, checkIP(ip, "host"), ErrDisallowedEndpointHost)

				return
			}

			test.NoError(t, checkIP(ip, "host"))
		})
	}
}

func TestEmbeddedIPv4(T *testing.T) {
	T.Parallel()

	T.Run("reports the address each range carries", func(t *testing.T) {
		t.Parallel()

		for name, tc := range map[string]struct{ in, want string }{
			"nat64":         {in: "64:ff9b::c0a8:101", want: "192.168.1.1"},
			"6to4":          {in: "2002:c0a8:101::1", want: "192.168.1.1"},
			"v4 compatible": {in: "::c0a8:101", want: "192.168.1.1"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				carried, ok := embeddedIPv4(net.ParseIP(tc.in))

				must.True(t, ok)
				test.True(t, carried.Equal(net.ParseIP(tc.want)))
			})
		}
	})

	// A dotted quad reaches this as a 4-in-6 address. It is an IPv4 address
	// written the way net.ParseIP writes them, not a v6 address carrying one,
	// and reading it as the latter would answer a question twice.
	T.Run("reports nothing for the addresses that carry nothing", func(t *testing.T) {
		t.Parallel()

		for name, in := range map[string]net.IP{
			"a dotted quad":        net.ParseIP("192.168.1.1"),
			"a four-byte address":  net.IPv4(192, 168, 1, 1).To4(),
			"an ordinary v6":       net.ParseIP("2606:4700:4700::1111"),
			"unique local":         net.ParseIP("fd00::1"),
			"no recognized length": {1, 2, 3},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				carried, ok := embeddedIPv4(in)

				test.False(t, ok)
				test.Nil(t, carried)
			})
		}
	})
}
