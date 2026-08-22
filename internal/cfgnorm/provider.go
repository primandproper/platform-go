package cfgnorm

import (
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
)

// Provider canonicalizes a provider name: trimmed of surrounding whitespace and
// lowercased.
//
// It exists because validation and dispatch have to agree on what a provider
// name is, and for a long while they did not. Every selector in this module
// dispatches on strings.TrimSpace(strings.ToLower(...)), while the matching
// validation.In rule compared the raw string — so "Segment", "LaunchDarkly" and
// "APNS " built the provider the operator meant and were rejected by that
// provider's own config. Each seam having its own unexported copy of the same
// two calls is what let the two halves drift apart one package at a time.
//
// Call it once, at the top of the function, and use the result for both the
// rule and the switch. Normalizing in place — assigning back to cfg.Provider —
// is a different thing and is only correct where the config is known to be
// owned by the caller; a config a composition root hands to several
// constructors should come back out of validation as the operator wrote it.
//
// It is named for the field it is nearly always applied to, but it is the
// canonicalization for any string a config dispatches on — async
// notifications' Topology is the other one.
func Provider(provider string) string {
	return strings.TrimSpace(strings.ToLower(provider))
}

// SelectProvider normalizes provider and returns it, or ErrUnknownProvider
// naming subject and the provider as the operator spelled it.
//
// Constructors call it before ValidateWithContext, for a reason that outlives
// the tidiness: ozzo's validation.Errors is a map with no Unwrap, so a sentinel
// returned from a validation rule is a string by the time it reaches the
// caller and errors.Is cannot find it. Selecting the provider first is what
// makes `errors.Is(err, errors.ErrUnknownProvider)` a fact rather than a
// convention, and it also means a typo'd provider reports itself rather than
// the missing credentials that are its consequence.
//
// known holds normalized names. Include "" in it where an unset provider
// selects a default, and leave it out where naming one is mandatory.
func SelectProvider(provider string, known []string, subject string) (string, error) {
	normalized := Provider(provider)
	if !slices.Contains(known, normalized) {
		return "", errors.Wrapf(errors.ErrUnknownProvider, "%s %q", subject, provider)
	}

	return normalized, nil
}
