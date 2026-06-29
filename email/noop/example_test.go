package noop_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v2/email"
	"github.com/primandproper/platform-go/v2/email/noop"
)

func ExampleNewEmailer() {
	emailer, err := noop.NewEmailer()
	if err != nil {
		panic(err)
	}

	err = emailer.SendEmail(context.Background(), &email.OutboundEmailMessage{
		ToAddress:   "user@example.com",
		ToName:      "User",
		FromAddress: "noreply@example.com",
		FromName:    "App",
		Subject:     "Welcome!",
		HTMLContent: "<h1>Hello</h1>",
	})

	fmt.Println(err)
	// Output: <nil>
}
