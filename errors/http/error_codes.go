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
)
