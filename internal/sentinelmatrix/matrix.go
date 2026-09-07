package sentinelmatrix

import (
	"slices"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/sessions"

	"google.golang.org/grpc/codes"
)

// Disposition is what this module has decided one sentinel means on the wire.
// The three are exhaustive by construction: a sentinel either has a case in its
// own package's mappers, or wraps something the platform mappers answer, or
// resolves to a 500.
type Disposition int

const (
	// Mapped: the package's own HTTPMapper and GRPCMapper both answer.
	Mapped Disposition = iota
	// Platform: those two are silent and errors/http and errors/grpc answer,
	// because the sentinel wraps a platform one.
	Platform
	// Unhandled: nobody answers, and a 500 is the honest reply.
	Unhandled
)

func (d Disposition) String() string {
	switch d {
	case Mapped:
		return "mapped by its own package"
	case Platform:
		return "mapped by the platform mappers"
	case Unhandled:
		return "deliberately unmapped"
	default:
		return "unknown"
	}
}

// The five packages that map their own sentinels, spelled once each. Each name
// is three things — a key in Matrix, an entry in Packages and a case in Mappers
// — and a sixth package is added in all three together.
const (
	dataPrivacyPkg = "dataprivacy"
	identityPkg    = "identity"
	linksPkg       = "links"
	operationsPkg  = "operations"
	sessionsPkg    = "sessions"
)

// Decision is one sentinel and what this module decided it means on the wire.
type Decision struct {
	Err error
	Is  Disposition
}

