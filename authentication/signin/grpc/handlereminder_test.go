package grpc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// reminderMailbox keeps the handles the reminder door mailed.
type reminderMailbox struct {
	handles []string
	mu      sync.Mutex
}

var _ signin.HandleReminderMailer = (*reminderMailbox)(nil)

func (m *reminderMailbox) SendHandleReminder(_ context.Context, mail *signin.HandleReminderMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handles = append(m.handles, mail.User.Username)

	return nil
}

func (m *reminderMailbox) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.handles...)
}

// newHandleReminderHarness is newHarness with the handle reminder door wired in.
// The floor is a nanosecond for the reason buildHarness gives the sign-in link
// door's; what it protects is asserted in the service's own tests.
func newHandleReminderHarness(t *testing.T) (*harness, *reminderMailbox) {
	t.Helper()

	mailbox := &reminderMailbox{}

	return newHarness(t, []signin.ServiceOption{
		signin.WithHandleReminderMailer(mailbox),
		signin.WithHandleReminderFloor(time.Nanosecond),
	}), mailbox
}

// TestRequestHandleReminderSaysNothingAboutAnAddress is the enumeration defense
// at the transport: an address nobody holds and one somebody does produce the
// same empty response, and only the second is mailed. The request carries no
// caller, which is the anonymous declaration it rests on.
func TestRequestHandleReminderSaysNothingAboutAnAddress(T *testing.T) {
	T.Parallel()

	h, mailbox := newHandleReminderHarness(T)

	stranger, err := h.client.RequestHandleReminder(h.rootCtx, &signinpb.RequestHandleReminderRequest{
		EmailAddress: "nobody@example.com",
	})
	must.NoError(T, err)
	test.SliceEmpty(T, mailbox.sent())

	known, err := h.client.RequestHandleReminder(h.rootCtx, &signinpb.RequestHandleReminderRequest{
		EmailAddress: h.user.EmailAddress,
	})
	must.NoError(T, err)
	test.Eq(T, []string{h.user.Username}, mailbox.sent())

	test.True(T, proto.Equal(stranger, known))
}

// TestRequestHandleReminderReportsAnUnwiredDoor pins that a service built with
// no mailer answers Internal rather than the silence a stranger's address gets.
func TestRequestHandleReminderReportsAnUnwiredDoor(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	_, err := h.client.RequestHandleReminder(h.rootCtx, &signinpb.RequestHandleReminderRequest{
		EmailAddress: h.user.EmailAddress,
	})
	test.EqOp(T, codes.Internal, status.Code(err))
}
