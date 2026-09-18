package push_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/notifications/push"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The directory these tests fan out in, and the two people in it. Two, because
// the read the fan-out makes is the one that takes several principals at once.
var testScope = tenancy.Of("acct_1")

const (
	firstPrincipal  = "user_1"
	secondPrincipal = "user_2"
)

// The failures these tests stand in for, each named for what it is rather than
// for the branch it exercises.
var (
	// errRegistryUnavailable is the resolve failing — the one failure that
	// leaves nothing sent.
	errRegistryUnavailable = platformerrors.New("the device registry is unavailable")

	// errProviderUnreachable is a push that failed for a reason that says
	// nothing about the token: APNs was down, the network went. The row stays.
	errProviderUnreachable = platformerrors.New("the push provider is unreachable")

	// errPruneRefused is the registry refusing the prune after the provider has
	// already called the token dead.
	errPruneRefused = platformerrors.New("the device token prune was refused")

	// errInstrumentUnavailable stands in for a metrics provider that cannot
	// build an instrument.
	errInstrumentUnavailable = platformerrors.New("the push fan-out instrument is unavailable")
)

// deadToken is what a provider adapter answers with when the handset is gone:
// the typed sentinel wrapped around the provider's own words, which is the shape
// notifications/mobile documents and the only thing this package matches on.
func deadToken(words string) error {
	return platformerrors.Wrap(mobile.ErrTokenInvalid, words)
}

// device is one registration, spelled here so a test names only what it is
// about.
func device(principal string, platform notifications.Platform, token string) *notifications.Device {
	return &notifications.Device{
		ID:        token + "-id",
		Principal: principal,
		Token:     token,
		Platform:  platform,
		Scope:     testScope,
	}
}

// sentPush is one call the fan-out made on the sender.
type sentPush struct {
	Platform string
	Token    string
	Message  mobile.PushMessage
}

// stubSender is a mobile.PushNotificationSender that records what it was asked
// to send and answers per token.
//
// It is hand-written rather than generated because there is no mock for this
// interface on either side of the split, and what these tests need of it —
// "answer this token with that error" — is a map rather than a call list.
type stubSender struct {
	answers map[string]error
	sent    []sentPush
}

var _ mobile.PushNotificationSender = (*stubSender)(nil)

func newStubSender(answers map[string]error) *stubSender {
	return &stubSender{answers: answers}
}

// SendPush records the call and answers with whatever this token was given. The
// fan-out sends one handset at a time, so nothing here is reached concurrently.
func (s *stubSender) SendPush(_ context.Context, platform, token string, msg mobile.PushMessage) error {
	s.sent = append(s.sent, sentPush{Platform: platform, Token: token, Message: msg})

	return s.answers[token]
}

// tokensSent is what the sender was handed, in order.
func (s *stubSender) tokensSent() []string {
	tokens := make([]string, 0, len(s.sent))
	for i := range s.sent {
		tokens = append(tokens, s.sent[i].Token)
	}

	return tokens
}

// resolving is a registry that answers the fan-out's read with these devices and
// records every prune. Everything else on the seam is left nil, so a fan-out
// that reached for it fails loudly rather than quietly.
func resolving(devices []*notifications.Device, pruneErr error) *notificationsmock.RegistryMock {
	return &notificationsmock.RegistryMock{
		ListDevicesByPrincipalsFunc: func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			[]string,
		) ([]*notifications.Device, error) {
			return devices, nil
		},
		InvalidateDeviceTokenFunc: func(context.Context, string, string) error { return pruneErr },
	}
}

// newFanout builds a fan-out over a registry and a sender, refusing to continue
// if construction failed.
func newFanout(
	t *testing.T,
	registry notifications.Registry,
	sender mobile.PushNotificationSender,
	opts ...push.Option,
) *push.Fanout {
	t.Helper()

	fanout, err := push.NewFanout(registry, sender, opts...)
	must.NoError(t, err)

	return fanout
}

// testMessage is what every fan-out below sends.
var testMessage = mobile.PushMessage{Title: "Your order shipped", Body: "Arriving Thursday."}

// testReader is the executor the fan-out's read is made with.
//
// Nothing executes through it: the registry beneath the fan-out is a mock, and
// what these tests assert is that the executor the caller handed over is the one
// the fan-out passed down. database.SQLQueryExecutor has no unexported methods,
// so unlike database.Tx a test can stand in for it.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the fan-out's registry is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the fan-out's registry is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the fan-out's registry is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the fan-out's registry is a mock; nothing runs on this")
}

// reader is the executor every fan-out below reads with, named once so a test
// can assert that it is the one that reached the registry.
var reader = &testReader{}
