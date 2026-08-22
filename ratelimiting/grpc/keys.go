package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// ErrNilKeyFunc indicates the interceptor was built without a key function.
var ErrNilKeyFunc = platformerrors.New("nil rate limit key function")

// KeyFunc extracts the key an RPC is counted against. It is given the server
// info as well as the context, so a service can limit one expensive method
// harder than the rest by folding info.FullMethod into the key.
//
// Returning an empty key with a nil error exempts the RPC. An error is treated
// as a failure of the guard rather than a verdict from it — see WithFailClosed.
type KeyFunc func(ctx context.Context, info *grpc.UnaryServerInfo) (string, error)

// Key prefixes, so that composing extractors cannot collide a peer-derived key
// with a metadata-derived one.
const (
	peerKeyPrefix     = "peer:"
	metadataKeyPrefix = "md:"
)

// KeyByPeer keys on the address the RPC's connection came from.
//
// It is the safe default and, with nothing in front of the server, the only
// safe one: metadata is written by the caller. Behind a proxy every RPC
// arrives from the proxy, so this pools all of them into one bucket — key on
// whatever the proxy asserts instead, via KeyByMetadata.
func KeyByPeer() KeyFunc {
	return func(ctx context.Context, _ *grpc.UnaryServerInfo) (string, error) {
		p, ok := peer.FromContext(ctx)
		if !ok || p.Addr == nil {
			return "", nil
		}

		// An address with no port to split off — a unix socket, say — keys on
		// itself. That is a fallback, not a failure: the caller is still
		// identified, and refusing to key it would exempt it from the limit.
		addr := p.Addr.String()
		if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
			addr = host
		}

		return peerKeyPrefix + addr, nil
	}
}

// KeyByMetadata keys on an incoming metadata value — an API key, a tenant ID,
// whatever the service issues to identify a caller.
//
// The value is hashed rather than used verbatim, for the reason the HTTP
// package's KeyByHeader gives: a limiter key becomes a Redis key and reaches
// spans on the way, and a credential that lands in a keyspace has been
// disclosed. An RPC without the metadata is exempted rather than pooled under
// one empty key; compose with FirstNonEmpty to fall through to the peer.
//
// Only the first value is read. Metadata is multi-valued, and a caller that
// sends two would otherwise get a different key per ordering.
func KeyByMetadata(name string) KeyFunc {
	return func(ctx context.Context, _ *grpc.UnaryServerInfo) (string, error) {
		values := metadata.ValueFromIncomingContext(ctx, name)
		if len(values) == 0 {
			return "", nil
		}

		value := strings.TrimSpace(values[0])
		if value == "" {
			return "", nil
		}

		sum := sha256.Sum256([]byte(value))

		return metadataKeyPrefix + hex.EncodeToString(sum[:]), nil
	}
}

// PerMethod scopes another extractor's key to the method being called, giving
// every method its own bucket per caller rather than one shared between them.
//
// Use it where one method is far more expensive than its neighbors and should
// not be able to spend the budget they need. The method name is bounded — it
// comes from the service definition, not from the caller — so it is safe to
// concatenate into a key.
//
// An exempted key stays exempt: scoping nothing to a method is still nothing.
func PerMethod(fn KeyFunc) KeyFunc {
	return func(ctx context.Context, info *grpc.UnaryServerInfo) (string, error) {
		key, err := fn(ctx, info)
		if err != nil || key == "" {
			return "", err
		}

		return info.FullMethod + "|" + key, nil
	}
}

// FirstNonEmpty tries each KeyFunc in order and returns the first non-empty
// key. An error stops the ladder and is returned rather than falling through:
// an extractor that failed has not established that the caller is
// unidentified, and a broader key would quietly loosen the limit on exactly
// the RPCs something already went wrong on.
func FirstNonEmpty(fns ...KeyFunc) KeyFunc {
	return func(ctx context.Context, info *grpc.UnaryServerInfo) (string, error) {
		for _, fn := range fns {
			if fn == nil {
				continue
			}

			key, err := fn(ctx, info)
			if err != nil {
				return "", err
			}

			if key != "" {
				return key, nil
			}
		}

		return "", nil
	}
}
