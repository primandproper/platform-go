package random

import (
	"math/rand/v2"
)

// Element fetches a random element from an array. It returns the zero value of
// T for an empty slice rather than panicking.
func Element[T any](s []T) T {
	if len(s) == 0 {
		var zero T
		return zero
	}

	//nolint:gosec // not going to use crypto/rand for this
	return s[rand.IntN(len(s))]
}