// Matrix is the decision made about every exported sentinel in the four
// packages that map their own errors. Its keys are checked against those
// packages' source in both directions, so it is a roster that cannot quietly
// stop describing the tree.
//
//nolint:goconst // The keys are sentinel identifiers in four other packages, and three of those packages have an ErrNilStore. That they collide is a fact the roster records, not a constant this one should extract.
var Matrix = map[string]map[string]Decision{
	dataPrivacyPkg: {
		// A subject asking after their own export or erasure is a client. These five
		// are the answers they can act on: the ID is not one of theirs, the request
		// is not in the state the call needs, or the request they sent is malformed.
		"ErrArtifactUnavailable":     {Err: dataprivacy.ErrArtifactUnavailable, Is: Mapped},
		"ErrEmptySubjectID":          {Err: dataprivacy.ErrEmptySubjectID, Is: Mapped},
		"ErrNotAwaitingConfirmation": {Err: dataprivacy.ErrNotAwaitingConfirmation, Is: Mapped},
		"ErrRequestNotFound":         {Err: dataprivacy.ErrRequestNotFound, Is: Mapped},
		"ErrUnknownRequestType":      {Err: dataprivacy.ErrUnknownRequestType, Is: Mapped},

		// The nil-argument sentinels, which wrap errors.ErrNilInputParameter and are
		// answered by the platform mapper for that reason. They are wiring failures
		// and the 400 they resolve to is generous; the mapping predates this package
		// and is not this package's to change.
		"ErrNilDatabaseClient": {Err: dataprivacy.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: dataprivacy.ErrNilExecutor, Is: Platform},
		"ErrNilFetch":          {Err: dataprivacy.ErrNilFetch, Is: Platform},
		"ErrNilOperations":     {Err: dataprivacy.ErrNilOperations, Is: Platform},
		"ErrNilRequest":        {Err: dataprivacy.ErrNilRequest, Is: Platform},
		"ErrNilStore":          {Err: dataprivacy.ErrNilStore, Is: Platform},

		// Fulfillment-side outcomes and construction failures. A collector that
		// panicked, an export document too large to store, an upload manager that
		// cannot sign a URL, a registry with a duplicate key: none is a request a
		// subject could have sent differently, and a 500 is the honest answer.
		// ErrNilPage and ErrCursorStalled are a Store misbehaving toward its own
		// caller and never reach a handler at all.
		"ErrArtifactEncrypted":  {Err: dataprivacy.ErrArtifactEncrypted, Is: Unhandled},
		"ErrCollectorPanicked":  {Err: dataprivacy.ErrCollectorPanicked, Is: Unhandled},
		"ErrCursorStalled":      {Err: dataprivacy.ErrCursorStalled, Is: Unhandled},
		"ErrDocumentTooLarge":   {Err: dataprivacy.ErrDocumentTooLarge, Is: Unhandled},
		"ErrDuplicateKey":       {Err: dataprivacy.ErrDuplicateKey, Is: Unhandled},
		"ErrEraserPanicked":     {Err: dataprivacy.ErrEraserPanicked, Is: Unhandled},
		"ErrEverySectionFailed": {Err: dataprivacy.ErrEverySectionFailed, Is: Unhandled},
		"ErrInvalidFragment":    {Err: dataprivacy.ErrInvalidFragment, Is: Unhandled},
		"ErrInvalidKey":         {Err: dataprivacy.ErrInvalidKey, Is: Unhandled},
		"ErrNilPage":            {Err: dataprivacy.ErrNilPage, Is: Unhandled},
		"ErrNoCollectors":       {Err: dataprivacy.ErrNoCollectors, Is: Unhandled},
		"ErrNoErasers":          {Err: dataprivacy.ErrNoErasers, Is: Unhandled},
		"ErrNoURLSigner":        {Err: dataprivacy.ErrNoURLSigner, Is: Unhandled},
		"ErrNoUploadManager":    {Err: dataprivacy.ErrNoUploadManager, Is: Unhandled},
		"ErrNotInProgress":      {Err: dataprivacy.ErrNotInProgress, Is: Unhandled},
		"ErrUnexpiringArtifact": {Err: dataprivacy.ErrUnexpiringArtifact, Is: Unhandled},
		"ErrUnknownStatus":      {Err: dataprivacy.ErrUnknownStatus, Is: Unhandled},
	},
	linksPkg: {
		// The four redemption outcomes and the malformed token. These are the whole
		// of what a person holding a link can be told.
		"ErrInvalidToken":        {Err: links.ErrInvalidToken, Is: Mapped},
		"ErrLinkAlreadyRedeemed": {Err: links.ErrLinkAlreadyRedeemed, Is: Mapped},
		"ErrLinkExpired":         {Err: links.ErrLinkExpired, Is: Mapped},
		"ErrLinkNotFound":        {Err: links.ErrLinkNotFound, Is: Mapped},
		"ErrLinkRevoked":         {Err: links.ErrLinkRevoked, Is: Mapped},

		// Wraps errors.ErrNilInputParameter, and the platform mapper answers it.
		"ErrNilStore": {Err: links.ErrNilStore, Is: Platform},

		// Minter construction — an unregistered action, an unusable URL template, a
		// non-positive TTL — and the store reporting itself. ErrStoreUnavailable is
		// the one worth pausing on: redemption fails closed on it, and it is a 500
		// deliberately, because a link this package cannot prove is unused is not a
		// link the bearer should be told anything specific about.
		"ErrEmptySubject":      {Err: links.ErrEmptySubject, Is: Unhandled},
		"ErrInsecureActionURL": {Err: links.ErrInsecureActionURL, Is: Unhandled},
		"ErrInvalidActionURL":  {Err: links.ErrInvalidActionURL, Is: Unhandled},
		"ErrInvalidID":         {Err: links.ErrInvalidID, Is: Unhandled},
		"ErrInvalidTTL":        {Err: links.ErrInvalidTTL, Is: Unhandled},
		"ErrNoActions":         {Err: links.ErrNoActions, Is: Unhandled},
		"ErrStaleRecord":       {Err: links.ErrStaleRecord, Is: Unhandled},
		"ErrStoreUnavailable":  {Err: links.ErrStoreUnavailable, Is: Unhandled},
		"ErrUnknownAction":     {Err: links.ErrUnknownAction, Is: Unhandled},
	},
	operationsPkg: {
		// A missing operation, which is also what an operation belonging to somebody
		// else reads as, and a subscription refused for capacity.
		"ErrOperationNotFound": {Err: operations.ErrOperationNotFound, Is: Mapped},
		"ErrTooManyWatchers":   {Err: operations.ErrTooManyWatchers, Is: Mapped},

		// The nil-argument sentinels, which wrap errors.ErrNilInputParameter.
		"ErrNilConfig":         {Err: operations.ErrNilConfig, Is: Platform},
		"ErrNilDatabaseClient": {Err: operations.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: operations.ErrNilExecutor, Is: Platform},
		"ErrNilOperation":      {Err: operations.ErrNilOperation, Is: Platform},
		"ErrNilQueue":          {Err: operations.ErrNilQueue, Is: Platform},
		"ErrNilRegistry":       {Err: operations.ErrNilRegistry, Is: Platform},
		"ErrNilService":        {Err: operations.ErrNilService, Is: Platform},
		"ErrNilStore":          {Err: operations.ErrNilStore, Is: Platform},

		// Registry and worker outcomes: a kind registered twice, a runner that
		// panicked, a result too large to record, a watcher used after close. They
		// describe the service rather than the request, and a 500 is the honest
		// answer. ErrRequestTooLarge is the near miss — it is about something a
		// caller sent — but it is raised by the service enqueuing work rather than
		// by a handler decoding a request, and nothing today puts it on a response.
		"ErrDuplicateKind":       {Err: operations.ErrDuplicateKind, Is: Unhandled},
		"ErrDuplicateOperation":  {Err: operations.ErrDuplicateOperation, Is: Unhandled},
		"ErrInvalidDefinition":   {Err: operations.ErrInvalidDefinition, Is: Unhandled},
		"ErrRequestTooLarge":     {Err: operations.ErrRequestTooLarge, Is: Unhandled},
		"ErrRequestTypeMismatch": {Err: operations.ErrRequestTypeMismatch, Is: Unhandled},
		"ErrResultTooLarge":      {Err: operations.ErrResultTooLarge, Is: Unhandled},
		"ErrRunnerPanicked":      {Err: operations.ErrRunnerPanicked, Is: Unhandled},
		"ErrUnknownKind":         {Err: operations.ErrUnknownKind, Is: Unhandled},
		"ErrWatcherClosed":       {Err: operations.ErrWatcherClosed, Is: Unhandled},
	},
	identityPkg: {
		// The four absences and the two collisions: what a client sent that
		// names nothing, and what a client sent that names something already
		// taken. These are the answers a registration form and a directory UI
		// act on, and the reason this package acquired mappers at all.
		"ErrAccountNotFound":    {Err: identity.ErrAccountNotFound, Is: Mapped},
		"ErrEmailAddressTaken":  {Err: identity.ErrEmailAddressTaken, Is: Mapped},
		"ErrInvitationNotFound": {Err: identity.ErrInvitationNotFound, Is: Mapped},
		"ErrMembershipNotFound": {Err: identity.ErrMembershipNotFound, Is: Mapped},
		"ErrUserNotFound":       {Err: identity.ErrUserNotFound, Is: Mapped},
		"ErrUsernameTaken":      {Err: identity.ErrUsernameTaken, Is: Mapped},

		// The three states an act is refused from rather than forbidden. Each is
		// fixable by the caller in a specific order, and a 500 would tell them to
		// do nothing. ErrInvitationExpired is the one row in this file whose two
		// transports differ on purpose — see identity's own mappers.
		"ErrInvitationExpired": {Err: identity.ErrInvitationExpired, Is: Mapped},
		"ErrLastAccountOwner":  {Err: identity.ErrLastAccountOwner, Is: Mapped},
		"ErrNoDefaultAccount":  {Err: identity.ErrNoDefaultAccount, Is: Mapped},

		// A write whose entity names a different tenant than the call did. The
		// two halves of the request disagreed, which is a bad request rather than
		// a refusal on authority.
		"ErrScopeMismatch": {Err: identity.ErrScopeMismatch, Is: Mapped},

		// Wrap errors.ErrNilInputParameter, so the platform mappers answer them.
		// They are wiring failures rather than anything a client sent.
		"ErrNilAccount":        {Err: identity.ErrNilAccount, Is: Platform},
		"ErrNilAccountUpdate":  {Err: identity.ErrNilAccountUpdate, Is: Platform},
		"ErrNilDatabaseClient": {Err: identity.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: identity.ErrNilExecutor, Is: Platform},
		"ErrNilInvitation":     {Err: identity.ErrNilInvitation, Is: Platform},
		"ErrNilMembership":     {Err: identity.ErrNilMembership, Is: Platform},
		"ErrNilProfileUpdate":  {Err: identity.ErrNilProfileUpdate, Is: Platform},
		"ErrNilStore":          {Err: identity.ErrNilStore, Is: Platform},
		"ErrNilUser":           {Err: identity.ErrNilUser, Is: Platform},

		// Wrap errors.ErrUnrecognizedInputValue, which the platform mappers
		// already answer as a bad request. A case of this package's own would be
		// a second copy of that decision, free to drift from it.
		"ErrInvalidEmailAddress":     {Err: identity.ErrInvalidEmailAddress, Is: Platform},
		"ErrInvalidInvitationStatus": {Err: identity.ErrInvalidInvitationStatus, Is: Platform},
		"ErrInvalidTimeZone":         {Err: identity.ErrInvalidTimeZone, Is: Platform},
	},

	sessionsPkg: {
		// Every unusable session. The two timeouts wrap ErrExpired, which wraps
		// ErrNotFound, so all four resolve; they are listed because a sentinel that
		// stops wrapping is exactly the kind of change this roster is here to catch.
		"ErrAbsoluteTimeout": {Err: sessions.ErrAbsoluteTimeout, Is: Mapped},
		"ErrExpired":         {Err: sessions.ErrExpired, Is: Mapped},
		"ErrIdleTimeout":     {Err: sessions.ErrIdleTimeout, Is: Mapped},
		"ErrNotFound":        {Err: sessions.ErrNotFound, Is: Mapped},

		// Wrap errors.ErrEmptyInputParameter and errors.ErrNilInputParameter.
		"ErrIDRequired":        {Err: sessions.ErrIDRequired, Is: Platform},
		"ErrNilBackend":        {Err: sessions.ErrNilBackend, Is: Platform},
		"ErrPrincipalRequired": {Err: sessions.ErrPrincipalRequired, Is: Platform},

		// A backend handed an identifier it did not mint, a backend that keeps no
		// principal index, and the three Policy validation failures. None is
		// something a client sent.
		"ErrIDConflict":              {Err: sessions.ErrIDConflict, Is: Unhandled},
		"ErrNegativeTouchInterval":   {Err: sessions.ErrNegativeTouchInterval, Is: Unhandled},
		"ErrNoPrincipalIndex":        {Err: sessions.ErrNoPrincipalIndex, Is: Unhandled},
		"ErrNoTimeout":               {Err: sessions.ErrNoTimeout, Is: Unhandled},
		"ErrTouchExceedsIdleTimeout": {Err: sessions.ErrTouchExceedsIdleTimeout, Is: Unhandled},
	},
}

