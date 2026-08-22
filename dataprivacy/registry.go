package dataprivacy

import (
	"slices"

	"github.com/primandproper/platform-go/v13/charset"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// validKey is a registration key: a lowercase identifier fragment, optionally
// dotted, with no bound on how many segments. Keys become object keys in the
// artifact and attribute values in telemetry, and both are places where an
// arbitrary string is a problem rather than a feature.
//
// Lowercase only, and no leading underscore — narrower than the SQL identifier
// rule elsewhere in this module, because a key here is written by a person
// registering a domain rather than derived from a schema, and one spelling of
// it is enough.
var validKey = charset.New(
	charset.ASCIILower.Union(charset.ASCIIDigits, charset.Bytes('_')),
	charset.WithFirst(charset.ASCIILower),
	charset.WithSeparator('.', 0),
)

// Registry is the set of domains that know how to collect and erase.
//
// Registration replaces the god-struct the prior art aggregated into. There,
// every domain wrote into one shared type, so adding a domain meant editing a
// central file that imported every domain package — a cost paid on every schema
// change by the one file most likely to conflict, and one that grew two fields
// in a single month. Here a domain announces itself and the library composes
// what it gets, so adding one touches one call site and nothing else.
//
// A Registry is built during startup and read concurrently thereafter. It is
// not safe to register into one that a Fulfiller is already running against, and
// nothing here pretends otherwise: registration is a wiring-time activity, and
// a mutex would only make an ordering bug quieter.
type Registry struct {
	collectors map[string]Collector
	erasers    map[string]Eraser
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		collectors: map[string]Collector{},
		erasers:    map[string]Eraser{},
	}
}

// RegisterCollector adds one domain's export collector under key, which becomes
// that domain's section name in every artifact.
//
// Re-registering a key is an error rather than a replacement. A silent
// overwrite would drop a domain from every export from then on, and the only
// symptom would be a section missing from a file nobody reads until a regulator
// does.
func (r *Registry) RegisterCollector(key string, collector Collector) error {
	if err := validateKey(key); err != nil {
		return err
	}

	if collector == nil {
		return platformerrors.Wrapf(platformerrors.ErrNilInputParameter, "nil dataprivacy collector for %q", key)
	}

	if _, exists := r.collectors[key]; exists {
		return platformerrors.Wrapf(ErrDuplicateKey, "dataprivacy collector %q", key)
	}

	r.collectors[key] = collector

	return nil
}

// RegisterEraser adds one domain's eraser under key.
//
// A domain may register a Collector, an Eraser, or both, and the keys are
// deliberately independent namespaces. A domain that holds data it must export
// but may never delete — an immutable ledger — registers only a collector, and
// that asymmetry is the normal case rather than a misconfiguration.
func (r *Registry) RegisterEraser(key string, eraser Eraser) error {
	if err := validateKey(key); err != nil {
		return err
	}

	if eraser == nil {
		return platformerrors.Wrapf(platformerrors.ErrNilInputParameter, "nil dataprivacy eraser for %q", key)
	}

	if _, exists := r.erasers[key]; exists {
		return platformerrors.Wrapf(ErrDuplicateKey, "dataprivacy eraser %q", key)
	}

	r.erasers[key] = eraser

	return nil
}

// CollectorKeys returns the registered collector keys, sorted.
//
// Sorted rather than in registration order, so an artifact's manifest lists its
// sections identically whatever order the wiring happened to run in. Two
// exports of the same subject differing only in the order of a JSON array is
// the kind of diff that costs somebody an afternoon.
func (r *Registry) CollectorKeys() []string {
	return sortedKeys(r.collectors)
}

// EraserKeys returns the registered eraser keys, sorted.
func (r *Registry) EraserKeys() []string {
	return sortedKeys(r.erasers)
}

// Collector returns the collector registered under key.
func (r *Registry) Collector(key string) (Collector, bool) {
	collector, ok := r.collectors[key]

	return collector, ok
}

// Eraser returns the eraser registered under key.
func (r *Registry) Eraser(key string) (Eraser, bool) {
	eraser, ok := r.erasers[key]

	return eraser, ok
}

// validateKey reports whether key is usable as a section name.
func validateKey(key string) error {
	if key == "" {
		return platformerrors.Wrap(ErrInvalidKey, "empty dataprivacy registration key")
	}

	if !validKey.Valid(key) {
		return platformerrors.Wrapf(ErrInvalidKey, "dataprivacy registration key %q", key)
	}

	return nil
}

// sortedKeys projects a map's keys in sorted order.
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}
