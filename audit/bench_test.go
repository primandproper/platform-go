package audit

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/tenancy"
)

// The cost Record adds over a plain INSERT is what these measure: canonicalize
// the entry, encode the field blobs, and digest the result. The INSERT itself
// dominates in any real deployment, but this is the part that runs inside the
// caller's transaction and therefore on their lock hold time, so it is worth a
// number rather than an assumption.

type benchResource struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Descr     string    `json:"description"`
	OwnerID   string    `json:"ownerID"`
	Servings  int       `json:"servings"`
	Prep      int       `json:"prepMinutes"`
	Published bool      `json:"published"`
}

func benchEntry() *Entry {
	return &Entry{
		RecordedAt:   time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
		ID:           "entry_01HZY0000000000000",
		Seq:          4096,
		Scope:        tenancy.Of("acct_01HZY0000000000000"),
		ResourceType: "recipe",
		ResourceID:   "recipe_01HZY0000000000000",
		EventType:    EventUpdated,
		Actor:        Actor{ID: "user_01HZY0000000000000", Type: ActorUser, IP: "203.0.113.7"},
		Changes: map[string]Change{
			"name":        {Old: "Soup", New: "Stew"},
			"servings":    {Old: 2, New: 4},
			"published":   {Old: false, New: true},
			"prepMinutes": {Old: 15, New: 40},
		},
		Metadata: map[string]string{"requestID": "req_01HZY0000000000000", "reason": "correction"},
	}
}

func BenchmarkCanonicalImage(b *testing.B) {
	entry := benchEntry()

	changes, err := encodeFields(entry.Changes)
	if err != nil {
		b.Fatal(err)
	}

	metadata, err := encodeFields(entry.Metadata)
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		bytesSink = canonicalImage(entry, changes, metadata)
	}
}

// BenchmarkEncodeAndHash is the whole per-entry cost: both field maps encoded
// canonically, then framed and digested.
func BenchmarkEncodeAndHash(b *testing.B) {
	entry := benchEntry()

	for b.Loop() {
		changes, err := encodeFields(entry.Changes)
		if err != nil {
			b.Fatal(err)
		}

		metadata, err := encodeFields(entry.Metadata)
		if err != nil {
			b.Fatal(err)
		}

		if stringSink, err = chainHash(entry.PrevHash, canonicalImage(entry, changes, metadata)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiff(b *testing.B) {
	before := benchResource{
		CreatedAt: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
		ID:        "recipe_01HZY0000000000000",
		Name:      "Soup",
		Descr:     "an ordinary description",
		OwnerID:   "acct_01HZY0000000000000",
		Servings:  2,
		Prep:      15,
	}

	after := before
	after.Name = "Stew"
	after.Servings = 4
	after.Published = true

	for b.Loop() {
		var err error
		if changesSink, err = Diff(before, after); err != nil {
			b.Fatal(err)
		}
	}
}

var (
	bytesSink   []byte
	stringSink  string
	changesSink map[string]Change
)
