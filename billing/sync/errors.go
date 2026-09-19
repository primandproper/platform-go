package sync

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. Every other error a caller sees here came
// out of the billing store or out of their own Place, wrapped with what the sync
// was doing when it arrived.
var (
	// ErrNilStore indicates a nil billing.SubscriptionStore.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subscription store for a billing sync")

	// ErrNilPlace indicates a nil Place. It is required — see the package
	// documentation for what a delivery cannot say and why nothing here can
	// answer it.
	ErrNilPlace = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subscription placement")

	// ErrNilBillingWriter indicates a WithStanding that named a classifier and
	// no identity.BillingWriter. The two go together: a reading with nothing to
	// write it to is a standing nobody stores.
	ErrNilBillingWriter = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity billing writer")

	// ErrNilClassify indicates a WithStanding that named a writer and no
	// standing.Classify. There is deliberately no default — see the package
	// documentation.
	ErrNilClassify = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing standing classifier")

	// ErrNilExecutor indicates a nil transaction. Every write a sync makes runs
	// on the caller's, because the row it writes is one fact with whatever else
	// that transaction is recording.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing sync transaction")

	// ErrNilEvent indicates a nil capitalism.Event.
	ErrNilEvent = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil payment processor event")

	// ErrUnidentifiedSubscription indicates a delivery reporting a subscription
	// the adapter placed no provider-side identifier on.
	//
	// It is refused rather than acknowledged. The identifier is what every
	// subsequent delivery for the same agreement is matched on, so a row written
	// without one is an agreement no later event can ever find — and the next
	// delivery would open a second.
	ErrUnidentifiedSubscription = platformerrors.New("subscription event names no provider-side subscription id")

	// ErrNoPaidPeriod indicates a delivery that would open an agreement and
	// reported no bounded paid period to open it with.
	//
	// It is the refusal this package exists for. The alternative is the
	// approximation the package documentation quotes, and a period invented at
	// the moment of the write is one nothing downstream can tell from a period
	// the provider reported.
	ErrNoPaidPeriod = platformerrors.New("subscription event reports no bounded paid period to open an agreement with")

	// ErrNoPlacement indicates a Place that answered with neither a placement
	// nor an error. There is nowhere to put the agreement, and a create from a
	// nil placement would be one refused by billing for an empty account, three
	// frames further down.
	ErrNoPlacement = platformerrors.New("subscription placement resolved to nothing")
)
