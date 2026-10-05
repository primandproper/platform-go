package audit

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRedaction(T *testing.T) {
	T.Parallel()

	T.Run("drops a field before it is written", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Drop: []string{"passwordHash"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			ResourceID:   "user_1",
			Actor:        Actor{ID: "user_1", Type: ActorUser},
			Changes: map[string]Change{
				"passwordHash": {Old: "$2a$old", New: "$2a$new"},
				"email":        {Old: "a@example.com", New: "b@example.com"},
			},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		must.MapLen(t, 1, read.Changes)
		test.MapNotContainsKey(t, read.Changes, "passwordHash")

		// The caller's own value reflects what was written, not what it asked
		// for — otherwise the value it logs would disagree with the table.
		test.MapNotContainsKey(t, entry.Changes, "passwordHash")
	})

	T.Run("replaces a hashed field with a digest", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Hash: []string{"token"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {Old: "secret-a", New: "secret-b"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)

		oldHash, ok := read.Changes["token"].Old.(string)
		must.True(t, ok)
		test.True(t, strings.HasPrefix(oldHash, "sha256:"))
		test.StrNotContains(t, oldHash, "secret-a")

		newHash, ok := read.Changes["token"].New.(string)
		must.True(t, ok)
		test.NotEq(t, oldHash, newHash)
	})

	T.Run("leaves an absent half of a change absent", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Hash: []string{"token"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventCreated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {New: "secret"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.Nil(t, read.Changes["token"].Old)
		test.NotNil(t, read.Changes["token"].New)
	})

	T.Run("applies to metadata as well as changes", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Drop: []string{"authorization"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventAccessed,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Metadata:     map[string]string{"authorization": "Bearer abc", "requestID": "req_1"},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		must.MapLen(t, 1, read.Metadata)
		test.EqOp(t, "req_1", read.Metadata["requestID"])
	})

	T.Run("hashes a metadata value in place", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Hash: []string{"sessionID"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventAccessed,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Metadata:     map[string]string{"sessionID": "sess_secret", "requestID": "req_1"},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		must.MapLen(t, 2, read.Metadata)

		// Correlatable without being readable: two entries carrying the same
		// session hash to the same digest, which is the question worth asking.
		test.True(t, strings.HasPrefix(read.Metadata["sessionID"], "sha256:"))
		test.StrNotContains(t, read.Metadata["sessionID"], "sess_secret")
		test.EqOp(t, "req_1", read.Metadata["requestID"])
	})

	T.Run("applies the catch-all to every resource type", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("", Redaction{Drop: []string{"password"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "something_else_entirely",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"password": {New: "hunter2"}, "name": {New: "x"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		must.MapLen(t, 1, read.Changes)
		test.MapContainsKey(t, read.Changes, "name")
	})

	T.Run("drops a field named in both lists", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Hash: []string{"token"}}),
			WithRedaction("user", Redaction{Drop: []string{"token"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {New: "secret"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.MapEmpty(t, read.Changes)
	})

	T.Run("leaves an unconfigured resource type untouched", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Drop: []string{"name"}}))
		reader := newTestReader(t, client)

		entry := entryFor(tenancy.Of("acct_1"), "article_1")
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.MapContainsKey(t, read.Changes, "name")
	})

	T.Run("hashes over the redacted values, so the chain still verifies", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Drop: []string{"passwordHash"}, Hash: []string{"token"}}))
		reader := newTestReader(t, client)

		record(t, client, recorder, &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Of("acct_1"),
			Actor:        Actor{ID: "user_1"},
			Changes: map[string]Change{
				"passwordHash": {New: "$2a$new"},
				"token":        {New: "secret"},
				"email":        {New: "a@example.com"},
			},
		})

		result, err := reader.Verify(t.Context(), client.Reader(), tenancy.Of("acct_1"), time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		test.True(t, result.Intact())
	})
}

