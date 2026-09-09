package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's BillingWriter: one method per billing event, because a
// processor webhook is unauthenticated and public and what it can reach is
// worth being able to enumerate.
var _ BillingWriter = (*SQLStore)(nil)

// RecordAccountSubscription writes what a processor delivery reported: the
// account's standing, the plan it is on, and the reconciliation that just
// happened.
//
// The three move together because a delivery reports them together. Writing
// them one at a time would leave an account paid on last month's plan between
// two of the writes, and would leave it there for good if the second failed.
//
// The plan is required. An account whose subscription has ended is
// RecordAccountSubscriptionEnded, which is a separate method rather than an
// empty plan here because the difference between them is a cancellation: a
// handler passing through an unchecked payload would otherwise cancel a
// subscription while believing it had renewed one.
//
// The sync stamp is the Store's clock rather than a parameter, for the reason
// MarkUserEmailAddressVerified takes no time: the fact being recorded is that
// this process heard from the processor, and the moment that happened is the
// moment of the write.
func (s *SQLStore) RecordAccountSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
	planID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "recording identity account subscription")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "recording identity account subscription")
	}

	if !status.Valid() {
		return op.Error(
			platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "billing status %q", status),
			"recording identity account subscription",
		)
	}

	if planID == "" {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty subscription plan"),
			"recording identity account subscription",
		)
	}

	if err := s.recordSubscription(ctx, tx, scope, accountID, status, pointer.To(planID)); err != nil {
		return op.Error(err, "recording identity account subscription")
	}

	return nil
}

// RecordAccountSubscriptionEnded writes the delivery that says there is no
// subscription any more: the new standing, no plan, and the reconciliation.
//
// The plan becomes NULL rather than the empty string, because the two are
// different facts about an account — no plan is what a cancelled subscription
// leaves behind, and the empty string is a plan whose identifier is nothing —
// and because a cancellation that left the old plan in place leaves the account
// unpaid on a plan it is no longer paying for, which is what every downstream
// entitlement check then reads.
//
// The standing is a parameter because ending is not one status: a subscription
// the customer cancelled, one the processor gave up collecting on, and one whose
// trial simply ran out are the same write over different standings, and which of
// them means what is the application's policy rather than this package's.
func (s *SQLStore) RecordAccountSubscriptionEnded(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "recording identity account subscription ended")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "recording identity account subscription ended")
	}

	if !status.Valid() {
		return op.Error(
			platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "billing status %q", status),
			"recording identity account subscription ended",
		)
	}

	if err := s.recordSubscription(ctx, tx, scope, accountID, status, nil); err != nil {
		return op.Error(err, "recording identity account subscription ended")
	}

	return nil
}

// recordSubscription runs the one statement both deliveries share, since a
// subscription that is current and one that has ended differ only in whether the
// bound plan is a plan or a NULL. What differs is the surface, which is where the
// difference belongs: the column's whole domain stays writable precisely because
// the argument saying what it becomes is not also the argument saying whether to
// write it.
func (s *SQLStore) recordSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
	planID *string,
) error {
	count, err := s.q.RecordAccountSubscription(ctx, tx, identitydb.RecordAccountSubscriptionParams{
		ID:                          accountID,
		Scope:                       scope,
		BillingStatus:               status.String(),
		SubscriptionPlanID:          planID,
		LastPaymentProviderSyncedAt: pointer.To(s.now()),
	})

	return s.guardCount(ctx, count, err, ErrAccountNotFound, "recording identity account subscription")
}

// SetAccountBillingStatus moves an account between billing standings without
// touching its plan or its reconciliation stamp.
//
// This is the operator's move — a suspension, which no processor reports. A
// delivery from the processor is RecordAccountSubscription or
// RecordAccountSubscriptionEnded, both of which stamp the reconciliation this
// one deliberately does not: nothing was asked of the processor here, and
// stamping would date the account's billing state to an operator's click.
func (s *SQLStore) SetAccountBillingStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity account billing status")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity account billing status")
	}

	if !status.Valid() {
		return op.Error(
			platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "billing status %q", status),
			"setting identity account billing status",
		)
	}

	count, err := s.q.SetAccountBillingStatus(ctx, tx, identitydb.SetAccountBillingStatusParams{
		ID:            accountID,
		Scope:         scope,
		BillingStatus: status.String(),
	})
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "setting identity account billing status"); err != nil {
		return op.Error(err, "setting identity account billing status")
	}

	return nil
}

// SetAccountPaymentProcessorCustomerID attaches the account to its customer at
// the processor, which is the write that happens the first time it is created
// there.
func (s *SQLStore) SetAccountPaymentProcessorCustomerID(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID, customerID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity account payment processor customer")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity account payment processor customer")
	}

	if customerID == "" {
		// The empty string is how "not created at the processor" is stored, so
		// writing it here would be a detachment dressed as an attachment.
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty payment processor customer"),
			"setting identity account payment processor customer",
		)
	}

	count, err := s.q.SetAccountPaymentProcessorCustomerID(ctx, tx,
		identitydb.SetAccountPaymentProcessorCustomerIDParams{
			ID:                         accountID,
			Scope:                      scope,
			PaymentProcessorCustomerID: customerID,
		})
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "setting identity account payment processor customer"); err != nil {
		return op.Error(err, "setting identity account payment processor customer")
	}

	return nil
}

// MarkAccountBillingSynced stamps a reconciliation that moved nothing, as of the
// Store's clock.
//
// It is the write a reconciler owes the next run: "we checked and nothing had
// changed" is a fact, and without it an account that has been current for a year
// is indistinguishable from one nobody has looked at since. A reconciliation
// that did find something moved writes it through RecordAccountSubscription,
// which stamps this column itself.
//
// It takes no time, for the reason MarkUserEmailAddressVerified takes none: the
// fact being recorded is that this process reconciled with the processor, and
// the moment that happened is the moment of the write. A caller-supplied time
// would let a handler running through a backlog claim it had just checked, and
// would put a value bound in the caller's location into a column this package
// compares lexically on one of its three dialects — see the timestamp note in
// identity/queries.
func (s *SQLStore) MarkAccountBillingSynced(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	accountID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(accountIDKey, accountID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "marking identity account billing synced")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "marking identity account billing synced")
	}

	count, err := s.q.MarkAccountBillingSynced(ctx, tx, identitydb.MarkAccountBillingSyncedParams{
		ID:                          accountID,
		Scope:                       scope,
		LastPaymentProviderSyncedAt: pointer.To(s.now()),
	})
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "marking identity account billing synced"); err != nil {
		return op.Error(err, "marking identity account billing synced")
	}

	return nil
}
