package http

import (
	"context"
	"io"
	"slices"
	"strings"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/uploads"
)

// The two dispositions, spelled once because the rule below returns one of them
// and the header carries it.
const (
	dispositionInline     = "inline"
	dispositionAttachment = "attachment"
)

// activeContentTypes are the stored types a browser treats as a document of its
// own rather than as data: it parses them in the origin that served them, and
// runs whatever script they contain.
//
// Every one of them is a way for somebody who can upload a file to run code on
// the application's origin, reached by uploading a file rather than by finding
// a bug. SVG is the one that surprises people — an image element carrying a
// script tag is still an image — and the XML pair is here because a stylesheet
// processing instruction turns a document into whatever the stylesheet says it
// is.
//
// The list is short and closed on purpose. It is not "types we distrust", which
// would be unbounded and would eventually catch a PDF somebody wanted rendered;
// it is the types a browser will execute, which is a question with an answer.
var activeContentTypes = []string{
	"text/html",
	"application/xhtml+xml",
	"image/svg+xml",
	"text/xml",
	"application/xml",
	"text/xsl",
}

// dispositionFor decides how the object is presented, from the content type the
// row records.
//
// An empty type is an attachment. A row that does not say what its object is
// leaves the answer to the browser, and the browser's answer is arrived at by
// looking at the bytes — which is exactly the guess nosniff exists to stop and
// exactly the guess an attacker gets to influence.
//
// No filename is attached to either. The row records a key, not a name: its
// last segment is whatever the consumer's key scheme put there, which for the
// scheme registry's own documentation demonstrates is "original.png" for every
// avatar in the system. Offering that as the name the browser saves under is
// worse than offering none, and a filename parameter is also the one part of
// this header that has to be escaped.
func dispositionFor(contentType string) string {
	base, _, _ := strings.Cut(contentType, ";")

	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" || slices.Contains(activeContentTypes, base) {
		return dispositionAttachment
	}

	return dispositionInline
}

// The two seeks that cannot be served. Neither is reachable from net/http,
// which is the only caller: they are the invariants of the type rather than
// answers anyone gets.
var (
	errNegativeSeek  = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "seek to a negative offset")
	errUnknownWhence = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "seek from an unknown whence")
)

// rangedObject presents an object in storage to net/http as an io.ReadSeeker,
// so that http.ServeContent can answer a Range request against it.
//
// Seeking is arithmetic: it moves an offset and drops whatever reader is open.
// Nothing is opened until a Read actually asks for a byte, and what is opened
// then starts at the offset the last seek left — so the whole of ServeContent's
// preamble, which seeks to the end to measure the object and back to the start
// to position it, costs no request at all, and a range costs exactly one.
//
// The reader it opens runs to the end of the object rather than to the end of
// the range, because ServeContent does not say how much it is about to copy —
// it seeks, then copies. The bytes past the range are never read: the copy
// stops at its own count and Close tears the reader down. What that costs is a
// ranged read of the remainder issued to the provider and abandoned, which is
// the price of getting net/http's conditional handling instead of parsing the
// Range header here.
//
// It is not safe for concurrent use and does not need to be: ServeContent reads
// it from the goroutine serving the request.
type rangedObject struct {
	// ctx is the request's, held because io.ReadSeeker has nowhere to pass one
	// and the provider call underneath needs it — so a client that disconnects
	// cancels the read it left in flight.
	ctx context.Context

	reader uploads.RangeReader
	open   io.ReadCloser

	key string

	// size is the row's, which is what the response claims. See serve.
	size   int64
	offset int64
}

var (
	_ io.ReadSeeker = (*rangedObject)(nil)
	_ io.Closer     = (*rangedObject)(nil)
)

func (o *rangedObject) Seek(offset int64, whence int) (int64, error) {
	var next int64

	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = o.offset + offset
	case io.SeekEnd:
		next = o.size + offset
	default:
		return 0, errUnknownWhence
	}

	if next < 0 {
		return 0, errNegativeSeek
	}

	if next == o.offset {
		return next, nil
	}

	// The open reader is positioned at the old offset and cannot be moved, so
	// it goes. The next Read opens one at the new offset, and a seek nobody
	// reads from opens nothing at all.
	if err := o.Close(); err != nil {
		return 0, err
	}

	o.offset = next

	return next, nil
}

func (o *rangedObject) Read(p []byte) (int, error) {
	// Past the end of what the row says the object is. Reported as the end of
	// the file rather than passed to the provider, so a row whose size overruns
	// its object and a client that asked for the last byte of one reach the
	// same answer.
	if o.offset >= o.size {
		return 0, io.EOF
	}

	if o.open == nil {
		opened, err := o.reader.OpenRange(o.ctx, o.key, o.offset, o.size-o.offset)
		if err != nil {
			return 0, err
		}

		o.open = opened
	}

	read, err := o.open.Read(p)
	o.offset += int64(read)

	return read, err
}

// Close releases whatever reader is open. It is idempotent, and a rangedObject
// that was never read from closes nothing.
func (o *rangedObject) Close() error {
	if o.open == nil {
		return nil
	}

	open := o.open
	o.open = nil

	return open.Close()
}
