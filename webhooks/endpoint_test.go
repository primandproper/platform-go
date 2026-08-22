package webhooks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/tenancy"

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

func TestEndpoint_Validate(T *testing.T) {
	T.Parallel()

	valid := func() *Endpoint {
		return &Endpoint{
			ID:          "endpoint-1",
			Scope:       testScope,
			URL:         "https://93.184.216.34/hooks",
			ContentType: DefaultContentType,
			Secret:      Secret{Current: []byte("secret")},
			Events:      []EventType{orderCreated},
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
		endpoint.Events = nil

		test.ErrorIs(t, endpoint.Validate(t.Context(), testCatalog, nil), ErrNoEvents)
	})

	// The typo case the catalog exists to catch. Without this check the endpoint
	// registers cleanly and then never fires.
	T.Run("subscribing to an unknown event", func(t *testing.T) {
		t.Parallel()

		endpoint := valid()
		endpoint.Events = []EventType{orderCreated, "odrer.updated"}

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

		endpoint, err := json.Marshal(&Endpoint{Events: []EventType{orderCreated, orderUpdated}})
		must.NoError(t, err)
		test.StrContains(t, string(endpoint), `"events":["order.created","order.updated"]`)

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
		must.NoError(t, json.Unmarshal([]byte(`{"events":["order.created"]}`), &endpoint))
		test.Eq(t, []EventType{orderCreated}, endpoint.Events)

		var delivery Delivery
		must.NoError(t, json.Unmarshal([]byte(`{"eventType":"order.created"}`), &delivery))
		test.EqOp(t, orderCreated, delivery.EventType)

		var catalog Catalog
		must.NoError(t, json.Unmarshal([]byte(`{"order.created":{"description":"d"}}`), &catalog))
		test.True(t, catalog.Known(orderCreated))
	})
}

// The event type reaches the driver as a string rather than as an EventType.
// Both work against most drivers — a defined string type goes through their
// reflective fallback — but "most" is a property of whichever driver a consumer
// wired up, and a Store implementation is allowed to be one this package has
// never seen. The conversion is at the boundary so that it is this package's
// decision rather than a driver's.
func TestQueries_BindEventTypesAsStrings(T *testing.T) {
	T.Parallel()

	t := newTables(DefaultTablePrefix)

	T.Run("subscription inserts", func(t2 *testing.T) {
		t2.Parallel()

		_, args := t.buildInsertSubscriptions(dialect.SQLite, "endpoint-1", []EventType{orderCreated, orderUpdated})

		must.SliceLen(t2, 4, args)
		test.EqOp(t2, "order.created", mustBeString(t2, args[1]))
		test.EqOp(t2, "order.updated", mustBeString(t2, args[3]))
	})

	T.Run("the fan-out lookup", func(t2 *testing.T) {
		t2.Parallel()

		_, args := t.buildSelectEndpointsForEvent(dialect.SQLite, testScope, orderCreated)

		must.SliceNotEmpty(t2, args)
		test.EqOp(t2, "order.created", mustBeString(t2, args[0]))
	})

	T.Run("delivery inserts", func(t2 *testing.T) {
		t2.Parallel()

		_, args := t.buildInsertDelivery(dialect.SQLite, &Delivery{
			ID: "d", Scope: testScope, EventType: orderCreated, Payload: testBody,
		}, time.Now().UTC())

		must.SliceLen(t2, 6, args)
		test.EqOp(t2, "order.created", mustBeString(t2, args[2]))
	})
}

// mustBeString fails unless the bound argument is a string. A test that only
// compared values would pass on an EventType too, since the comparison would be
// against an untyped constant.
func mustBeString(t *testing.T, arg any) string {
	t.Helper()

	bound, ok := arg.(string)
	must.True(t, ok, must.Sprintf("bound argument %#v is %T, want string", arg, arg))

	return bound
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
