package identity

// time/tzdata is imported blank so the time zone cases below do not depend on
// the test host carrying a zoneinfo database; it affects this test binary only.
//
// The note sits outside the import block because the two formatters disagree
// about a comment inside one: goimports wants a blank line above it, gci wants
// the standard-library section contiguous and takes that line back out. Since
// `make format` runs goimports and then gci, a comment in there is rewritten
// twice on every run and the file is reported as reformatted when nothing
// about it actually changed.
import (
	"errors"
	"testing"
	"time"
	_ "time/tzdata"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestAccountStatus(T *testing.T) {
	T.Parallel()

	T.Run("closed set", func(t *testing.T) {
		t.Parallel()

		for _, status := range []AccountStatus{StatusUnverified, StatusGood, StatusBanned, StatusTerminated} {
			test.True(t, status.Valid(), test.Sprintf("%q", status))
			test.EqOp(t, string(status), status.String())
		}

		// A column holding a near-miss is a value nothing should switch on.
		for _, status := range []AccountStatus{"", "banned ", "GOOD", "active"} {
			test.False(t, status.Valid(), test.Sprintf("%q", status))
		}
	})

	T.Run("only good standing admits sign-in", func(t *testing.T) {
		t.Parallel()

		test.True(t, StatusGood.AdmitsSignIn())

		// The second copy of this rule would be the one that admits a banned
		// user, which is why it is a method rather than a comparison.
		for _, status := range []AccountStatus{StatusUnverified, StatusBanned, StatusTerminated, "nonsense"} {
			test.False(t, status.AdmitsSignIn(), test.Sprintf("%q", status))
		}
	})
}

func TestBillingStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []BillingStatus{BillingUnpaid, BillingTrial, BillingPaid, BillingSuspended} {
		test.True(t, status.Valid(), test.Sprintf("%q", status))
		test.EqOp(t, string(status), status.String())
	}

	for _, status := range []BillingStatus{"", "free", "PAID"} {
		test.False(t, status.Valid(), test.Sprintf("%q", status))
	}
}

func TestInvitationStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []InvitationStatus{InvitationPending, InvitationAccepted, InvitationRejected, InvitationCancelled} {
		test.True(t, status.Valid(), test.Sprintf("%q", status))
	}

	test.True(t, InvitationPending.Pending())
	test.False(t, InvitationAccepted.Pending())
	test.False(t, InvitationRejected.Pending())
	test.False(t, InvitationCancelled.Pending())
}

func TestAgreement(t *testing.T) {
	t.Parallel()

	test.True(t, TermsOfService.Valid())
	test.True(t, PrivacyPolicy.Valid())
	test.False(t, Agreement("cookies").Valid())
	test.False(t, Agreement("").Valid())
}

func TestUser_Redacted(T *testing.T) {
	T.Parallel()

	T.Run("clears every credential", func(t *testing.T) {
		t.Parallel()

		user := &User{
			ID:                            "u1",
			Username:                      "ada",
			HashedPassword:                "argon2$secret",
			TwoFactorSecret:               "TOTPSECRET",
			EmailAddressVerificationToken: "tok",
			ServiceRoles:                  []string{"service_admin"},
		}

		redacted := user.Redacted()
		test.EqOp(t, "", redacted.HashedPassword)
		test.EqOp(t, "", redacted.TwoFactorSecret)
		test.EqOp(t, "", redacted.EmailAddressVerificationToken)
		test.EqOp(t, "ada", redacted.Username)

		// The original is untouched: a caller redacting for a response body
		// still holds the value it read.
		test.EqOp(t, "argon2$secret", user.HashedPassword)

		// The roles are cloned, so mutating the copy cannot reach through.
		redacted.ServiceRoles[0] = "root"
		test.EqOp(t, "service_admin", user.ServiceRoles[0])
	})

	T.Run("nil redacts to nil", func(t *testing.T) {
		t.Parallel()

		var user *User
		test.Nil(t, user.Redacted())
	})
}

