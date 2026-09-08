package identity

import (
	"time"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// identity/internal/identitydb is sqlc-gen-unison's output: one params and one
// row struct per statement, the same on all three dialects. These functions are
// the whole of what this package does with them — a row becomes the domain
// type, a domain value becomes the params — and every one is a struct literal
// on purpose. A renamed or retyped column changes the generated struct, and
// every conversion here stops compiling; the scan-by-position pairing these
// replaced reported the same mistake as a runtime scan error, or worse, as two
// same-typed columns silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree — which is why the page converters
// restate the fields rather than casting. Restating is the cost; the compiler
// checking every field name is what it buys.

// utcPtr normalizes an optional timestamp to UTC, preserving absence. It is the
// one home for the rule, and every conversion below goes through it.
//
// Every timestamp this package writes is UTC, so every one it returns is too —
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the string parsed as, so a caller comparing two of those, or
// rendering one into JSON, would get an answer that depends on where the row
// was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// pageRow is one row of a rendered list query: the value, and the two counts
// the statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which
// is what makes a page and the number describing it come from one snapshot of
// the table. It also means a page with no rows carries no counts — see
// filtering.Drain, which is what reports that as unknown rather than as zero.
type pageRow[T any] struct {
	value    *T
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts[T any](row pageRow[T]) (filtered, total int64) {
	return row.filtered, row.total
}

// countOf narrows a COUNT that came back from its own statement to the unsigned
// pair filtering.Pagination reports.
//
// The rendered lists carry their counts on the rows and go through
// filtering.Drain, which narrows them itself. The prefix search's count is a
// second statement, so the narrowing is here — and it is not a guard against
// the database: a COUNT cannot be negative, and what it stops is the conversion
// turning one into a number larger than the table could hold.
func countOf(count int64) uint64 {
	if count < 0 {
		return 0
	}

	return uint64(count)
}

// pageValue reads the value off a row, for filtering.Drain. The value is
// returned as it stands rather than copied, so whatever a caller did to the
// slice of pointers before draining — attaching roles, redacting — is what the
// page carries.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// pageValues collects a page's values, for the passes a caller makes over them
// before draining.
func pageValues[T any](rows []pageRow[T]) []*T {
	values := make([]*T, 0, len(rows))
	for _, row := range rows {
		values = append(values, row.value)
	}

	return values
}

// listWindow is the filter window every generated list statement binds, in the
// shape the generated params carry it. One reading of the filter, restated into
// each nominal params type by the constructors below.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here,
// and it defaults to excluding, which is what the statement's COALESCE would
// have done with a NULL anyway — bound explicitly so the parameter is a bool
// rather than a pointer whose nil means the same thing.
//
// The UTC normalization on the four times is load-bearing on SQLite, not
// cosmetic. That column compares as text, the stored shape is UTC
// `YYYY-MM-DD HH:MM:SS`, and the driver renders a bound time.Time with its own
// zone's clock in exactly that prefix position — so a UTC value compares
// correctly to the second and a zoned one is off by its offset, silently.
// Verified empirically against modernc; if the generated sqlite bindings ever
// learn to format times themselves, this stays correct and merely stops being
// the only thing making it so.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text,
// not a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the six paged reads. A read that
// reached for the ascending statement while holding a descending filter would
// answer in the order the client did not ask for, and nothing about the rows
// that came back would say so.
//
// The descending rows are converted rather than restated field by field, which
// is the one place in this file that casts. The preamble's rule is about two
// projections that happen to agree — a list row and a get row — and these are
// not that: they are one projection rendered twice, with the walk reversed and
// nothing else changed. So the conversion is the assertion, and Go makes it the
// compiler's: the day the two projections stop being identical, in field name,
// type or order, this stops building rather than filling the wrong fields.
func sortedRows[Ascending, Descending any](
	filter *filtering.QueryFilter,
	ascending func() ([]Ascending, error),
	descending func() ([]Descending, error),
	same func(Descending) Ascending,
) ([]Ascending, error) {
	if !filter.SortsDescending() {
		return ascending()
	}

	rows, err := descending()
	if err != nil {
		return nil, err
	}

	page := make([]Ascending, 0, len(rows))
	for i := range rows {
		page = append(page, same(rows[i]))
	}

	return page, nil
}

// Users.

func userFromRow(r *identitydb.GetUserRow) *User {
	return &User{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         utcPtr(r.PasswordLastChangedAt),
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     utcPtr(r.TwoFactorSecretVerifiedAt),
		EmailAddressVerifiedAt:        utcPtr(r.EmailAddressVerifiedAt),
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 AccountStatus(r.AccountStatus),
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    utcPtr(r.LastAcceptedTermsOfService),
		LastAcceptedPrivacyPolicy:     utcPtr(r.LastAcceptedPrivacyPolicy),
		CreatedAt:                     r.CreatedAt.UTC(),
		LastUpdatedAt:                 utcPtr(r.LastUpdatedAt),
		ArchivedAt:                    utcPtr(r.ArchivedAt),
	}
}

// The three single-user reads keyed on something other than the id each have a
// row type of their own, because sqlc's row types are nominal per statement:
// two statements projecting the same twenty columns still produce two structs
// that cannot convert to one another.
//
// So each restates the fields into the get's row and converts from there. That
// is the cost the package comment describes, and what it buys is a compiler
// error at every one of these the day a column is renamed or retyped, rather
// than three reads that scan a row into the wrong fields.

func userFromUsernameRow(r *identitydb.GetUserByUsernameRow) *User {
	return userFromRow(&identitydb.GetUserRow{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         r.PasswordLastChangedAt,
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 r.AccountStatus,
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
		CreatedAt:                     r.CreatedAt,
		LastUpdatedAt:                 r.LastUpdatedAt,
		ArchivedAt:                    r.ArchivedAt,
	})
}

func userFromEmailAddressRow(r *identitydb.GetUserByEmailAddressRow) *User {
	return userFromRow(&identitydb.GetUserRow{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         r.PasswordLastChangedAt,
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 r.AccountStatus,
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
		CreatedAt:                     r.CreatedAt,
		LastUpdatedAt:                 r.LastUpdatedAt,
		ArchivedAt:                    r.ArchivedAt,
	})
}

func userFromEmailVerificationTokenRow(r *identitydb.GetUserByEmailVerificationTokenRow) *User {
	return userFromRow(&identitydb.GetUserRow{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         r.PasswordLastChangedAt,
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 r.AccountStatus,
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
		CreatedAt:                     r.CreatedAt,
		LastUpdatedAt:                 r.LastUpdatedAt,
		ArchivedAt:                    r.ArchivedAt,
	})
}

func userPageRow(r *identitydb.ListUsersRow) pageRow[User] {
	return pageRow[User]{
		value: userFromRow(&identitydb.GetUserRow{
			ID:                            r.ID,
			Scope:                         r.Scope,
			Username:                      r.Username,
			EmailAddress:                  r.EmailAddress,
			FirstName:                     r.FirstName,
			LastName:                      r.LastName,
			HashedPassword:                r.HashedPassword,
			RequiresPasswordChange:        r.RequiresPasswordChange,
			PasswordLastChangedAt:         r.PasswordLastChangedAt,
			TwoFactorSecret:               r.TwoFactorSecret,
			TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
			EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
			EmailAddressVerificationToken: r.EmailAddressVerificationToken,
			AccountStatus:                 r.AccountStatus,
			AccountStatusExplanation:      r.AccountStatusExplanation,
			LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
			LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
			CreatedAt:                     r.CreatedAt,
			LastUpdatedAt:                 r.LastUpdatedAt,
			ArchivedAt:                    r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

// userFromSearchRow is userPageRow's counterpart for the prefix search, whose
// row carries the projection and no counts — the count is its own statement.
func userFromSearchRow(r *identitydb.SearchUsersByUsernameRow) *User {
	return userFromRow(&identitydb.GetUserRow{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         r.PasswordLastChangedAt,
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 r.AccountStatus,
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
		CreatedAt:                     r.CreatedAt,
		LastUpdatedAt:                 r.LastUpdatedAt,
		ArchivedAt:                    r.ArchivedAt,
	})
}

func userFromBatchRow(r *identitydb.ListUsersByIDsRow) *User {
	return userFromRow(&identitydb.GetUserRow{
		ID:                            r.ID,
		Scope:                         r.Scope,
		Username:                      r.Username,
		EmailAddress:                  r.EmailAddress,
		FirstName:                     r.FirstName,
		LastName:                      r.LastName,
		HashedPassword:                r.HashedPassword,
		RequiresPasswordChange:        r.RequiresPasswordChange,
		PasswordLastChangedAt:         r.PasswordLastChangedAt,
		TwoFactorSecret:               r.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.EmailAddressVerificationToken,
		AccountStatus:                 r.AccountStatus,
		AccountStatusExplanation:      r.AccountStatusExplanation,
		LastAcceptedTermsOfService:    r.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.LastAcceptedPrivacyPolicy,
		CreatedAt:                     r.CreatedAt,
		LastUpdatedAt:                 r.LastUpdatedAt,
		ArchivedAt:                    r.ArchivedAt,
	})
}

// Roles.

// ownedRole is one row of a batched role read: the parent the role hangs off,
// and the role itself.
//
// The three role tables project the same two columns and still have three row
// types, because sqlc's row types are nominal per statement. Flattening each
// into this is what lets one grouping — SQLStore's rolesFor — serve all three
// rather than being written once per table, which is the shape the roles of a
// user, a membership and an invitation would otherwise arrive in.
type ownedRole struct {
	ownerID string
	role    string
}

func userRolesFromRows(rows []identitydb.ListUserRolesByUserIDsRow) []ownedRole {
	owned := make([]ownedRole, 0, len(rows))
	for i := range rows {
		owned = append(owned, ownedRole{ownerID: rows[i].UserID, role: rows[i].Role})
	}

	return owned
}

func membershipRolesFromRows(rows []identitydb.ListMembershipRolesByMembershipIDsRow) []ownedRole {
	owned := make([]ownedRole, 0, len(rows))
	for i := range rows {
		owned = append(owned, ownedRole{ownerID: rows[i].MembershipID, role: rows[i].Role})
	}

	return owned
}

func invitationRolesFromRows(rows []identitydb.ListInvitationRolesByInvitationIDsRow) []ownedRole {
	owned := make([]ownedRole, 0, len(rows))
	for i := range rows {
		owned = append(owned, ownedRole{ownerID: rows[i].InvitationID, role: rows[i].Role})
	}

	return owned
}

func createUserParams(u *User) identitydb.CreateUserParams {
	return identitydb.CreateUserParams{
		ID:                            u.ID,
		Scope:                         u.Scope,
		Username:                      u.Username,
		EmailAddress:                  u.EmailAddress,
		FirstName:                     u.FirstName,
		LastName:                      u.LastName,
		HashedPassword:                u.HashedPassword,
		RequiresPasswordChange:        u.RequiresPasswordChange,
		PasswordLastChangedAt:         u.PasswordLastChangedAt,
		TwoFactorSecret:               u.TwoFactorSecret,
		TwoFactorSecretVerifiedAt:     u.TwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        u.EmailAddressVerifiedAt,
		EmailAddressVerificationToken: u.EmailAddressVerificationToken,
		AccountStatus:                 u.AccountStatus.String(),
		AccountStatusExplanation:      u.AccountStatusExplanation,
		LastAcceptedTermsOfService:    u.LastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     u.LastAcceptedPrivacyPolicy,
	}
}

// updateUserParams is the profile update: four columns and two derived ones.
//
// verifiedAt and token are the caller's decision rather than the user's own
// fields, and they are decided together — moving an address clears the proof
// that went with it and burns the link that would have proved it, and only the
// store knows what the stored address was. Taking the token from the User would
// be worse than useless here: the copy a caller holds is usually a redacted one,
// whose token is the empty string, so every profile save would burn a live link.
// See SQLStore.UpdateUser.
func updateUserParams(u *User, verifiedAt *time.Time, token string) identitydb.UpdateUserParams {
	return identitydb.UpdateUserParams{
		ID:                            u.ID,
		Scope:                         u.Scope,
		Username:                      u.Username,
		EmailAddress:                  u.EmailAddress,
		FirstName:                     u.FirstName,
		LastName:                      u.LastName,
		EmailAddressVerifiedAt:        verifiedAt,
		EmailAddressVerificationToken: token,
	}
}

// searchUsersParams and countSearchUsersParams take the pattern rather than the
// prefix somebody typed, because the escaping is the caller's to have done: the
// statement's ESCAPE clause is meaningless over a pattern nothing escaped, and
// the two statements have to search for the same thing. See
// SQLStore.SearchUsersByUsername, which renders it once through
// querygen.PrefixPattern and hands it to both.
//
// The window is the same one the rendered lists bind, less the four time bounds
// the search's statement does not carry: the cursor is a username here, because
// the statement orders by that column.
func searchUsersParams(scope tenancy.Scope, pattern string, filter *filtering.QueryFilter) identitydb.SearchUsersByUsernameParams {
	w := windowFrom(filter)

	return identitydb.SearchUsersByUsernameParams{
		Scope:          scope,
		UsernamePrefix: pattern,
		PageCursor:     w.pageCursor,
		ResultLimit:    w.resultLimit,
	}
}

func countSearchUsersParams(scope tenancy.Scope, pattern string) identitydb.CountSearchUsersByUsernameParams {
	return identitydb.CountSearchUsersByUsernameParams{
		Scope:          scope,
		UsernamePrefix: pattern,
	}
}

func listUsersParams(scope tenancy.Scope, filter *filtering.QueryFilter) identitydb.ListUsersParams {
	w := windowFrom(filter)

	return identitydb.ListUsersParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// Accounts.

func accountFromRow(r *identitydb.GetAccountRow) *Account {
	return &Account{
		ID:                          r.ID,
		Scope:                       r.Scope,
		Name:                        r.Name,
		OwnerUserID:                 r.OwnerUserID,
		BillingStatus:               BillingStatus(r.BillingStatus),
		SubscriptionPlanID:          r.SubscriptionPlanID,
		PaymentProcessorCustomerID:  r.PaymentProcessorCustomerID,
		LastPaymentProviderSyncedAt: utcPtr(r.LastPaymentProviderSyncedAt),
		BillingAddress: BillingAddress{
			Line1:      r.AddressLine1,
			Line2:      r.AddressLine2,
			City:       r.AddressCity,
			State:      r.AddressState,
			PostalCode: r.AddressPostalCode,
			Country:    r.AddressCountry,
			Phone:      r.AddressPhone,
		},
		TimeZone:      r.TimeZone,
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
	}
}

func accountPageRow(r *identitydb.ListAccountsRow) pageRow[Account] {
	return pageRow[Account]{
		value: accountFromRow(&identitydb.GetAccountRow{
			ID:                          r.ID,
			Scope:                       r.Scope,
			Name:                        r.Name,
			OwnerUserID:                 r.OwnerUserID,
			BillingStatus:               r.BillingStatus,
			SubscriptionPlanID:          r.SubscriptionPlanID,
			PaymentProcessorCustomerID:  r.PaymentProcessorCustomerID,
			LastPaymentProviderSyncedAt: r.LastPaymentProviderSyncedAt,
			AddressLine1:                r.AddressLine1,
			AddressLine2:                r.AddressLine2,
			AddressCity:                 r.AddressCity,
			AddressState:                r.AddressState,
			AddressPostalCode:           r.AddressPostalCode,
			AddressCountry:              r.AddressCountry,
			AddressPhone:                r.AddressPhone,
			TimeZone:                    r.TimeZone,
			CreatedAt:                   r.CreatedAt,
			LastUpdatedAt:               r.LastUpdatedAt,
			ArchivedAt:                  r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createAccountParams(a *Account) identitydb.CreateAccountParams {
	return identitydb.CreateAccountParams{
		ID:                          a.ID,
		Scope:                       a.Scope,
		Name:                        a.Name,
		OwnerUserID:                 a.OwnerUserID,
		BillingStatus:               a.BillingStatus.String(),
		SubscriptionPlanID:          a.SubscriptionPlanID,
		PaymentProcessorCustomerID:  a.PaymentProcessorCustomerID,
		LastPaymentProviderSyncedAt: a.LastPaymentProviderSyncedAt,
		AddressLine1:                a.BillingAddress.Line1,
		AddressLine2:                a.BillingAddress.Line2,
		AddressCity:                 a.BillingAddress.City,
		AddressState:                a.BillingAddress.State,
		AddressPostalCode:           a.BillingAddress.PostalCode,
		AddressCountry:              a.BillingAddress.Country,
		AddressPhone:                a.BillingAddress.Phone,
		TimeZone:                    a.TimeZone,
	}
}

// updateAccountParams is the name and the address. Neither the owner nor any
// billing column is here — each has a write of its own, and an update that
// assigned them would let a profile save undo a processor webhook.
func updateAccountParams(a *Account) identitydb.UpdateAccountParams {
	return identitydb.UpdateAccountParams{
		ID:                a.ID,
		Scope:             a.Scope,
		Name:              a.Name,
		AddressLine1:      a.BillingAddress.Line1,
		AddressLine2:      a.BillingAddress.Line2,
		AddressCity:       a.BillingAddress.City,
		AddressState:      a.BillingAddress.State,
		AddressPostalCode: a.BillingAddress.PostalCode,
		AddressCountry:    a.BillingAddress.Country,
		AddressPhone:      a.BillingAddress.Phone,
		TimeZone:          a.TimeZone,
	}
}

func listAccountsParams(scope tenancy.Scope, filter *filtering.QueryFilter) identitydb.ListAccountsParams {
	w := windowFrom(filter)

	return identitydb.ListAccountsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// accountPageRowForUser converts one row of the accounts-through-memberships
// junction list.
//
// It restates the account columns a third time rather than converting from
// accountPageRow's row, because the generated row types are nominal per
// statement: two statements projecting the same columns in the same order still
// produce two types, and the compiler checking every field name at each seam is
// what these conversions are for.
func accountPageRowForUser(r *identitydb.ListAccountsForUserRow) pageRow[Account] {
	return pageRow[Account]{
		value: accountFromRow(&identitydb.GetAccountRow{
			ID:                          r.ID,
			Scope:                       r.Scope,
			Name:                        r.Name,
			OwnerUserID:                 r.OwnerUserID,
			BillingStatus:               r.BillingStatus,
			SubscriptionPlanID:          r.SubscriptionPlanID,
			PaymentProcessorCustomerID:  r.PaymentProcessorCustomerID,
			LastPaymentProviderSyncedAt: r.LastPaymentProviderSyncedAt,
			AddressLine1:                r.AddressLine1,
			AddressLine2:                r.AddressLine2,
			AddressCity:                 r.AddressCity,
			AddressState:                r.AddressState,
			AddressPostalCode:           r.AddressPostalCode,
			AddressCountry:              r.AddressCountry,
			AddressPhone:                r.AddressPhone,
			TimeZone:                    r.TimeZone,
			CreatedAt:                   r.CreatedAt,
			LastUpdatedAt:               r.LastUpdatedAt,
			ArchivedAt:                  r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func listAccountsForUserParams(scope tenancy.Scope, userID string, filter *filtering.QueryFilter) identitydb.ListAccountsForUserParams {
	w := windowFrom(filter)

	return identitydb.ListAccountsForUserParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		BelongsToUser:   userID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// Memberships.
//
// The membership read is keyed on the (user, account) pair rather than on the
// id — the table carries one but nothing addresses a row by it — so its row
// type is the keyed read's rather than a standard get's.

func membershipFromRow(r *identitydb.GetMembershipByUserAndAccountRow) *Membership {
	return &Membership{
		ID:               r.ID,
		Scope:            r.Scope,
		BelongsToUser:    r.BelongsToUser,
		BelongsToAccount: r.BelongsToAccount,
		DefaultAccount:   r.DefaultAccount,
		CreatedAt:        r.CreatedAt.UTC(),
		LastUpdatedAt:    utcPtr(r.LastUpdatedAt),
		ArchivedAt:       utcPtr(r.ArchivedAt),
	}
}

// membershipFromListRow converts one row of the unpaged memberships list. It
// restates the keyed read's row rather than sharing its type, because the
// generated row types are nominal per statement.
func membershipFromListRow(r *identitydb.ListMembershipsForUserRow) *Membership {
	return membershipFromRow(&identitydb.GetMembershipByUserAndAccountRow{
		ID:               r.ID,
		Scope:            r.Scope,
		BelongsToUser:    r.BelongsToUser,
		BelongsToAccount: r.BelongsToAccount,
		DefaultAccount:   r.DefaultAccount,
		CreatedAt:        r.CreatedAt,
		LastUpdatedAt:    r.LastUpdatedAt,
		ArchivedAt:       r.ArchivedAt,
	})
}

func listMembershipsForUserParams(scope tenancy.Scope, userID string) identitydb.ListMembershipsForUserParams {
	return identitydb.ListMembershipsForUserParams{Scope: scope, BelongsToUser: userID}
}

// memberPageRow converts one roster row: the membership, and the member it
// names under the projection's user_ prefix.
//
// The user is redacted here rather than at the call site because this is the
// value a roster page is made of, and a roster is the read most likely to reach
// a response body: thirty members is thirty chances to serve a password hash.
func memberPageRow(r *identitydb.ListAccountMembersRow) pageRow[MembershipWithUser] {
	user := userFromRow(&identitydb.GetUserRow{
		ID:                            r.UserID,
		Scope:                         r.UserScope,
		Username:                      r.UserUsername,
		EmailAddress:                  r.UserEmailAddress,
		FirstName:                     r.UserFirstName,
		LastName:                      r.UserLastName,
		HashedPassword:                r.UserHashedPassword,
		RequiresPasswordChange:        r.UserRequiresPasswordChange,
		PasswordLastChangedAt:         r.UserPasswordLastChangedAt,
		TwoFactorSecret:               r.UserTwoFactorSecret,
		TwoFactorSecretVerifiedAt:     r.UserTwoFactorSecretVerifiedAt,
		EmailAddressVerifiedAt:        r.UserEmailAddressVerifiedAt,
		EmailAddressVerificationToken: r.UserEmailAddressVerificationToken,
		AccountStatus:                 r.UserAccountStatus,
		AccountStatusExplanation:      r.UserAccountStatusExplanation,
		LastAcceptedTermsOfService:    r.UserLastAcceptedTermsOfService,
		LastAcceptedPrivacyPolicy:     r.UserLastAcceptedPrivacyPolicy,
		CreatedAt:                     r.UserCreatedAt,
		LastUpdatedAt:                 r.UserLastUpdatedAt,
		ArchivedAt:                    r.UserArchivedAt,
	})

	return pageRow[MembershipWithUser]{
		value: &MembershipWithUser{
			User:             user.Redacted(),
			ID:               r.ID,
			Scope:            r.Scope,
			BelongsToUser:    r.BelongsToUser,
			BelongsToAccount: r.BelongsToAccount,
			DefaultAccount:   r.DefaultAccount,
			CreatedAt:        r.CreatedAt.UTC(),
			LastUpdatedAt:    utcPtr(r.LastUpdatedAt),
			ArchivedAt:       utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func listAccountMembersParams(scope tenancy.Scope, accountID string, filter *filtering.QueryFilter) identitydb.ListAccountMembersParams {
	w := windowFrom(filter)

	return identitydb.ListAccountMembersParams{
		CreatedAfter:     w.createdAfter,
		CreatedBefore:    w.createdBefore,
		UpdatedAfter:     w.updatedAfter,
		UpdatedBefore:    w.updatedBefore,
		IncludeArchived:  w.includeArchived,
		Scope:            scope,
		BelongsToAccount: accountID,
		PageCursor:       w.pageCursor,
		ResultLimit:      w.resultLimit,
	}
}

// Invitations.

func invitationFromRow(r *identitydb.GetInvitationRow) *Invitation {
	return &Invitation{
		ID:               r.ID,
		Scope:            r.Scope,
		BelongsToAccount: r.BelongsToAccount,
		FromUser:         r.FromUser,
		ToEmail:          r.ToEmail,
		ToName:           r.ToName,
		ToUser:           r.ToUser,
		Token:            r.Token,
		Status:           InvitationStatus(r.Status),
		Note:             r.Note,
		StatusNote:       r.StatusNote,
		ExpiresAt:        r.ExpiresAt.UTC(),
		CreatedAt:        r.CreatedAt.UTC(),
		LastUpdatedAt:    utcPtr(r.LastUpdatedAt),
		ArchivedAt:       utcPtr(r.ArchivedAt),
	}
}

func invitationPageRowFromUser(r *identitydb.ListInvitationsByFromUserRow) pageRow[Invitation] {
	return pageRow[Invitation]{
		value: invitationFromRow(&identitydb.GetInvitationRow{
			ID:               r.ID,
			Scope:            r.Scope,
			BelongsToAccount: r.BelongsToAccount,
			FromUser:         r.FromUser,
			ToEmail:          r.ToEmail,
			ToName:           r.ToName,
			ToUser:           r.ToUser,
			Token:            r.Token,
			Status:           r.Status,
			Note:             r.Note,
			StatusNote:       r.StatusNote,
			ExpiresAt:        r.ExpiresAt,
			CreatedAt:        r.CreatedAt,
			LastUpdatedAt:    r.LastUpdatedAt,
			ArchivedAt:       r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func invitationPageRowToEmail(r *identitydb.ListInvitationsByToEmailRow) pageRow[Invitation] {
	return pageRow[Invitation]{
		value: invitationFromRow(&identitydb.GetInvitationRow{
			ID:               r.ID,
			Scope:            r.Scope,
			BelongsToAccount: r.BelongsToAccount,
			FromUser:         r.FromUser,
			ToEmail:          r.ToEmail,
			ToName:           r.ToName,
			ToUser:           r.ToUser,
			Token:            r.Token,
			Status:           r.Status,
			Note:             r.Note,
			StatusNote:       r.StatusNote,
			ExpiresAt:        r.ExpiresAt,
			CreatedAt:        r.CreatedAt,
			LastUpdatedAt:    r.LastUpdatedAt,
			ArchivedAt:       r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createInvitationParams(i *Invitation) identitydb.CreateInvitationParams {
	return identitydb.CreateInvitationParams{
		ID:               i.ID,
		Scope:            i.Scope,
		BelongsToAccount: i.BelongsToAccount,
		FromUser:         i.FromUser,
		ToEmail:          i.ToEmail,
		ToName:           i.ToName,
		ToUser:           i.ToUser,
		Token:            i.Token,
		Status:           i.Status.String(),
		Note:             i.Note,
		StatusNote:       i.StatusNote,
		ExpiresAt:        i.ExpiresAt.UTC(),
	}
}

// Memberships.

// upsertMembershipParams is the write that puts a user in an account, and the
// only generated statement this table has: the rest key on the (user, account)
// pair rather than on the id.
//
// Neither the id nor the creation time is what comes back out of it. A rejoin
// converges onto the row that is already there, which keeps both — see
// SQLStore.writeMembership, which reads them back rather than assuming the ones
// it sent.
func upsertMembershipParams(m *Membership) identitydb.UpsertMembershipParams {
	return identitydb.UpsertMembershipParams{
		ID:               m.ID,
		Scope:            m.Scope,
		BelongsToUser:    m.BelongsToUser,
		BelongsToAccount: m.BelongsToAccount,
		DefaultAccount:   m.DefaultAccount,
	}
}
