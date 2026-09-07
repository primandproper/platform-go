// Package injection holds the samber/do helpers shared by this module's
// do.Provide registrations.
//
// It exists because the observability package cannot be the single home for
// them: it imports the four pillar config subpackages in order to build a
// Pillars, so those subpackages cannot import it back. Both sides reach the
// same helper through here instead.
package injection

import (
	"slices"

	"github.com/samber/do/v2"
)

// InvokeOptional resolves a service that need not be registered.
//
// It distinguishes the two failures a plain do.Invoke conflates: "nobody
// registered one", which yields the zero value and no error, and "the
// registered one failed to build", which is returned. That distinction is the
// whole point — a dependency nobody wanted should not stop a container from
// wiring up, and a dependency somebody configured wrongly should not be
// silently swapped for its absence.
//
// The distinction is drawn by asking the injector whether T is registered
// before invoking it, not by matching the invocation's error against
// do.ErrServiceNotFound. do reports a provider that itself invoked something
// unregistered with that same sentinel — the nested miss travels up the chain
// unwrapped, carrying its path only as text — so the sentinel says "something
// along the way was not found" and cannot say whether that something was T.
// Registration presence can. do.Invoke resolves T strictly by do.NameOf[T]
// through the scope and its ancestors, and do.ListProvidedServices walks the
// same scope and the same ancestors, on a virtual scope as well as a real one;
// an explicit do.As alias is registered under the alias name and so is listed,
// and the implicit aliasing that resolves an interface to some concrete
// belongs to do.InvokeAs alone, which this function does not call. The two
// therefore agree in every case do.Invoke can meet, and once T is known to be
// registered, every error its provider returns is a build failure — a nested
// not-found included.
func InvokeOptional[T any](i do.Injector) (T, error) {
	if !isProvided[T](i) {
		var zero T

		return zero, nil
	}

	return do.Invoke[T](i)
}

// isProvided reports whether a service do.Invoke[T] would find is registered
// in the injector or one of its ancestor scopes.
func isProvided[T any](i do.Injector) bool {
	name := do.NameOf[T]()

	return slices.ContainsFunc(i.ListProvidedServices(), func(d do.ServiceDescription) bool {
		return d.Service == name
	})
}
