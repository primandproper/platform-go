package dataprivacy

import (
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
)

// testRequestID is the request identity the packager tests bind their
// ciphertexts to. Encode and decode have to agree on it, because disagreeing
// is precisely what associated data makes fail.
const testRequestID = "req_test"

// testKeyID names the single key in the test keyring.
const testKeyID encryption.KeyID = "test"

// testKeyMaterial is 32 bytes, which is what AES-256 wants.
var testKeyMaterial = []byte("0123456789abcdef0123456789abcdef")

// newTestEncryptorDecryptor builds a one-key AES keyring.
//
// It takes the key material rather than closing over it so that a test wanting
// two rings that disagree — the case where decryption should fail — can build
// them from different bytes.
func newTestEncryptorDecryptor(key []byte) (encryption.EncryptorDecryptor, error) {
	cipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return encryption.NewKeyring(testKeyID, []encryption.RingKey{{ID: testKeyID, Cipher: cipher}})
}
