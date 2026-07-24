package noop

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v6/email"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmailer()
		must.NoError(t, err)
		test.NotNil(t, e)
	})
}

func TestEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmailer()
		must.NoError(t, err)

		err = e.SendEmail(context.Background(), &email.OutboundEmailMessage{
			ToAddress:   "test@example.com",
			Subject:     "Test",
			HTMLContent: "<p>hello</p>",
		})
		test.NoError(t, err)
	})

	T.Run("with nil message", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmailer()
		must.NoError(t, err)

		err = e.SendEmail(context.Background(), nil)
		test.NoError(t, err)
	})
}
