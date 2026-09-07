package http

var (
	// this just ensures that we don't have any duplicated codes.
	_ = map[string]ErrorCode{
		string(ErrNothingSpecific):            ErrNothingSpecific,
		string(ErrFetchingSessionContextData): ErrFetchingSessionContextData,
		string(ErrDecodingRequestInput):       ErrDecodingRequestInput,
		string(ErrValidatingRequestInput):     ErrValidatingRequestInput,
		string(ErrDataNotFound):               ErrDataNotFound,
		string(ErrTalkingToDatabase):          ErrTalkingToDatabase,
		string(ErrMisbehavingDependency):      ErrMisbehavingDependency,
		string(ErrTalkingToSearchProvider):    ErrTalkingToSearchProvider,
		string(ErrSecretGeneration):           ErrSecretGeneration,
		string(ErrUserIsBanned):               ErrUserIsBanned,
		string(ErrUserIsNotAuthorized):        ErrUserIsNotAuthorized,
		string(ErrEncryptionIssue):            ErrEncryptionIssue,
		string(ErrCircuitBroken):              ErrCircuitBroken,
		string(ErrIdempotencyKeyInFlight):     ErrIdempotencyKeyInFlight,
		string(ErrIdempotencyKeyReused):       ErrIdempotencyKeyReused,
		string(ErrResourceConflict):           ErrResourceConflict,
		string(ErrTooManyRequests):            ErrTooManyRequests,
		string(ErrInvalidRequestSignature):    ErrInvalidRequestSignature,
		string(ErrNotEntitled):                ErrNotEntitled,
		string(ErrQuotaExhausted):             ErrQuotaExhausted,
		string(ErrActionLinkUnusable):         ErrActionLinkUnusable,
		string(ErrInvalidSearchCursor):        ErrInvalidSearchCursor,
		string(ErrSearchWindowExceeded):       ErrSearchWindowExceeded,
	}
)

type (
	// ErrorCode is a string code identifying specific error conditions in API responses.
	ErrorCode string
)

