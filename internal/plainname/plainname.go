// Package plainname validates plain names: the ones an operator writes into
// config — a plan name, a meter name — that then travel into cache keys,
// idempotency keys, metric attribute values, and permission strings.
//
// It is named for what it validates rather than for "identifier", which is the
// public identifiers package's word for a minted opaque ID. The two answer
// unrelated questions — is this string one I generated, versus may this name go
// where it is about to go — and the near-collision read as a typo at every call
// site that imported one while meaning the other.
//
// Those destinations are the reason the rule is restriction rather than
// escaping. None of the four has a quoting convention a producing package could
// rely on, and the cache key is the one that matters most: a separator that can
// appear inside a component is a key collision, and a key collision there serves
// one subject another's answer.
//
// This is deliberately narrow. Packages whose names travel somewhere else — a
// SQL identifier interpolated into query text, a saga step name that is a
// component of a colon-separated key — have their own rule and their own
// validator, because the charset each admits follows from where the name goes
// rather than from a shared notion of "identifier".
//
// The alphabet below comes from the charset package, and that does not soften
// the point. What is shared there is the set of bytes and the scan over them;
// what is not shared is which bytes, and why. A caller reading the rule reads it
// on this line rather than being sent to a common predicate to find out what it
// currently means — which is the thing worth refusing, and is why charset offers
// no way to hand it an arbitrary function.
package plainname

import (
	"github.com/primandproper/platform-go/v13/charset"
)

// plain is the alphabet: a letter or underscore, followed by letters, digits,
// or underscores.
//
// A leading digit is rejected so that a name is never mistakable for a number in
// the places these travel — a metric attribute value, a cache key component.
var plain = charset.New(
	charset.ASCIIAlphanumeric.Union(charset.Bytes('_')),
	charset.WithFirst(charset.ASCIILetters.Union(charset.Bytes('_'))),
)

// Valid reports whether name is a plain name no longer than maxLen.
//
// The bound stays here rather than in plain because it is the caller's and not
// the rule's: entitlements caps a plan name, metering caps a meter name, and the
// alphabet is the same either way. It counts bytes, as every ceiling these names
// run into does.
func Valid(name string, maxLen int) bool {
	return len(name) <= maxLen && plain.Valid(name)
}