func TestUser_Predicates(T *testing.T) {
	T.Parallel()

	now := time.Now().UTC()

	T.Run("two factor needs a secret that was proven", func(t *testing.T) {
		t.Parallel()

		// A secret that has been issued and never proven is a QR code somebody
		// may have closed, which is why the check is not "is the secret set".
		test.False(t, (&User{TwoFactorSecret: "SECRET"}).TwoFactorEnabled())
		test.False(t, (&User{TwoFactorSecretVerifiedAt: &now}).TwoFactorEnabled())
		test.True(t, (&User{TwoFactorSecret: "SECRET", TwoFactorSecretVerifiedAt: &now}).TwoFactorEnabled())

		var nilUser *User
		test.False(t, nilUser.TwoFactorEnabled())
		test.False(t, nilUser.EmailAddressVerified())
		test.False(t, nilUser.Archived())
		test.False(t, nilUser.HasPassword())
	})

	T.Run("a password is a hash that is there", func(t *testing.T) {
		t.Parallel()

		// The question a password sign-in asks before it compares: an engine
		// handed an empty hash has no way to know it was never set.
		test.False(t, (&User{}).HasPassword())
		test.True(t, (&User{HashedPassword: "argon2$x"}).HasPassword())

		// Redaction clears the hash, so a redacted user reports false. That is
		// why this is a sign-in flow's question and not a profile page's.
		test.False(t, (&User{HashedPassword: "argon2$x"}).Redacted().HasPassword())
	})
}

