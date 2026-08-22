package encryption

import (
	"github.com/primandproper/platform-go/v13/errors"
)

// frameVersion is the layout version written at the front of every ciphertext.
//
// It exists so that a future change to the frame is a decision rather than a
// corruption: a build that meets a version it does not know refuses the
// ciphertext instead of misreading the bytes after it. Bump it only for a
// layout change, never for a cipher change — which key and which algorithm are
// what the key ID already answers.
const frameVersion byte = 1

// headerOverhead is the version byte plus the key ID length byte.
const headerOverhead = 2

// encodeHeader renders the leading bytes of a ciphertext:
//
//	version (1) || len(keyID) (1) || keyID
//
// The key ID travels in the clear. It has to: decryption has to know which key
// to reach for before it can authenticate anything, which is why a KeyID must
// name a key without describing it.
func encodeHeader(id KeyID) ([]byte, error) {
	switch {
	case id == "":
		return nil, ErrEmptyKeyID
	case len(id) > MaxKeyIDLength:
		return nil, errors.Wrapf(ErrKeyIDTooLong, "%d bytes exceeds %d", len(id), MaxKeyIDLength)
	}

	header := make([]byte, 0, headerOverhead+len(id))
	header = append(header, frameVersion, byte(len(id)))
	header = append(header, id...)

	return header, nil
}

// decodeHeader splits a ciphertext into the key it names, the header bytes
// that name it, and the body the Cipher produced.
//
// The header is returned alongside the key ID because it is authenticated
// data, and authenticating it means re-supplying the exact bytes that were
// authenticated rather than a re-encoding of them.
func decodeHeader(ciphertext []byte) (id KeyID, header, body []byte, err error) {
	if len(ciphertext) < headerOverhead {
		return "", nil, nil, errors.Wrapf(ErrMalformedCiphertext, "%d bytes cannot hold a header", len(ciphertext))
	}

	if v := ciphertext[0]; v != frameVersion {
		return "", nil, nil, errors.Wrapf(ErrUnsupportedCiphertextVersion, "version %d", v)
	}

	idLen := int(ciphertext[1])
	if idLen == 0 {
		return "", nil, nil, errors.Wrap(ErrMalformedCiphertext, "header declares an empty key ID")
	}

	end := headerOverhead + idLen
	if len(ciphertext) < end {
		return "", nil, nil, errors.Wrapf(
			ErrMalformedCiphertext,
			"header declares a %d byte key ID but only %d bytes follow",
			idLen, len(ciphertext)-headerOverhead,
		)
	}

	return KeyID(ciphertext[headerOverhead:end]), ciphertext[:end], ciphertext[end:], nil
}

// bindHeader is the associated data a Cipher actually sees: the frame header
// followed by whatever the caller supplied.
//
// It always copies. Both callers go on to append to the header they passed in,
// and sharing a backing array between the associated data and the ciphertext
// being assembled would corrupt one of them in exactly the cases where the
// header's capacity happened to be large enough — which is to say
// intermittently, and only in production.
func bindHeader(header, associatedData []byte) []byte {
	bound := make([]byte, 0, len(header)+len(associatedData))
	bound = append(bound, header...)
	bound = append(bound, associatedData...)

	return bound
}
