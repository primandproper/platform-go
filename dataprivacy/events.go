package dataprivacy

import (
	"github.com/primandproper/platform-go/v15/webhooks"
)

// The events this package emits. They are platform's names for platform's own
// writes, so they are constants here rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody.
const (
	// EventErasureFulfilled says an erasure request ran to completion. It is
	// emitted once per request, on the transaction that ran every eraser, and
	// the payload carries what each section destroyed.
	EventErasureFulfilled webhooks.EventType = "dataprivacy.erasure.fulfilled"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, dataprivacy.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventErasureFulfilled: {Description: "An erasure request ran to completion across every registered section."},
	}
}

// ErasureEvent is the payload of EventErasureFulfilled.
//
// It is the erasure's one signal. "Forget this person" is one request,
// fulfilled across every store the person touched, and the stores' own hooks
// record nothing for their part of it so that the request is not written down
// once per store; what each of them did is here instead, under Sections, beside
// the request that did it. That is also why it names the subject when the
// per-store hooks did not: a subscriber told an erasure happened has to be told
// whom it was about to propagate it, and a subject's opaque ID is how every
// other event in this module names one.
type ErasureEvent struct {
	_ struct{} `json:"-"`

	// Sections is what each registered eraser destroyed, keyed by the key it was
	// registered under. Every eraser that ran is present, zero included.
	Sections map[string]ErasureSection `json:"sections"`
	// RequestID is the request the erasure fulfilled.
	RequestID string `json:"requestID"`
	// Subject is whom the request was about.
	Subject Subject `json:"subject"`
	// Deleted is how many rows the erasure destroyed, across every section.
	Deleted int64 `json:"deleted"`
	// Anonymized is how many rows it kept but stripped, across every section.
	Anonymized int64 `json:"anonymized"`
	// KeyShredded says the subject's data key was destroyed as well.
	KeyShredded bool `json:"keyShredded"`
}

// ErasureSection is what one eraser destroyed. The basis for anything it
// retained is on the request, not here: it is prose written for a regulator,
// and an event is copied into every subscriber's logs.
type ErasureSection struct {
	_ struct{} `json:"-"`

	// Deleted is how many rows the eraser destroyed.
	Deleted int64 `json:"deleted"`
	// Anonymized is how many rows it kept but stripped.
	Anonymized int64 `json:"anonymized"`
}