func TestUser_Validate(T *testing.T) {
	T.Parallel()

	valid := func() *User {
		return &User{
			Scope:          tenancy.Global(),
			Username:       "ada",
			EmailAddress:   "ada@example.com",
			HashedPassword: "argon2$x",
			AccountStatus:  StatusGood,
		}
	}

	T.Run("accepts a complete user", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, valid().ValidateWithContext(t.Context()))
	})

	T.Run("requires a scope", func(t *testing.T) {
		t.Parallel()

		user := valid()
		user.Scope = tenancy.Scope{}

		// A user that says nothing about which directory it belongs to is one an
		// application registered by accident.
		must.ErrorIs(t, user.ValidateWithContext(t.Context()), tenancy.ErrNoScope)
	})

	T.Run("accepts a user with no password", func(t *testing.T) {
		t.Parallel()

		user := valid()
		user.HashedPassword = ""

		// A passkey-only or federated registration produces somebody there is
		// no hash to store for, and the requirement this replaced never caught
		// what it was written for: a plaintext password is a non-empty string
		// and passed.
		must.NoError(t, user.ValidateWithContext(t.Context()))
	})

	T.Run("refuses an unknown status", func(t *testing.T) {
		t.Parallel()

		user := valid()
		user.AccountStatus = "active"

		must.ErrorIs(t, user.ValidateWithContext(t.Context()), platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("refuses an address that is not bare", func(t *testing.T) {
		t.Parallel()

		user := valid()
		user.EmailAddress = "Ada <ada@example.com>"

		err := user.ValidateWithContext(t.Context())
		must.Error(t, err)

		// The column is a unique key and is compared against what a sign-in form
		// submits, so the display-name form would store a value that never
		// matches.
		var fieldErrs validation.Errors
		must.True(t, errors.As(err, &fieldErrs))
		must.ErrorIs(t, fieldErrs["emailAddress"], ErrInvalidEmailAddress)
	})

	T.Run("refuses a malformed address", func(t *testing.T) {
		t.Parallel()

		for _, address := range []string{"not-an-address", "ada@", "@example.com", "ada @example.com"} {
			user := valid()
			user.EmailAddress = address

			must.Error(t, user.ValidateWithContext(t.Context()), must.Sprintf("%q", address))
		}
	})

	T.Run("nil is an error, not a panic", func(t *testing.T) {
		t.Parallel()

		var user *User
		must.ErrorIs(t, user.ValidateWithContext(t.Context()), ErrNilUser)
		must.ErrorIs(t, user.validateProfile(t.Context()), ErrNilUser)
	})

	T.Run("the profile check covers what a profile write assigns", func(t *testing.T) {
		t.Parallel()

		// The status is AdminWriter's column, and a handler assembling a User
		// out of a profile form has no reason to name one. Nor a password.
		lean := &User{Scope: tenancy.Global(), Username: "ada", EmailAddress: "ada@example.com"}
		must.NoError(t, lean.validateProfile(t.Context()))
		must.ErrorIs(t, lean.ValidateWithContext(t.Context()), platformerrors.ErrUnrecognizedInputValue)

		// Narrower is not laxer about the columns it does assign: the address
		// rule is the same one registration is held to, so the two cannot
		// drift.
		noScope := valid()
		noScope.Scope = tenancy.Scope{}
		must.ErrorIs(t, noScope.validateProfile(t.Context()), tenancy.ErrNoScope)

		noUsername := valid()
		noUsername.Username = ""
		must.Error(t, noUsername.validateProfile(t.Context()))

		display := valid()
		display.EmailAddress = "Ada <ada@example.com>"
		must.Error(t, display.validateProfile(t.Context()))
	})

	T.Run("defaults to unverified", func(t *testing.T) {
		t.Parallel()

		user := &User{}
		user.EnsureDefaults()

		// A user who has proven nothing is the honest starting point; admitting
		// them by default is the mistake worth making unspellable.
		test.EqOp(t, StatusUnverified, user.AccountStatus)

		set := &User{AccountStatus: StatusGood}
		set.EnsureDefaults()
		test.EqOp(t, StatusGood, set.AccountStatus)

		var nilUser *User
		nilUser.EnsureDefaults()
	})
}

func TestPrincipal(T *testing.T) {
	T.Parallel()

	build := func() *Principal {
		return &Principal{
			User: &User{ID: "u1", ServiceRoles: []string{"service_admin"}},
			Memberships: []*Membership{
				{BelongsToAccount: "a1", DefaultAccount: true, Roles: []string{"account_admin"}},
				{BelongsToAccount: "a2", Roles: []string{"account_member"}},
			},
			ActiveAccountID: "a1",
		}
	}

	T.Run("resolves the active membership", func(t *testing.T) {
		t.Parallel()

		principal := build()
		must.NotNil(t, principal.ActiveMembership())
		test.EqOp(t, "a1", principal.ActiveMembership().BelongsToAccount)

		principal.ActiveAccountID = "a3"
		test.Nil(t, principal.ActiveMembership())
	})

	T.Run("roles are the union", func(t *testing.T) {
		t.Parallel()

		principal := build()
		test.Eq(t, []string{"account_admin"}, principal.AccountRoles())
		test.Eq(t, []string{"service_admin"}, principal.ServiceRoles())

		// Handing a PolicyResolver half the answer is how an operator's support
		// access stops working inside a customer's account.
		test.Eq(t, []string{"service_admin", "account_admin"}, principal.Roles())

		principal.ActiveAccountID = "a2"
		test.Eq(t, []string{"service_admin", "account_member"}, principal.Roles())
	})

	T.Run("lists accounts default first", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a1", "a2"}, build().AccountIDs())
	})

	T.Run("nil is empty, not a panic", func(t *testing.T) {
		t.Parallel()

		var principal *Principal
		test.Nil(t, principal.ActiveMembership())
		test.SliceEmpty(t, principal.Roles())
		test.Nil(t, principal.AccountIDs())
		test.Nil(t, principal.ServiceRoles())
		test.Nil(t, principal.AccountRoles())
	})
}

func TestMembership(T *testing.T) {
	T.Parallel()

	T.Run("validates", func(t *testing.T) {
		t.Parallel()

		valid := &Membership{
			Scope:            tenancy.Global(),
			BelongsToUser:    "u1",
			BelongsToAccount: "a1",
			Roles:            []string{"account_member"},
		}
		must.NoError(t, valid.ValidateWithContext(t.Context()))

		// A membership with no roles is a user who belongs to an account and may
		// do nothing in it.
		noRoles := *valid
		noRoles.Roles = nil
		must.Error(t, noRoles.ValidateWithContext(t.Context()))

		emptyRole := *valid
		emptyRole.Roles = []string{""}
		must.ErrorIs(t, emptyRole.ValidateWithContext(t.Context()), platformerrors.ErrEmptyInputParameter)

		noScope := *valid
		noScope.Scope = tenancy.Scope{}
		must.ErrorIs(t, noScope.ValidateWithContext(t.Context()), tenancy.ErrNoScope)

		var nilMembership *Membership
		must.ErrorIs(t, nilMembership.ValidateWithContext(t.Context()), ErrNilMembership)
	})

	T.Run("predicates", func(t *testing.T) {
		t.Parallel()

		membership := &Membership{Roles: []string{"a", "b"}}
		test.False(t, membership.Archived())

		var nilMembership *Membership
		test.False(t, nilMembership.Archived())
	})
}

func TestInvitation(T *testing.T) {
	T.Parallel()

	valid := func() *Invitation {
		return &Invitation{
			Scope:            tenancy.Global(),
			BelongsToAccount: "a1",
			FromUser:         "u1",
			ToEmail:          "brian@example.com",
			Token:            "tok",
			Status:           InvitationPending,
			ExpiresAt:        baseTime.Add(time.Hour),
			Roles:            []string{"account_member"},
		}
	}

	T.Run("validates", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, valid().ValidateWithContext(t.Context()))

		// An invitation link is a bearer credential for joining somebody else's
		// account; one that never expires is still valid in a mailbox somebody
		// lost control of two years ago.
		noExpiry := valid()
		noExpiry.ExpiresAt = time.Time{}
		must.ErrorIs(t, noExpiry.ValidateWithContext(t.Context()), platformerrors.ErrEmptyInputParameter)

		noToken := valid()
		noToken.Token = ""
		must.Error(t, noToken.ValidateWithContext(t.Context()))

		noRoles := valid()
		noRoles.Roles = nil
		must.Error(t, noRoles.ValidateWithContext(t.Context()))

		// A role nobody can hold. The invitation would be accepted and write a
		// membership carrying it, and the escalation-shaped failure surfaces at
		// the recipient's first permission check rather than here.
		emptyRole := valid()
		emptyRole.Roles = []string{"account_member", ""}
		must.ErrorIs(t, emptyRole.ValidateWithContext(t.Context()), platformerrors.ErrEmptyInputParameter)

		badStatus := valid()
		badStatus.Status = "unknown"
		must.ErrorIs(t, badStatus.ValidateWithContext(t.Context()), platformerrors.ErrUnrecognizedInputValue)

		var nilInvitation *Invitation
		must.ErrorIs(t, nilInvitation.ValidateWithContext(t.Context()), ErrNilInvitation)
	})

	T.Run("expiry is inclusive of the instant it names", func(t *testing.T) {
		t.Parallel()

		invitation := valid()
		test.False(t, invitation.Expired(baseTime))
		test.True(t, invitation.Expired(invitation.ExpiresAt))
		test.True(t, invitation.Expired(invitation.ExpiresAt.Add(time.Second)))

		var nilInvitation *Invitation
		test.False(t, nilInvitation.Expired(baseTime))
	})

	T.Run("redacts the token", func(t *testing.T) {
		t.Parallel()

		redacted := valid().Redacted()
		test.EqOp(t, "", redacted.Token)
		test.EqOp(t, "brian@example.com", redacted.ToEmail)

		var nilInvitation *Invitation
		test.Nil(t, nilInvitation.Redacted())
	})

	T.Run("defaults to pending", func(t *testing.T) {
		t.Parallel()

		invitation := &Invitation{}
		invitation.EnsureDefaults()
		test.EqOp(t, InvitationPending, invitation.Status)

		var nilInvitation *Invitation
		nilInvitation.EnsureDefaults()
	})
}

