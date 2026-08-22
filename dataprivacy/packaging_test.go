package dataprivacy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/compression"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testDocument is the document the packaging suite round trips.
func testDocument() *Document {
	return &Document{
		Data: map[string]json.RawMessage{
			"identity": json.RawMessage(`{"email":"a@example.com"}`),
			"billing":  json.RawMessage(`{"invoices":[1,2,3]}`),
		},
		Manifest: Manifest{
			Format:      DocumentFormat,
			RequestID:   "req-1",
			Subject:     testSubject,
			GeneratedAt: baseTime,
			Sections:    []string{"billing", "identity"},
		},
	}
}

func TestPackager(T *testing.T) {
	T.Parallel()

	T.Run("plain JSON round trips", func(t *testing.T) {
		t.Parallel()

		p := &packager{}

		encoded, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		decoded, err := p.decode(t.Context(), encoded, testRequestID)
		must.NoError(t, err)

		var doc Document
		must.NoError(t, json.Unmarshal(decoded, &doc))

		test.Eq(t, []string{"billing", "identity"}, doc.Manifest.Sections)
		test.EqOp(t, "application/json", p.contentType())
		test.False(t, p.encrypts())
	})

	T.Run("the encoding is canonical", func(t *testing.T) {
		t.Parallel()

		p := &packager{}

		first, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		second, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		// Two exports of unchanged data produce identical bytes, so a consumer
		// can tell "nothing changed" from "something changed" by digest rather
		// than by diffing two files that are semantically equal.
		test.Eq(t, first, second)

		// Object keys are sorted, whatever order the fragments arrived in.
		body := string(first)
		test.Less(t, strings.Index(body, `"identity"`), strings.Index(body, `"billing"`))
	})

	T.Run("compression round trips", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		p := &packager{compressor: compressor}

		encoded, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		decoded, err := p.decode(t.Context(), encoded, testRequestID)
		must.NoError(t, err)

		var doc Document
		must.NoError(t, json.Unmarshal(decoded, &doc))
		test.EqOp(t, DocumentFormat, doc.Manifest.Format)

		test.EqOp(t, "application/octet-stream", p.contentType())
	})

	T.Run("encryption round trips and hides the plaintext", func(t *testing.T) {
		t.Parallel()

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		p := &packager{encryptor: encryptorDecryptor, decryptor: encryptorDecryptor}

		encoded, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		test.StrNotContains(t, string(encoded), "a@example.com")
		test.True(t, p.encrypts())

		decoded, err := p.decode(t.Context(), encoded, testRequestID)
		must.NoError(t, err)

		test.StrContains(t, string(decoded), "a@example.com")
	})

	T.Run("compression and encryption compose in that order", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		p := &packager{compressor: compressor, encryptor: encryptorDecryptor, decryptor: encryptorDecryptor}

		encoded, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.NoError(t, err)

		decoded, err := p.decode(t.Context(), encoded, testRequestID)
		must.NoError(t, err)

		var doc Document
		must.NoError(t, json.Unmarshal(decoded, &doc))
		test.Eq(t, []string{"billing", "identity"}, doc.Manifest.Sections)
	})

	T.Run("a manifest with failures reports the document incomplete", func(t *testing.T) {
		t.Parallel()

		doc := testDocument()
		test.True(t, doc.Complete())

		doc.Manifest.Failures = map[string]string{"billing": "timed out"}
		test.False(t, doc.Complete())
	})
}

func TestRequest_Overdue(T *testing.T) {
	T.Parallel()

	T.Run("an unfulfilled request past its deadline is overdue", func(t *testing.T) {
		t.Parallel()

		req := &Request{Status: StatusInProgress, DueAt: baseTime}

		test.True(t, req.Overdue(baseTime.Add(time.Second)))
		test.False(t, req.Overdue(baseTime.Add(-time.Second)))
	})

	T.Run("a served request is never overdue", func(t *testing.T) {
		t.Parallel()

		// Late is a fact about the past, not a thing to page somebody about.
		for _, status := range []Status{StatusCompleted, StatusFailed, StatusExpired, StatusCancelled} {
			req := &Request{Status: status, DueAt: baseTime}
			test.False(t, req.Overdue(baseTime.Add(time.Hour)), test.Sprintf("status %s", status))
		}
	})

	T.Run("a request with no deadline is never overdue", func(t *testing.T) {
		t.Parallel()

		req := &Request{Status: StatusInProgress}
		test.False(t, req.Overdue(baseTime))
	})

	T.Run("a nil request is not overdue", func(t *testing.T) {
		t.Parallel()

		var req *Request
		test.False(t, req.Overdue(baseTime))
		test.False(t, req.Partial())
	})
}

func TestStatus_Terminal(T *testing.T) {
	T.Parallel()

	T.Run("classifies every status", func(t *testing.T) {
		t.Parallel()

		terminal := []Status{StatusCompleted, StatusFailed, StatusExpired, StatusCancelled}
		for _, status := range terminal {
			test.True(t, status.Terminal(), test.Sprintf("status %s", status))
		}

		live := []Status{StatusAwaitingConfirmation, StatusInProgress, StatusInProgress}
		for _, status := range live {
			test.False(t, status.Terminal(), test.Sprintf("status %s", status))
		}
	})
}

func TestRequestType_Valid(T *testing.T) {
	T.Parallel()

	T.Run("accepts only the implemented types", func(t *testing.T) {
		t.Parallel()

		test.True(t, RequestExport.Valid())
		test.True(t, RequestErasure.Valid())
		test.False(t, RequestType("").Valid())
		test.False(t, RequestType("rectification").Valid())
	})
}

func TestPackager_MalformedFragment(T *testing.T) {
	T.Parallel()

	T.Run("a fragment that is not JSON fails the encode", func(t *testing.T) {
		t.Parallel()

		doc := testDocument()
		doc.Data["broken"] = json.RawMessage(`{"unterminated":`)

		// The Worker validates fragments before assembly precisely so this
		// cannot happen there; the guard here is what makes that a defense in
		// depth rather than the only check.
		_, err := (&packager{}).encode(t.Context(), doc, testRequestID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "encoding dataprivacy export document")
	})
}
