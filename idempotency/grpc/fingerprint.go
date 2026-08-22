package grpc

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/primandproper/platform-go/v13/idempotency"

	"google.golang.org/protobuf/proto"
)

// fingerprint identifies the request a key is being used for.
//
// The full method goes in so one key cannot answer calls to two different
// RPCs, and the principal so two tenants sending the same key for the same
// call do not share a record.
//
// The request is marshaled deterministically. Without that, a message with a
// map field serializes differently on every attempt, and an ordinary retry
// would be reported as key reuse.
func fingerprint(fullMethod, principal string, req proto.Message) (idempotency.Fingerprint, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return "", err
	}

	sum := sha256.New()

	// Length-prefixed so the parts cannot run together: without it a method of
	// "/a" with principal "bc" would hash the same as "/ab" with principal "c".
	write := func(part []byte) {
		_, _ = sum.Write([]byte(strconv.Itoa(len(part))))
		_, _ = sum.Write([]byte(":"))
		_, _ = sum.Write(part)
	}

	write([]byte(fullMethod))
	write([]byte(principal))
	write(payload)

	return idempotency.Fingerprint(hex.EncodeToString(sum.Sum(nil))), nil
}
