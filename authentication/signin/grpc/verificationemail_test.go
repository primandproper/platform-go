package grpc_test

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// verificationMailbox keeps the verification mail the server's service sends,
// which is the only place the link's secret goes.
type verificationMailbox struct {
	sent []*signin.VerificationMail
	mu   sync.Mutex
}

func (m *verificationMailbox) SendVerification(_ context.Context, mail *signin.VerificationMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sent = append(m.sent, mail)

	return nil
}

func (m *verificationMailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

func TestServer_RequestVerificationEmail(T *testing.T) {
	T.Parallel()

	// jane is registered in good standing with an address nobody has proven,
	// which is the person a resend is for.
	T.Run("mails the caller a link that verifies them, and then refuses", func(t *testing.T) {
		t.Parallel()

		mailbox := &verificationMailbox{}
		h := newHarness(t, []signin.ServiceOption{signin.WithVerificationMailer(mailbox)})

		response, err := h.client.RequestVerificationEmail(h.asJane(), &signinpb.RequestVerificationEmailRequest{})
		must.NoError(t, err)
		must.NotNil(t, response)

		must.EqOp(t, 1, mailbox.count())
		mail := mailbox.sent[0]
		test.EqOp(t, h.user.ID, mail.User.ID)
		test.NotEq(t, "", mail.Token)

		_, err = h.client.VerifyEmailAddress(h.rootCtx, &signinpb.VerifyEmailAddressRequest{Token: mail.Token})
		must.NoError(t, err)

		// Proven now, so a second request is refused and mails nothing.
		_, err = h.client.RequestVerificationEmail(h.asJane(), &signinpb.RequestVerificationEmailRequest{})
		test.ErrorIs(t, err, signin.ErrEmailAddressAlreadyVerified)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.EqOp(t, 1, mailbox.count())
	})

	// It has nothing but the principal to read the subject off, so an anonymous
	// request has nobody to mail.
	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		mailbox := &verificationMailbox{}
		h := newHarness(t, []signin.ServiceOption{signin.WithVerificationMailer(mailbox)})

		_, err := h.client.RequestVerificationEmail(h.rootCtx, &signinpb.RequestVerificationEmailRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, 0, mailbox.count())
	})
}
