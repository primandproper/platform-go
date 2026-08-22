package saga_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/retry"
	"github.com/primandproper/platform-go/v13/saga"
)

// Booking is the state one order-placement saga carries between its steps.
type Booking struct {
	OrderID  string `json:"orderID"`
	ChargeID string `json:"chargeID"`
	Reserved bool   `json:"reserved"`
}

// ExampleRegister defines a linear saga: charge, reserve, notify. The first two
// can be taken back; the third is a fire-and-forget notification that needs no
// compensation.
func ExampleRegister() {
	registry := saga.NewRegistry()

	err := saga.Register(registry, saga.Definition[Booking]{
		Name: "place_order",
		Steps: []saga.Step[Booking]{
			{
				Name: "charge_card",
				Do: func(_ context.Context, b *Booking) error {
					b.ChargeID = "ch_" + b.OrderID

					return nil
				},
				Undo: func(_ context.Context, b *Booking) error {
					b.ChargeID = ""

					return nil
				},
			},
			{
				Name: "reserve_inventory",
				Do: func(_ context.Context, b *Booking) error {
					b.Reserved = true

					return nil
				},
				Undo: func(_ context.Context, b *Booking) error {
					b.Reserved = false

					return nil
				},
			},
			{
				// No Undo: a notification that was sent cannot be unsent, and
				// nothing after it can fail and force its compensation.
				Name: "notify_partner",
				Do:   func(context.Context, *Booking) error { return nil },
			},
		},
	})
	if err != nil {
		panic(err)
	}

	steps, _ := registry.StepNames("place_order")
	fmt.Println(steps)

	// Output: [charge_card reserve_inventory notify_partner]
}

// ExampleStep_unretryable shows how a step says "this will not become true by
// waiting". The remaining attempts are skipped and compensation starts at once.
func ExampleStep_unretryable() {
	charge := func(context.Context, *Booking) error {
		// A declined card is a decision, not a hiccup. Retrying it twenty-five
		// times only delays the refund, so the remaining attempts are skipped
		// and compensation starts at once.
		return retry.Unretryable(fmt.Errorf("the card was declined"))
	}

	step := saga.Step[Booking]{Name: "charge_card", Do: charge}

	fmt.Println(step.Name, "->", step.Do(context.Background(), &Booking{}))

	// Output: charge_card -> unretryable: the card was declined
}

// ExampleStatus_Terminal shows which statuses a worker will not move out of.
// StatusStuck is terminal in that sense while still being the one status
// Runner.Resume accepts.
func ExampleStatus_Terminal() {
	for _, status := range []saga.Status{
		saga.StatusRunning,
		saga.StatusCompensating,
		saga.StatusCompleted,
		saga.StatusCompensated,
		saga.StatusStuck,
	} {
		fmt.Printf("%s: %t\n", status, status.Terminal())
	}

	// Output:
	// running: false
	// compensating: false
	// completed: true
	// compensated: true
	// stuck: true
}
