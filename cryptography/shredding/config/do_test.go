package shreddingcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, newMigratedClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		store, err := do.Invoke[shredding.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestRegisterKeys(T *testing.T) {
	T.Parallel()

	T.Run("wires without a publisher provider", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, newMigratedClient(t))
		do.ProvideValue(i, newTestWrapper(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)
		RegisterKeys(i)

		keys, err := do.Invoke[shredding.Keys](i)
		must.NoError(t, err)
		must.NotNil(t, keys)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		_, err = keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		_, err = keys.Decrypt(t.Context(), testSubject, sealed, nil)
		test.ErrorIs(t, err, shredding.ErrSubjectShredded)
	})

	T.Run("announces shreds when a publisher provider is registered", func(t *testing.T) {
		t.Parallel()

		var topic string

		published := make(chan any, 1)
		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(_ context.Context, name string) (messagequeue.Publisher, error) {
				topic = name

				return &messagequeuemock.PublisherMock{
					PublishFunc: func(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
						published <- data

						return nil
					},
				}, nil
			},
		}

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, newMigratedClient(t))
		do.ProvideValue(i, newTestWrapper(t))
		do.ProvideValue[messagequeue.PublisherProvider](i, provider)
		do.ProvideValue(i, &Config{})

		RegisterStore(i)
		RegisterKeys(i)

		keys, err := do.Invoke[shredding.Keys](i)
		must.NoError(t, err)

		_, err = keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		test.EqOp(t, shredding.DefaultInvalidationTopic, topic)
		test.Eq(t, any(testSubject), <-published)
	})
}
