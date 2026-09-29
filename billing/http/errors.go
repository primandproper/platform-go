package http

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. All of them are construction refusals: a
// delivery that fails is answered with a status code rather than an error,
// because the provider on the other end reads nothing else.
var (
	// ErrNilPaymentManager indicates a nil capitalism.PaymentManager. It is the
	// half that verifies a delivery, and an endpoint without it would be
	// reconciling bytes anybody could have sent.
	ErrNilPaymentManager = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil payment manager for a billing webhook")

	// ErrNilSyncer indicates a nil billing/sync Syncer.
	ErrNilSyncer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing syncer for a billing webhook")

	// ErrNilClient indicates a nil database.Client, which the handler opens the
	// delivery's transaction on.
	ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for a billing webhook")

	// ErrNilScopeResolver indicates a handler built without WithScopeResolver.
	//
	// It has no default, and that is the point: which tenant a delivery is for
	// is the one thing on this endpoint that is the deployment's own, and a
	// default would make the wiring that answers it and the wiring that forgot
	// to look identical. A deployment with no tenants passes GlobalScope, by
	// name.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing webhook scope resolver")
)
