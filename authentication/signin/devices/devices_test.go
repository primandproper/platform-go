package devices

import (
	"context"
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shoenig/test"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func TestPeerExtractor(T *testing.T) {
	T.Parallel()

	T.Run("reads the peer's host and the client's user agent", func(t *testing.T) {
		t.Parallel()

		ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51234}})
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(userAgentMetadataKey, "  ", userAgentMetadataKey, "grpc-go/1.80"))

		test.EqOp(t, Origin{IPAddress: "203.0.113.7", UserAgent: "grpc-go/1.80"}, PeerExtractor(ctx))
	})

	// Behind a proxy that means recording the proxy, which is the honest answer
	// when nothing says which proxies to trust.
	T.Run("trusts no forwarded header", func(t *testing.T) {
		t.Parallel()

		ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 443}})
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-for", "198.51.100.9", "x-device-name", "spoofed"))

		test.EqOp(t, Origin{IPAddress: "10.0.0.2"}, PeerExtractor(ctx))
	})

	T.Run("keeps an address it cannot split whole", func(t *testing.T) {
		t.Parallel()

		ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.UnixAddr{Name: "/run/app.sock", Net: "unix"}})

		test.EqOp(t, Origin{IPAddress: "/run/app.sock"}, PeerExtractor(ctx))
	})

	T.Run("reads nothing from a context that is not a gRPC request's", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Origin{}, PeerExtractor(context.Background()))
	})
}

func TestDevice_Attributes(T *testing.T) {
	T.Parallel()

	T.Run("names only what is known", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, map[string]string{AttributeUserAgent: "Mozilla/5.0"}, (&Device{UserAgent: "Mozilla/5.0"}).Attributes())
	})

	T.Run("renders nothing for no device", func(t *testing.T) {
		t.Parallel()

		var device *Device

		test.MapEmpty(t, device.Attributes())
	})
}

func TestBound(T *testing.T) {
	T.Parallel()

	T.Run("leaves a short value alone, less its surrounding space", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "Mozilla/5.0", bound("  Mozilla/5.0\n"))
	})

	T.Run("drops bytes that are not UTF-8", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "Mozilla/5.0", bound("Mozilla\xff\xfe/5.0"))
	})

	T.Run("cuts a long value at the bound", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, strings.Repeat("a", MaxFieldLength), bound(strings.Repeat("a", MaxFieldLength*2)))
	})

	// A cut through a character would make a valid value invalid on the way in,
	// which Postgres refuses.
	T.Run("never splits a character", func(t *testing.T) {
		t.Parallel()

		for _, char := range []string{"é", "€", "😀"} {
			got := bound(strings.Repeat(char, MaxFieldLength))

			test.LessEq(t, MaxFieldLength, len(got), test.Sprintf("character %q", char))
			test.True(t, utf8.ValidString(got), test.Sprintf("character %q", char))
			test.Greater(t, MaxFieldLength-utf8.UTFMax, len(got), test.Sprintf("character %q", char))
		}
	})
}