// Packages are the directories Matrix's rows are read out of, relative to the
// module root. They are the five that export mappers of their own; a sixth would
// be added here, in Matrix and in Mappers together.
var Packages = []string{dataPrivacyPkg, identityPkg, linksPkg, operationsPkg, sessionsPkg}

// Mappers is the pair of mappers a package exports. The switch is the one place
// this package spells the five out; everywhere else they are the strings in
// Packages.
func Mappers(pkg string) (httperrors.HTTPErrorMapper, grpcerrors.GRPCErrorMapper) {
	switch pkg {
	case dataPrivacyPkg:
		return dataprivacy.HTTPMapper, dataprivacy.GRPCMapper
	case identityPkg:
		return identity.HTTPMapper, identity.GRPCMapper
	case linksPkg:
		return links.HTTPMapper, links.GRPCMapper
	case operationsPkg:
		return operations.HTTPMapper, operations.GRPCMapper
	case sessionsPkg:
		return sessions.HTTPMapper, sessions.GRPCMapper
	default:
		panic("no mappers for " + pkg)
	}
}

// Resolution is what one sentinel resolves to on both transports, asked of the
// mapper that owns it rather than of a process-global registry.
type Resolution struct {
	Err      error
	Package  string
	Name     string
	HTTPMsg  string
	HTTPCode httperrors.ErrorCode
	GRPCCode codes.Code
}

