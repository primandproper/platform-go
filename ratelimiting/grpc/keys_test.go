package grpc

import (
	"context"
	"net"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// withPeer returns a context carrying a peer at addr.
func withPeer(ctx context.Context, addr string) context.Context {
	return peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{
		IP:   net.ParseIP(addr),
		Port: 54321,
	}})
}

// info is the server info every key test passes.
var info = &grpc.UnaryServerInfo{FullMethod: testMethod}

func TestKeyByPeer(T *testing.T) {
	T.Parallel()

	T.Run("keys on the connection's host", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByPeer()(withPeer(t.Context(), "203.0.113.7"), info)
		must.NoError(t, err)
		test.EqOp(t, peerKeyPrefix+"203.0.113.7", key)
	})

	T.Run("exempts an RPC with no peer on the context", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByPeer()(t.Context(), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})
}

func TestKeyByMetadata(T *testing.T) {
	T.Parallel()

	withMD := func(pairs ...string) context.Context {
		return metadata.NewIncomingContext(T.Context(), metadata.Pairs(pairs...))
	}

	T.Run("hashes the value rather than keying on it", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByMetadata("x-api-key")(withMD("x-api-key", "sk_live_supersecret"), info)
		must.NoError(t, err)
		test.StrHasPrefix(t, metadataKeyPrefix, key)
		test.StrNotContains(t, key, "sk_live_supersecret")
	})

	T.Run("is stable for the same value", func(t *testing.T) {
		t.Parallel()

		fn := KeyByMetadata("x-api-key")

		first, err := fn(withMD("x-api-key", "abc"), info)
		must.NoError(t, err)

		second, err := fn(withMD("x-api-key", "abc"), info)
		must.NoError(t, err)

		test.EqOp(t, first, second)
	})

	T.Run("reads only the first of a repeated value", func(t *testing.T) {
		t.Parallel()

		// Metadata is multi-valued; folding both in would give a caller a
		// different key per ordering.
		fn := KeyByMetadata("x-api-key")

		one, err := fn(withMD("x-api-key", "abc"), info)
		must.NoError(t, err)

		two, err := fn(withMD("x-api-key", "abc", "x-api-key", "def"), info)
		must.NoError(t, err)

		test.EqOp(t, one, two)
	})

	T.Run("exempts an RPC without the metadata", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByMetadata("x-api-key")(withMD("x-tenant", "acme"), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})

	T.Run("exempts a value that is only whitespace", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByMetadata("x-api-key")(withMD("x-api-key", "   "), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})

	T.Run("exempts an RPC with no metadata at all", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByMetadata("x-api-key")(t.Context(), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})
}

func TestPerMethod(T *testing.T) {
	T.Parallel()

	T.Run("gives every method its own bucket per caller", func(t *testing.T) {
		t.Parallel()

		fn := PerMethod(staticKey("caller"))

		one, err := fn(t.Context(), &grpc.UnaryServerInfo{FullMethod: "/things.v1.Things/GetThing"})
		must.NoError(t, err)

		two, err := fn(t.Context(), &grpc.UnaryServerInfo{FullMethod: "/things.v1.Things/ListThings"})
		must.NoError(t, err)

		test.NotEqOp(t, one, two)
		test.StrContains(t, one, "caller")
	})

	T.Run("leaves an exempted RPC exempt", func(t *testing.T) {
		t.Parallel()

		key, err := PerMethod(staticKey(""))(t.Context(), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})

	T.Run("propagates the inner extractor's error", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("principal lookup failed")
		fn := PerMethod(func(context.Context, *grpc.UnaryServerInfo) (string, error) { return "", boom })

		key, err := fn(t.Context(), info)
		must.ErrorIs(t, err, boom)
		test.EqOp(t, "", key)
	})
}

func TestFirstNonEmpty(T *testing.T) {
	T.Parallel()

	T.Run("returns the first extractor that produced a key", func(t *testing.T) {
		t.Parallel()

		fn := FirstNonEmpty(KeyByMetadata("x-api-key"), KeyByPeer())

		ctx := metadata.NewIncomingContext(withPeer(t.Context(), "203.0.113.7"),
			metadata.Pairs("x-api-key", "abc"))

		key, err := fn(ctx, info)
		must.NoError(t, err)
		test.StrHasPrefix(t, metadataKeyPrefix, key)
	})

	T.Run("falls through when an extractor exempts the RPC", func(t *testing.T) {
		t.Parallel()

		fn := FirstNonEmpty(KeyByMetadata("x-api-key"), KeyByPeer())

		key, err := fn(withPeer(t.Context(), "203.0.113.7"), info)
		must.NoError(t, err)
		test.EqOp(t, peerKeyPrefix+"203.0.113.7", key)
	})

	T.Run("stops on an error rather than widening the key", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("principal lookup failed")
		fn := FirstNonEmpty(
			func(context.Context, *grpc.UnaryServerInfo) (string, error) { return "", boom },
			KeyByPeer(),
		)

		key, err := fn(withPeer(t.Context(), "203.0.113.7"), info)
		must.ErrorIs(t, err, boom)
		test.EqOp(t, "", key)
	})

	T.Run("skips nil extractors", func(t *testing.T) {
		t.Parallel()

		key, err := FirstNonEmpty(nil, KeyByPeer())(withPeer(t.Context(), "203.0.113.7"), info)
		must.NoError(t, err)
		test.EqOp(t, peerKeyPrefix+"203.0.113.7", key)
	})

	T.Run("exempts the RPC when every extractor does", func(t *testing.T) {
		t.Parallel()

		key, err := FirstNonEmpty(KeyByMetadata("x-api-key"))(t.Context(), info)
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})
}

func TestKeyNamespacing(T *testing.T) {
	T.Parallel()

	// Composed extractors must not collide: a peer-derived key and a
	// metadata-derived one pooling two callers into one bucket is a limit
	// silently halved.
	peerKey, err := KeyByPeer()(withPeer(T.Context(), "203.0.113.7"), info)
	must.NoError(T, err)

	mdKey, err := KeyByMetadata("x-api-key")(
		metadata.NewIncomingContext(T.Context(), metadata.Pairs("x-api-key", "203.0.113.7")), info)
	must.NoError(T, err)

	test.NotEqOp(T, peerKey, mdKey)
}
