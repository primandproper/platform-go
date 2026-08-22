package dataprivacy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/canonical"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// DocumentFormat tags the artifact's framing. It is the first thing a reader
// should look at, and it exists so that a v2 layout is distinguishable from a
// v1 one by something better than guessing from which keys are present.
const DocumentFormat = "dataprivacy.export.v1"

// Manifest describes what an artifact contains and, more importantly, what it
// does not.
//
// Failures is the field that earns this type. An export assembled from eleven
// domains where one timed out is still worth delivering — the subject is
// entitled to the other ten, and the statutory clock does not stop while a
// flaky domain is fixed — but delivering it without saying so would be a
// document that quietly asserts the missing data does not exist. Naming the gap
// is the difference between a partial answer and a wrong one.
type Manifest struct {
	// GeneratedAt is when the artifact was assembled.
	//
	// It is not Request.RequestedAt, and the gap between them is the queue wait
	// plus any retries. Sections are collected as of roughly this instant rather
	// than as of the request — see Collector — so this is the time the document
	// describes.
	GeneratedAt time.Time `json:"generatedAt"`
	// Failures maps a section name to why it is missing. Absent when the export
	// was complete.
	Failures map[string]string `json:"failures,omitempty"`
	// Format is DocumentFormat.
	Format string `json:"format"`
	// RequestID is the request this artifact answers.
	RequestID string `json:"requestID"`
	// Subject is who it is about.
	Subject Subject `json:"subject"`
	// Sections are the section names present in Data, sorted.
	Sections []string `json:"sections"`
}

// Document is the artifact's top-level shape: what this is, and the data.
//
// The two are siblings rather than the manifest being folded into the data, so
// that a section named "manifest" — which a domain is entitled to register —
// cannot collide with the framing.
type Document struct {
	// Data maps a section name to that domain's fragment, verbatim as its
	// Collector returned it.
	Data map[string]json.RawMessage `json:"data"`
	// Manifest describes the document.
	Manifest Manifest `json:"manifest"`
}

// Complete reports whether every registered section was collected.
func (d *Document) Complete() bool {
	return d != nil && len(d.Manifest.Failures) == 0
}

// packager turns a collected document into the bytes that get stored, and back.
//
// The pipeline is canonical JSON, then compression, then encryption, and the
// order is not arbitrary. Compressing before encrypting is the only order that
// compresses anything — ciphertext is incompressible by construction — and
// canonicalizing before both means two exports of unchanged data produce
// identical plaintext, so a consumer can tell "nothing changed" from "something
// changed" by digest rather than by diffing two files that are semantically
// equal and textually not.
//
// Compression before encryption does leak the plaintext's compressibility, and
// that is a real weakness in protocols where an attacker chooses part of the
// plaintext and observes the length (CRIME, BREACH). It does not apply here:
// the artifact is written once, to storage, by us, and nobody is issuing
// adaptive queries against its length.
type packager struct {
	compressor compression.Compressor
	encryptor  encryption.Encryptor
	decryptor  encryption.Decryptor
}

// encode renders a document as the bytes to store.
//
// requestID is bound into the ciphertext as associated data, so a stored
// artifact only decrypts for the request it was produced for. Without it, an
// artifact moved between two requests' rows — by a bug, a bad restore, or
// someone with write access to the reference — would decrypt cleanly and hand
// one subject another subject's export.
func (p *packager) encode(ctx context.Context, doc *Document, requestID string) ([]byte, error) {
	// Canonical rather than plain json.Marshal: the fragments are opaque bytes
	// from eleven different domains, and nothing else in the pipeline would
	// impose a stable key order on them.
	encoded, err := canonical.Marshal(doc)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding dataprivacy export document")
	}

	if p.compressor != nil {
		if encoded, err = p.compressor.CompressBytes(encoded); err != nil {
			return nil, platformerrors.Wrap(err, "compressing dataprivacy export document")
		}
	}

	if p.encryptor != nil {
		ciphertext, encErr := p.encryptor.Encrypt(ctx, encoded, []byte(requestID))
		if encErr != nil {
			return nil, platformerrors.Wrap(encErr, "encrypting dataprivacy export document")
		}

		encoded = ciphertext
	}

	return encoded, nil
}

// decode reverses encode, returning the canonical JSON a subject should
// actually receive.
func (p *packager) decode(ctx context.Context, stored []byte, requestID string) ([]byte, error) {
	if p.decryptor != nil {
		plaintext, err := p.decryptor.Decrypt(ctx, stored, []byte(requestID))
		if err != nil {
			return nil, platformerrors.Wrap(err, "decrypting dataprivacy export document")
		}

		stored = plaintext
	}

	if p.compressor != nil {
		decompressed, err := p.compressor.DecompressBytes(stored)
		if err != nil {
			return nil, platformerrors.Wrap(err, "decompressing dataprivacy export document")
		}

		stored = decompressed
	}

	return stored, nil
}

// encrypts reports whether the stored object is ciphertext, which decides
// whether a signed URL can be handed to a subject at all.
func (p *packager) encrypts() bool {
	return p.encryptor != nil
}

// contentType names what the stored object is, for the storage provider and for
// whatever eventually serves it.
//
// An encrypted artifact is octet-stream whatever the compressor did: it is
// base64 of ciphertext, and describing it by the compression underneath would
// invite a client to try to decompress it.
func (p *packager) contentType() string {
	switch {
	case p.encryptor != nil:
		return "application/octet-stream"
	case p.compressor != nil:
		return "application/octet-stream"
	default:
		return "application/json"
	}
}