// MappedResolutions is every sentinel this roster records as Mapped, together
// with the answer its own package's mappers give.
//
// It exists for the callers that register those mappers — errormappers.Register
// and, through it, service.Register. Each asserts that what its registration
// makes ToAPIError and MapToGRPC say matches this, so the two cannot answer one
// sentinel differently and neither can drift from the mapper that owns it. Both
// are separate test binaries and could not otherwise share an expectation.
//
// The order is the order of Packages and then of name, so a failure names the
// same row twice in a row rather than a different one each run.
func MappedResolutions() []Resolution {
	var out []Resolution

	for _, pkg := range Packages {
		httpMapper, grpcMapper := Mappers(pkg)

		names := make([]string, 0, len(Matrix[pkg]))
		for name, row := range Matrix[pkg] {
			if row.Is == Mapped {
				names = append(names, name)
			}
		}

		slices.Sort(names)

		for _, name := range names {
			row := Matrix[pkg][name]

			httpCode, httpMsg, _ := httpMapper.Map(row.Err)
			grpcCode, _ := grpcMapper.Map(row.Err)

			out = append(out, Resolution{
				Err:      row.Err,
				Package:  pkg,
				Name:     name,
				HTTPCode: httpCode,
				HTTPMsg:  httpMsg,
				GRPCCode: grpcCode,
			})
		}
	}

	return out
}
