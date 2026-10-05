package signin

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/conformance"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/proto"
)

// requestReminder asks for the handle an address signs in with, as the form
// nobody has signed in to does.
func requestReminder(t *testing.T, anon signinpb.SignInServiceClient, emailAddress string) *signinpb.RequestHandleReminderResponse {
	t.Helper()

	response, err := anon.RequestHandleReminder(t.Context(), &signinpb.RequestHandleReminderRequest{EmailAddress: emailAddress})
	must.NoError(t, err, must.Sprint("requesting a handle reminder"))

	return response
}

func handleReminders(t *testing.T, s *conformance.Session) {
	t.Helper()

	// Once, up front, for magicLinks' reason: a deployment that supplies no
	// HandleReminder reminds nobody, and against one with no mailer the door
	// answers Internal, which is not a promise this suite may hold it to.
	read := s.Seams().Actions.HandleReminder
	s.NeedsAction(t, read != nil, "handle reminder")

	// The flow the door was added for: somebody who knows their address and
	// not what they sign in as asks, with nobody on the client, and is mailed
	// the handle the password door takes.
	t.Run("a known address is mailed the handle it signs in with", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, requestHandleReminder)
		who, _ := register(t, s, withPassword(registrationRequest()))

		requestReminder(t, anon, who.email)

		handle, err := read(t.Context(), tenancy.Global(), who.email)
		must.NoError(t, err, must.Sprint("the deployment mailed no handle to an address somebody holds"))
		test.StrEqFold(t, who.username, handle)
	})

	// The enumeration defense at the transport: an address nobody holds and one
	// somebody does get the same answer. The control is that the known address
	// really was mailed and the unknown one was not, since two identical
	// answers from a deployment that mails nobody would prove nothing.
	t.Run("a request for an unknown address is answered as one for a known address", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, requestHandleReminder)
		who, _ := register(t, s, withPassword(registrationRequest()))

		known := requestReminder(t, anon, who.email)

		_, err := read(t.Context(), tenancy.Global(), who.email)
		must.NoError(t, err, must.Sprint("the control: the deployment mailed no handle to an address somebody holds"))

		stranger := freshEmail()
		unknown := requestReminder(t, anon, stranger)

		test.True(t, proto.Equal(known, unknown),
			test.Sprint("a handle reminder request told a known address apart from an unknown one"))

		_, err = read(t.Context(), tenancy.Global(), stranger)
		test.Error(t, err, test.Sprint("the deployment mailed a handle to an address nobody holds"))
	})
}
