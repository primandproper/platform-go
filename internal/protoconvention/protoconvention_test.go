package protoconvention_test

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// schemas is every .proto this module ships, keyed by the path it sits at and
// mapped to the descriptor its generated bindings carry.
//
// The key is the repository path rather than the canonical import name because
// that is what a failure has to hand a reader: the file to open. The two are
// checked against each other below, so an entry cannot point at the wrong
// descriptor and read as a correct one.
var schemas = map[string]protoreflect.FileDescriptor{
	"audit/proto/primandproper/platform/audit/v1/audit.proto": auditpb.
		File_primandproper_platform_audit_v1_audit_proto,
	"authentication/oauth2clients/proto/primandproper/platform/oauth2clients/v1/oauth2clients.proto": oauth2clientspb.
		File_primandproper_platform_oauth2clients_v1_oauth2clients_proto,
	"authentication/signin/proto/primandproper/platform/signin/v1/signin.proto": signinpb.
		File_primandproper_platform_signin_v1_signin_proto,
	"billing/proto/primandproper/platform/billing/v1/billing.proto": billingpb.
		File_primandproper_platform_billing_v1_billing_proto,
	"comments/proto/primandproper/platform/comments/v1/comments.proto": commentspb.
		File_primandproper_platform_comments_v1_comments_proto,
	"identity/proto/primandproper/platform/identity/v1/identity.proto": identitypb.
		File_primandproper_platform_identity_v1_identity_proto,
	"issuereports/proto/primandproper/platform/issuereports/v1/issuereports.proto": issuereportspb.
		File_primandproper_platform_issuereports_v1_issuereports_proto,
	"notifications/proto/primandproper/platform/notifications/v1/notifications.proto": notificationspb.
		File_primandproper_platform_notifications_v1_notifications_proto,
	"settings/proto/primandproper/platform/settings/v1/settings.proto": settingspb.
		File_primandproper_platform_settings_v1_settings_proto,
	"waitlists/proto/primandproper/platform/waitlists/v1/waitlists.proto": waitlistspb.
		File_primandproper_platform_waitlists_v1_waitlists_proto,
	"webhooks/proto/primandproper/platform/webhooks/v1/webhooks.proto": webhookspb.
		File_primandproper_platform_webhooks_v1_webhooks_proto,
}

// TestEveryFieldSpellsItsJSONNameLikeTheGoTag is the rule this package exists
// for, asserted as an equality so that it holds in both directions: an id field
// that pins nothing fails, and so does a field that pins anything protoc would
// not have derived and the substitution does not ask for.
func TestEveryFieldSpellsItsJSONNameLikeTheGoTag(T *testing.T) {
	T.Parallel()

	for path, file := range schemas {
		T.Run(path, func(t *testing.T) {
			t.Parallel()

			checked := 0

			eachMessage(file.Messages(), func(message protoreflect.MessageDescriptor) {
				fields := message.Fields()

				for i := range fields.Len() {
					field := fields.Get(i)
					checked++

					want := jsonName(field.Name())

					test.EqOp(t, want, field.JSONName(), test.Sprintf(
						"%s.%s renders JSON as %q, and this module's Go types spell that field %q — so a "+
							"consumer serving it over its own handlers and over gRPC-JSON emits two "+
							"spellings of one field",
						message.Name(), field.Name(), field.JSONName(), want))
				}
			})

			must.Positive(t, checked, must.Sprintf("%s has no fields, so this asserted nothing", path))
		})
	}
}

// TestTheRosterIsEveryProtoInTheModule checks the enumeration against the tree
// in both directions. A twelfth schema fails here until somebody records it, and
// a roster entry for a file that has been moved or deleted fails rather than
// quietly asserting over a descriptor nothing ships.
func TestTheRosterIsEveryProtoInTheModule(T *testing.T) {
	T.Parallel()

	found := protoFiles(T)
	rostered := make([]string, 0, len(schemas))

	for path := range schemas {
		rostered = append(rostered, path)
	}

	slices.Sort(rostered)

	for _, path := range found {
		test.True(T, slices.Contains(rostered, path), test.Sprintf(
			"%s ships in this module and is in no roster entry, so nothing checks its field names", path))
	}

	for _, path := range rostered {
		test.True(T, slices.Contains(found, path), test.Sprintf(
			"the roster names %s and no such file ships", path))
	}
}

