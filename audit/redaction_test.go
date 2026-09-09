package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/tenancy"

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
			ResourceID:   "user_1",
			Actor:        Actor{ID: "user_1", Type: ActorUser},
			Changes: map[string]Change{
				"passwordHash": {Old: "$2a$old", New: "$2a$new"},
				"email":        {Old: "a@example.com", New: "b@example.com"},
			},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {Old: "secret-a", New: "secret-b"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {New: "secret"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Metadata:     map[string]string{"authorization": "Bearer abc", "requestID": "req_1"},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Metadata:     map[string]string{"sessionID": "sess_secret", "requestID": "req_1"},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"password": {New: "hunter2"}, "name": {New: "x"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Actor:        Actor{ID: "user_1"},
			Changes:      map[string]Change{"token": {New: "secret"}},
		}
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
		must.NoError(t, err)
		test.MapEmpty(t, read.Changes)
	})

	T.Run("leaves an unconfigured resource type untouched", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock(),
			WithRedaction("user", Redaction{Drop: []string{"name"}}))
		reader := newTestReader(t, client)

		entry := entryFor("acct_1", "recipe_1")
		record(t, client, recorder, entry)

		read, err := reader.Get(t.Context(), entry.ID)
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
			Scope:        "acct_1",
			Actor:        Actor{ID: "user_1"},
			Changes: map[string]Change{
				"passwordHash": {New: "$2a$new"},
				"token":        {New: "secret"},
				"email":        {New: "a@example.com"},
			},
		})

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
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