func TestBillingAddress(t *testing.T) {
	t.Parallel()

	// An application that never collects an address leaves a zero value rather
	// than seven empty strings, which is what makes this check one comparison.
	empty, filled := BillingAddress{}, BillingAddress{City: "London"}

	test.True(t, empty.Zero())
	test.False(t, filled.Zero())
}

func TestAccount_Validate(T *testing.T) {
	T.Parallel()

	valid := func() *Account {
		return &Account{
			Scope:         tenancy.Global(),
			Name:          "Acme",
			OwnerUserID:   "u1",
			BillingStatus: BillingUnpaid,
		}
	}

	T.Run("accepts a complete account", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, valid().ValidateWithContext(t.Context()))
	})

	T.Run("requires an owner", func(t *testing.T) {
		t.Parallel()

		// An account with no owner is one whose every ownership-derived
		// permission check resolves to nobody.
		ownerless := valid()
		ownerless.OwnerUserID = ""
		must.Error(t, ownerless.ValidateWithContext(t.Context()))
	})

	T.Run("requires a scope and a known status", func(t *testing.T) {
		t.Parallel()

		noScope := valid()
		noScope.Scope = tenancy.Scope{}
		must.ErrorIs(t, noScope.ValidateWithContext(t.Context()), tenancy.ErrNoScope)

		badStatus := valid()
		badStatus.BillingStatus = "free"
		must.ErrorIs(t, badStatus.ValidateWithContext(t.Context()), platformerrors.ErrUnrecognizedInputValue)

		var nilAccount *Account
		must.ErrorIs(t, nilAccount.ValidateWithContext(t.Context()), ErrNilAccount)
	})

	T.Run("defaults to unpaid", func(t *testing.T) {
		t.Parallel()

		account := &Account{}
		account.EnsureDefaults()
		test.EqOp(t, BillingUnpaid, account.BillingStatus)

		var nilAccount *Account
		nilAccount.EnsureDefaults()
		test.False(t, nilAccount.Archived())
	})
}