const (
	// ErrNothingSpecific is a catch-all error code for when we just need one.
	ErrNothingSpecific ErrorCode = "E100"
	// ErrFetchingSessionContextData is returned when we fail to fetch session context data.
	ErrFetchingSessionContextData ErrorCode = "E101"
	// ErrDecodingRequestInput is returned when we fail to decode request input.
	ErrDecodingRequestInput ErrorCode = "E102"
	// ErrValidatingRequestInput is returned when the user provides invalid input.
	ErrValidatingRequestInput ErrorCode = "E103"
	// ErrDataNotFound is returned when we fail to find data in the database.
	ErrDataNotFound ErrorCode = "E104"
	// ErrTalkingToDatabase is returned when we fail to interact with a database.
	ErrTalkingToDatabase ErrorCode = "E105"
	// ErrMisbehavingDependency is returned when we fail to interact with a third party.
	ErrMisbehavingDependency ErrorCode = "E106"
	// ErrTalkingToSearchProvider is returned when we fail to interact with the search provider.
	ErrTalkingToSearchProvider ErrorCode = "E107"
	// ErrSecretGeneration is returned when we fail to generate a secret.
	ErrSecretGeneration ErrorCode = "E108"
	// ErrUserIsBanned is returned when a user is banned.
	ErrUserIsBanned ErrorCode = "E109"
	// ErrUserIsNotAuthorized is returned when a user is not authorized.
	ErrUserIsNotAuthorized ErrorCode = "E110"
	// ErrEncryptionIssue is returned when encryption fails in the service.
	ErrEncryptionIssue ErrorCode = "E111"
	// ErrCircuitBroken is returned when a service is circuit broken.
	ErrCircuitBroken ErrorCode = "E112"
	// ErrIdempotencyKeyInFlight is returned when a request repeats an
	// idempotency key whose work is still running. Whether that work will
	// succeed is unknowable, so the repeat is refused rather than run again.
	ErrIdempotencyKeyInFlight ErrorCode = "E113"
	// ErrIdempotencyKeyReused is returned when an idempotency key is presented
	// with a different request than the one it was first used for. Replaying
	// the earlier response would hide a client bug.
	ErrIdempotencyKeyReused ErrorCode = "E114"
	// ErrResourceConflict is returned when a request conflicts with the current
	// state of the resource — deleting something another record still
	// references, for instance. It is the general conflict code:
	// ErrIdempotencyKeyInFlight is also a 409, but it says something specific
	// about idempotency rather than about the resource.
	ErrResourceConflict ErrorCode = "E115"
	// ErrTooManyRequests is returned when a rate limiter refused the request.
	// It says only that the request came too fast, never that a quota is
	// spent: "too much this month" is a different answer with a different
	// remedy, and conflating them tells a client to retry when it should stop.
	ErrTooManyRequests ErrorCode = "E116"
	// ErrInvalidRequestSignature is returned when a request's HMAC signature did
	// not verify, or verified against a timestamp outside the tolerance.
	//
	// One code for both, because the client's remedy is the same — sign it
	// properly, with a current clock — and because a code per failure mode is a
	// forgery oracle. The two are still distinguishable in the message, which is
	// where clock skew, the one benign cause, can be said out loud without
	// saying anything about the key.
	ErrInvalidRequestSignature ErrorCode = "E117"
	// ErrNotEntitled is returned when the account's plan does not include the
	// feature the request needs.
	//
	// It is not ErrUserIsNotAuthorized, and the difference is the remedy. A 403
	// tells a client it is the wrong principal for this action; this tells it the
	// action is not part of what the account bought. The first is answered by an
	// administrator granting a role, the second by somebody entering a card, and
	// a client shown the wrong one asks the wrong person.
	ErrNotEntitled ErrorCode = "E118"
	// ErrQuotaExhausted is returned when the account is entitled to the feature
	// and has consumed all of it for the current billing period.
	//
	// It is deliberately not ErrTooManyRequests, whose own documentation draws
	// the same line from the other side: "too fast" resolves by waiting a moment
	// and "too much this month" does not resolve by waiting at all. Retry-After
	// is meaningful for one of them and a month long for the other.
	ErrQuotaExhausted ErrorCode = "E119"
	// ErrActionLinkUnusable is returned when a magic-login, verification,
	// reset, or unsubscribe link cannot be redeemed: it was already used, it
	// expired, it was revoked, or there is no such link.
	//
	// One code for all four, because a client has the same thing to do with
	// every one of them — ask for a new link — and because the four are not a
	// branch a client should be writing. The distinction that matters is the
	// one a person reads, and that is in the message, which can be specific
	// here without disclosing anything: a link token is 256 random bits, so
	// nobody is ever holding one they were not sent, and there is no guess for
	// the difference between "expired" and "no such link" to be an oracle for.
	ErrActionLinkUnusable ErrorCode = "E120"
	// ErrInvalidSearchCursor is returned when a pagination cursor was not issued
	// by the index being asked to resume it — it did not decode, or it came from
	// a different backend.
	//
	// It is not ErrValidatingRequestInput, though it is also a 400, because the
	// remedy is different and a client can act on it without a person reading
	// the message. Bad input means the request was wrong and needs correcting;
	// this means the request was fine and the *position* is unusable, and the
	// answer is to drop the cursor and start the pagination again. A client that
	// cannot tell the two apart either retries a cursor that will never work or
	// abandons a query that would have.
	ErrInvalidSearchCursor ErrorCode = "E121"
	// ErrSearchWindowExceeded is returned when pagination reached the depth the
	// index will serve. The cursor was valid; the page it names is past the end
	// of what can be paged to.
	//
	// Distinct from ErrInvalidSearchCursor for the same reason that one is
	// distinct from ErrValidatingRequestInput: same status, different remedy.
	// Restarting pagination does not help here — every page up to the ceiling is
	// still fine, and the only thing that reaches the rest of the result set is a
	// narrower query. It is emphatically not a 200 with an empty page, which
	// would tell the client it had seen everything.
	ErrSearchWindowExceeded ErrorCode = "E122"
	// ErrAuthenticationFailed is returned when a caller did not prove they are
	// who they say they are: a handle nobody holds, a password that does not
	// match, a second-factor code that does not validate, or a second-factor code
	// that was required and not sent.
	//
	// One code for the whole set, because telling them apart is the disclosure a
	// sign-in exists to avoid — "no such user" enumerates a directory and "wrong
	// code" confirms a password to whoever is guessing them. The one distinction
	// a client genuinely needs is whether to ask for a second-factor code, and
	// that is in the message rather than in a second code, so a client rendering
	// the message is right either way and one branching on the code cannot build
	// an oracle out of it.
	//
	// It is not ErrFetchingSessionContextData, which is the other 401 here: that
	// one is a session this service could not read, and this one is a caller who
	// has no session yet and did not earn one.
	ErrAuthenticationFailed ErrorCode = "E123"
)