// TestEachEntryHoldsItsOwnFileDescriptor is what keeps the roster's keys honest.
// The path a file sits at and the canonical name protoc addresses it by are the
// same string below the proto/ root, so an entry mapping one file's path to
// another file's descriptor is detectable rather than merely unlikely.
func TestEachEntryHoldsItsOwnFileDescriptor(T *testing.T) {
	T.Parallel()

	const root = "/proto/"

	for path, file := range schemas {
		T.Run(path, func(t *testing.T) {
			t.Parallel()

			_, canonical, ok := strings.Cut(path, root)
			must.True(t, ok, must.Sprintf("%s does not sit under a proto/ root", path))

			test.EqOp(t, canonical, file.Path(), test.Sprintf(
				"the roster maps %s to the descriptor for %s", path, file.Path()))
		})
	}
}

// TestJSONNameIsProtocsDerivationPlusOneSubstitution pins the rule itself, which
// is the half of this package a reader has to be able to check without running
// protoc. The cases cover both halves: names protoc's derivation answers on its
// own and the substitution leaves alone — redirect_uris among them, because the
// rule deliberately stops at the id suffix and does not chase every acronym a Go
// name might capitalize — and the id names the substitution exists for.
func TestJSONNameIsProtocsDerivationPlusOneSubstitution(T *testing.T) {
	T.Parallel()

	for name, want := range map[string]string{
		"id":                            "id",
		"username":                      "username",
		"resource_type":                 "resourceType",
		"email_address_verified_at":     "emailAddressVerifiedAt",
		"line1":                         "line1",
		"redirect_uris":                 "redirectUris",
		"user_id":                       "userID",
		"account_ids":                   "accountIDs",
		"owner_user_id":                 "ownerUserID",
		"oauth2_client_id":              "oauth2ClientID",
		"payment_processor_customer_id": "paymentProcessorCustomerID",
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, want, jsonName(protoreflect.Name(name)))
		})
	}
}

// jsonName is the spelling this module's fields carry on the wire: protoc's own
// derivation, with a trailing Id rendered ID and a trailing Ids rendered IDs.
//
// The substitution is applied to the derived camel rather than to the name's
// last underscore segment, which is what makes a field spelled plainly id come
// back as "id" — protoc leaves the first character alone, so the derivation of a
// single-segment name has no capital to substitute for, and its Go tag is "id"
// for the same reason.
func jsonName(name protoreflect.Name) string {
	derived := derive(string(name))

	switch {
	case strings.HasSuffix(derived, "Ids"):
		return strings.TrimSuffix(derived, "Ids") + "IDs"
	case strings.HasSuffix(derived, "Id"):
		return strings.TrimSuffix(derived, "Id") + "ID"
	default:
		return derived
	}
}

// derive is protoc's ToJsonName: underscores go away and the character after
// each is capitalized. Nothing else changes, the first character included.
func derive(name string) string {
	var (
		out   strings.Builder
		upper bool
	)

	out.Grow(len(name))

	for _, r := range name {
		switch {
		case r == '_':
			upper = true
		case upper:
			out.WriteRune(unicode.ToUpper(r))
			upper = false
		default:
			out.WriteRune(r)
		}
	}

	return out.String()
}

// eachMessage visits every message in a set and every message nested inside one,
// skipping the synthetic entry types a map field generates: their key and value
// fields are protoc's rather than this module's, and neither is nameable here.
func eachMessage(messages protoreflect.MessageDescriptors, visit func(protoreflect.MessageDescriptor)) {
	for i := range messages.Len() {
		message := messages.Get(i)

		if message.IsMapEntry() {
			continue
		}

		visit(message)
		eachMessage(message.Messages(), visit)
	}
}

// protoFiles is every .proto in the module, by the path it sits at relative to
// the root, sorted.
func protoFiles(t *testing.T) []string {
	t.Helper()

	root := moduleRoot(t)
	found := []string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// Neither holds a schema this module ships. A dot directory can hold
			// another checkout of it — an agent worktree under .claude would
			// otherwise report every schema twice — and artifacts/ is where
			// proto.sh unpacks a pinned protoc, whose own include tree is a few
			// dozen .proto files belonging to protobuf.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts") {
				return filepath.SkipDir
			}

			return nil
		}

		if filepath.Ext(path) != ".proto" {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		found = append(found, filepath.ToSlash(relative))

		return nil
	})
	must.NoError(t, err)

	slices.Sort(found)

	return found
}

// moduleRoot is two directories up, which is where this package sits and where
// go.mod has to be for the answer to be this module rather than whatever tree a
// test binary was copied into.
func moduleRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(t, err)
	must.FileExists(t, filepath.Join(root, "go.mod"))

	return root
}
