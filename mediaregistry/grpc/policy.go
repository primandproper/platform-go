package grpc

import (
	"context"
	"mime"
	"path"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
)

// The rules a deployment has about its uploads, each a value this surface takes
// as an option with a default rather than a shape it decides.
//
// Every one of them is a policy and none of them is the consumer's noun, which
// is the ruling this surface exists under: which types a product accepts, how
// large, where in the bucket the bytes go, and what it meters on the way in are
// the same four questions whatever the uploads are for. A product that answers
// them differently passes different values; nothing about the RPCs changes.

// DefaultMaxBytes is how large an upload may be when the deployment says
// nothing: 100 MiB.
//
// The cap is on by default and switched off by name, with WithoutMaxBytes. An
// uncapped upload is a caller deciding how much of the bucket, and of this
// process's time, one request may spend, and that is a decision a deployment
// should have to write down rather than inherit.
const DefaultMaxBytes int64 = 100 << 20

// UserSubjectType is the subject type an upload or a registration may attach an
// object to, paired with the caller's own principal identifier.
//
// It is the one word of the consumer's vocabulary this surface speaks, and it
// speaks it only about the caller. Whether recipe 123 is the caller's is a
// question only the consumer can answer, so an attachment to anything but the
// caller goes through the consumer's own RPC, which authorizes the subject and
// calls mediaregistry.StoreAndRecord with it. A user attaching an upload to
// themselves is the one attachment this surface can authorize without asking
// anybody, and it is the one an avatar is.
const UserSubjectType = "user"

type (
	// KeyFunc builds the key an upload's bytes are stored at, from who sent
	// them, the row id minted for them, and the name the client gave them.
	//
	// It is also where the part of the bucket that is the caller's comes from:
	// the default RecordKeyPolicy reads KeyFunc(caller, "", "") as the caller's
	// prefix and admits only keys beneath it. A layout that joins its segments
	// with path.Join, as DefaultKeyFunc does, therefore gets a record policy
	// that agrees with it for free.
	KeyFunc func(caller mediaregistryhttp.Caller, objectID, name string) string

	// ContentTypePolicy decides whether an object of this declared type may be
	// stored. False refuses it before a byte is read.
	//
	// It is asked about the type the client declared, which is the type the
	// row records and mediaregistry/http serves the object under, with nosniff.
	// So the declared type is the one that matters, and nothing here reads the
	// bytes to second-guess it.
	ContentTypePolicy func(contentType string) bool

	// RecordKeyPolicy decides whether a caller may register the bytes at key as
	// theirs. False is refused as an absence.
	//
	// It exists because registering a key is claiming it: a caller who could
	// register any key could register one another user's upload was written to
	// before its own registration ran, and would own it.
	RecordKeyPolicy func(ctx context.Context, caller mediaregistryhttp.Caller, key string) (bool, error)

	// AfterUpload runs once an upload is stored and registered, with the row
	// that was registered. It is where metering goes: this surface meters
	// nothing itself, because which meter an upload counts against is the
	// deployment's.
	//
	// It is not fatal. The bytes are stored and the row is committed by the
	// time it runs, so an error here is recorded on the upload's span and the
	// caller is still told their upload succeeded — which it did.
	AfterUpload func(ctx context.Context, caller mediaregistryhttp.Caller, object *mediaregistry.Object) error
)

// DefaultKeyFunc is the layout a deployment gets without WithKeyFunc:
// <principal>/<object id>/<name>.
//
// The principal first, so that the part of the bucket that is one caller's is
// a prefix, which is what the default RecordKeyPolicy checks and what a
// retention sweep over one person's uploads lists. The minted id next, so two
// uploads of avatar.png are two objects rather than one overwriting the other.
func DefaultKeyFunc(caller mediaregistryhttp.Caller, objectID, name string) string {
	return path.Join(caller.PrincipalID, objectID, name)
}

// activeOrUnstated is the default ContentTypePolicy: it refuses a type a
// browser executes, and a type nobody stated.
//
// The executed types are mediaregistry/http's list rather than a second one,
// because the question is the same — which stored types are a script on the
// application's origin — and two lists of the answer can disagree. The serve
// route already presents those as attachments; refusing them here is the
// stricter half, applied where refusing costs nothing. An unstated type is
// refused for the reason that route treats one as an attachment: a row that
// does not say what its object is leaves the answer to whatever sniffs it.
func activeOrUnstated(contentType string) bool {
	return !mediaregistryhttp.ActiveContent(contentType)
}

// AllowContentTypes is a ContentTypePolicy admitting exactly the named media
// types — the allowlist most products want. Parameters are ignored and case
// does not matter, so "image/png" admits "IMAGE/PNG; charset=binary".
//
// It replaces the default rather than narrowing it, as every option here
// does: a deployment that names image/svg+xml has decided to accept it.
func AllowContentTypes(types ...string) ContentTypePolicy {
	allowed := make([]string, 0, len(types))
	for _, t := range types {
		allowed = append(allowed, mediaType(t))
	}

	return func(contentType string) bool {
		base := mediaType(contentType)

		return base != "" && slices.Contains(allowed, base)
	}
}

// mediaType is a content type's media type, lowercased and without its
// parameters, or empty for one that does not parse.
func mediaType(contentType string) string {
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}

	return strings.ToLower(base)
}

// KeysUnderPrefix is a RecordKeyPolicy admitting only keys beneath the part of
// the bucket keys lays out for the caller: KeyFunc(caller, "", ""), cleaned.
// It is the default, over whichever KeyFunc the server was built with.
//
// A key is admitted only in its cleaned form, so "alice/../bob/x" is not a key
// under "alice". And a layout that puts nothing of the caller at the front —
// one whose prefix is empty — admits nothing, because a prefix everybody shares
// is not a part of the bucket that is anybody's. A deployment with such a
// layout decides for itself which keys a caller may claim, with
// WithRecordKeyPolicy.
func KeysUnderPrefix(keys KeyFunc) RecordKeyPolicy {
	return func(_ context.Context, caller mediaregistryhttp.Caller, key string) (bool, error) {
		prefix := path.Clean(keys(caller, "", ""))
		if prefix == "." || prefix == "/" || caller.PrincipalID == "" {
			return false, nil
		}

		if path.Clean(key) != key {
			return false, nil
		}

		return strings.HasPrefix(key, strings.TrimSuffix(prefix, "/")+"/"), nil
	}
}

// validName reports whether a client's object name can be the last segment of
// a key: present, one segment, and not one of the two that name a directory.
func validName(name string) bool {
	switch {
	case name == "", name == ".", name == "..":
		return false
	case strings.ContainsAny(name, `/\`):
		return false
	default:
		return true
	}
}

// mayAttach reports whether a caller may attach an object to subject from this
// surface: to nothing, or to themselves. See UserSubjectType.
func mayAttach(caller mediaregistryhttp.Caller, subject mediaregistry.Subject) bool {
	if !subject.Attached() {
		return true
	}

	return subject.Type == UserSubjectType && subject.ID == caller.PrincipalID && caller.PrincipalID != ""
}