func TestCredentialRedaction(T *testing.T) {
	T.Parallel()

	T.Run("names no field in both lists", func(t *testing.T) {
		t.Parallel()

		r := CredentialRedaction()
		for _, field := range r.Hash {
			test.SliceNotContains(t, r.Drop, field)
		}
	})

	T.Run("spells every multi-word field both ways", func(t *testing.T) {
		t.Parallel()

		r := CredentialRedaction()
		for _, fields := range [][]string{r.Drop, r.Hash} {
			for _, field := range fields {
				if !strings.Contains(field, "_") {
					continue
				}

				words := strings.Split(field, "_")
				for i := 1; i < len(words); i++ {
					words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
				}
				test.SliceContains(t, fields, strings.Join(words, ""))
			}
		}
	})

	T.Run("returns a fresh value on each call", func(t *testing.T) {
		t.Parallel()

		first := CredentialRedaction()
		first.Drop[0] = "overwritten"

		test.SliceNotContains(t, CredentialRedaction().Drop, "overwritten")
	})

	T.Run("is installed by default, over changes and metadata alike", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		credentials := CredentialRedaction()
		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "anything",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"email": {New: "a@example.com"}},
			Metadata:     map[string]string{"reason": "rotation"},
		}
		for _, field := range append(slices.Clone(credentials.Drop), credentials.Hash...) {
			entry.Changes[field] = Change{Old: "plain-old-" + field, New: "plain-new-" + field}
			entry.Metadata[field] = "plain-" + field
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)

		for _, field := range credentials.Drop {
			test.MapNotContainsKey(t, read.Changes, field)
			test.MapNotContainsKey(t, read.Metadata, field)
		}

		for _, field := range credentials.Hash {
			newHash, ok := read.Changes[field].New.(string)
			must.True(t, ok)
			test.True(t, strings.HasPrefix(newHash, "sha256:"))
			test.StrNotContains(t, newHash, "plain")

			test.True(t, strings.HasPrefix(read.Metadata[field], "sha256:"))
		}

		test.EqOp(t, "a@example.com", read.Changes["email"].New)
		test.EqOp(t, "rotation", read.Metadata["reason"])
	})

	T.Run("a deployment's own catch-all adds to it rather than replacing it", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("", Redaction{Drop: []string{"pin"}, Hash: []string{"password"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes: map[string]Change{
				"pin":      {New: "1234"},
				"secret":   {New: "s3cret"},
				"password": {New: "hunter2"},
			},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.MapNotContainsKey(t, read.Changes, "pin")
		test.MapNotContainsKey(t, read.Changes, "secret")
		// Named in the deployment's Hash and the default's Drop: dropped.
		test.MapNotContainsKey(t, read.Changes, "password")
	})

	T.Run("WithoutCredentialRedaction records the fields as given", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(), WithoutCredentialRedaction())
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {New: "not-a-credential"}},
			Metadata:     map[string]string{"secret": "not-a-credential"},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.EqOp(t, "not-a-credential", read.Changes["token"].New)
		test.EqOp(t, "not-a-credential", read.Metadata["secret"])
	})

	T.Run("WithoutCredentialRedaction leaves a deployment's own catch-all in place", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithoutCredentialRedaction(),
			WithRedaction("", Redaction{Drop: []string{"secret"}}))
		reader := newTestReader(t, client)

		entry := &Entry{
			EventType:    EventUpdated,
			ResourceType: "user",
			Scope:        tenancy.Global(),
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"secret": {New: "s3cret"}, "token": {New: "kept"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.GetAcrossScopes(t.Context(), client.Reader(), entry.ID)
		must.NoError(t, err)
		test.MapNotContainsKey(t, read.Changes, "secret")
		test.EqOp(t, "kept", read.Changes["token"].New)
	})
}

func TestRedaction_merge(T *testing.T) {
	T.Parallel()

	T.Run("accumulates rather than replacing", func(t *testing.T) {
		t.Parallel()

		merged := Redaction{Drop: []string{"a"}}.merge(Redaction{Drop: []string{"b"}, Hash: []string{"c"}})

		test.SliceContains(t, merged.Drop, "a")
		test.SliceContains(t, merged.Drop, "b")
		test.SliceContains(t, merged.Hash, "c")
	})

	T.Run("does not alias the receiver's slices", func(t *testing.T) {
		t.Parallel()

		original := Redaction{Drop: []string{"a"}}
		_ = original.merge(Redaction{Drop: []string{"b"}})

		test.SliceLen(t, 1, original.Drop)
	})
}
