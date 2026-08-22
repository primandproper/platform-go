package httpclient

import (
	"bytes"
	"io"
	"net/http"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
)

// signingTransport stamps a request's signature headers immediately before it
// reaches the wire.
type signingTransport struct {
	base   http.RoundTripper
	signer requestsigning.Signer
	obs    *transportObserver
}

var _ http.RoundTripper = (*signingTransport)(nil)

// RoundTrip signs the request and sends it.
//
// The body is buffered, because a MAC over it cannot be computed any other way,
// and the buffered copy is installed on the request before the signer runs — so
// the bytes signed and the bytes sent are one reader over one slice, not two
// reads of one stream that a later edit could let diverge.
func (t *signingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readRequestBody(req)
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the request body to sign it")
	}

	// RoundTrip must not modify the request it is given, and a signature header
	// is a modification. Clone gives the copy a header map of its own.
	signed := req.Clone(req.Context())

	// The buffered bytes go on before the signer runs, not after, so the request
	// it reads and the request that goes to the wire are the same object. That
	// is the whole reason the signer takes a request: signing one payload and
	// transmitting another is not a mistake this shape can make.
	if body != nil {
		signed.Body = io.NopCloser(bytes.NewReader(body))
		signed.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		signed.ContentLength = int64(len(body))
	}

	// GetBody is set above, so the signer's read is a rewind rather than a
	// consumption and signed.Body still has every byte in it.
	if err = t.signer.SignRequest(req.Context(), signed); err != nil {
		t.obs.signingFailures.Add(req.Context(), 1, requestAttrs(req))

		// Error, not debug: a client that cannot sign sends nothing at all, and
		// the failure is a key source this process could not read rather than
		// anything the far side did.
		t.obs.o11y.Logger().WithRequest(req).WithValue(keys.SignatureSchemeKey, t.signer.Scheme()).
			Error("signing the outbound request", err)

		return nil, platformerrors.Wrap(err, "signing the request")
	}

	// Nothing is logged on the way through. Signing succeeds on every request a
	// working client makes, and a line per request — even at debug — costs a
	// logger allocation on the hot path to record that the ordinary thing
	// happened.
	return t.base.RoundTrip(signed)
}

// readRequestBody buffers a request's body so a fresh reader over it can be
// installed, and returns nil for a request that has none.
//
// It reads req.Body rather than going through requestsigning.RequestBody, which
// would prefer GetBody: those are not the same bytes when something above has
// already swapped one in. The retry transport hands each attempt a freshly
// rewound Body, and GetBody would hand back the original.
func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}

	body, err := io.ReadAll(req.Body)

	// Closed here rather than left to the base transport, which is handed a
	// fresh reader over these bytes and will never see this one. The close error
	// is dropped: the bytes are already in hand, and failing a request over a
	// complaint from a body that has been fully read helps nobody.
	_ = req.Body.Close() //nolint:errcheck // the body has been read; a close failure changes nothing

	if err != nil {
		return nil, err
	}

	return body, nil
}