func TestAccount_TimeZone(T *testing.T) {
	T.Parallel()

	validate := func(t *testing.T, zone string) error {
		t.Helper()

		account := &Account{
			ID: identifiers.New(), Scope: testScope, Name: "Acme",
			OwnerUserID: identifiers.New(), BillingStatus: BillingUnpaid,
			TimeZone: zone,
		}

		return account.ValidateWithContext(t.Context())
	}

	T.Run("accepts a loadable zone", func(t *testing.T) {
		t.Parallel()
		test.NoError(t, validate(t, "America/Chicago"))
	})

	T.Run("accepts none stated", func(t *testing.T) {
		t.Parallel()

		// An application in one region never sets this, and refusing the write
		// would make a zone a required field for everybody who has no use for
		// one.
		test.NoError(t, validate(t, ""))
	})

	T.Run("refuses a name that does not load", func(t *testing.T) {
		t.Parallel()

		// The typo is the whole point: it looks right, and stored it would
		// render every date on the account wrong without anything saying so.
		err := validate(t, "America/Chicagoo")

		var fieldErrs validation.Errors
		must.True(t, errors.As(err, &fieldErrs))
		must.ErrorIs(t, fieldErrs["timeZone"], ErrInvalidTimeZone)
		test.ErrorIs(t, fieldErrs["timeZone"], platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("refuses Local", func(t *testing.T) {
		t.Parallel()

		// It loads, so only an explicit refusal catches it — and what it loads
		// is the reader's TZ environment variable, which makes the same stored
		// value mean different things on two replicas of one service.
		err := validate(t, "Local")

		var fieldErrs validation.Errors
		must.True(t, errors.As(err, &fieldErrs))
		must.ErrorIs(t, fieldErrs["timeZone"], ErrInvalidTimeZone)
	})

	T.Run("resolves to UTC when unstated", func(t *testing.T) {
		t.Parallel()

		loc, err := (&Account{}).Location()
		must.NoError(t, err)
		test.EqOp(t, time.UTC, loc)

		// Nil is the case a caller reaches through a not-found read it did not
		// check, and UTC is a better answer there than a panic.
		loc, err = (*Account)(nil).Location()
		must.NoError(t, err)
		test.EqOp(t, time.UTC, loc)
	})

	T.Run("resolves a stated zone", func(t *testing.T) {
		t.Parallel()

		loc, err := (&Account{TimeZone: "America/Chicago"}).Location()
		must.NoError(t, err)
		test.EqOp(t, "America/Chicago", loc.String())
	})

	T.Run("returns UTC alongside the error", func(t *testing.T) {
		t.Parallel()

		// A caller that would rather degrade than fail uses the location
		// regardless, so it must never be nil.
		loc, err := (&Account{TimeZone: "America/Chicagoo"}).Location()
		must.Error(t, err)
		test.ErrorIs(t, err, ErrInvalidTimeZone)
		must.NotNil(t, loc)
		test.EqOp(t, time.UTC, loc)
	})
}
