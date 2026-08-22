package http

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/primandproper/platform-go/v13/idempotency"
)

// fingerprint identifies the request a key is being used for.
//
// It covers more than the body on purpose. A key that only committed to a body
// would let the same payload posted to two endpoints — or by two different
// users — share one record, and the second caller would be handed the first's
// response. So the method, path, query, and principal all go in.
//
// The query is taken from Query().Encode(), which sorts by key. Using RawQuery
// would make ?a=1&b=2 and ?b=2&a=1 different requests and report an ordinary
// retry as key reuse.
//
// The body is hashed as raw bytes, which is strict: a client that
// re-serializes its JSON between attempts changes the fingerprint and is told
// it reused its key. That is the safe direction to err in — the alternative is
// deciding two different payloads are the same — and WithFingerprint exists
// for callers who want to canonicalize instead.
func fingerprint(req *http.Request, principal string, body []byte) idempotency.Fingerprint {
	sum := sha256.New()

	// Each part is length-prefixed so the parts cannot run together. Without
	// it, a path of "/a" with principal "bc" and a path of "/ab" with
	// principal "c" would hash identically.
	write := func(part string) {
		_, _ = sum.Write([]byte(strconv.Itoa(len(part))))
		_, _ = sum.Write([]byte(":"))
		_, _ = sum.Write([]byte(part))
	}

	write(req.Method)
	write(req.URL.Path)
	write(req.URL.Query().Encode())
	write(principal)

	_, _ = sum.Write([]byte(strconv.Itoa(len(body))))
	_, _ = sum.Write([]byte(":"))
	_, _ = sum.Write(body)

	return idempotency.Fingerprint(hex.EncodeToString(sum.Sum(nil)))
}
