package magiclinks

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The construction and argument refusals. Each wraps a platform sentinel, so the
// platform mappers answer them and this package declares no mappers of its own —
// the refusals a caller acts on are signin's, because they are the sign-in
// service's answers rather than this table's.
//
// The refusal a redemption actually produces is signin.ErrInvalidMagicLink,
// returned from Redeem rather than declared here: an unknown token, a spent one,
// a withdrawn one and an expired one are one answer, and that answer belongs to
// the door a caller is standing at rather than to the table underneath it.
var (
	// ErrNilConfig indicates NewSQLStore was called without a config.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in link store config")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the sign-in link store")

	// ErrNilRequest indicates Issue was called with no request on it.
	ErrNilRequest = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in link request")

	// ErrEmptySubjectID indicates a mint or a revocation naming nobody.
	ErrEmptySubjectID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty subject ID for a sign-in link")

	// ErrEmptyEmailAddress indicates a mint that did not say where the mail is
	// going.
	//
	// It is refused rather than stored empty, because the column is what a
	// redemption's address comparison is made against — see
	// signin.Service.RedeemMagicLink — and a row holding nothing there would
	// compare equal to nothing and refuse every redemption of a link that was
	// perfectly good. A mint whose address is unknown is a mail nobody can send.
	ErrEmptyEmailAddress = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty email address for a sign-in link")

	// ErrEmptySecret indicates a redemption presenting nothing.
	//
	// It is this store's own refusal rather than signin.ErrInvalidMagicLink,
	// because an empty argument is a caller that did not submit rather than a
	// guess that missed — and answering it with the collapsed refusal would put
	// a database round trip behind every empty request. signin's door refuses
	// one a step earlier for the same reason, so this is the backstop for a
	// caller holding the store directly.
	ErrEmptySecret = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty sign-in link secret")

	// ErrNonPositiveLifetime indicates a mint with no lifetime on it.
	//
	// It is refused rather than defaulted, because a sign-in link with no
	// deadline is a credential in an inbox that never dies, and a default
	// invented here would be this package choosing how long one lives. The
	// lifetime is signin's WithMagicLinkTTL, which has one.
	ErrNonPositiveLifetime = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "non-positive sign-in link lifetime")
)
