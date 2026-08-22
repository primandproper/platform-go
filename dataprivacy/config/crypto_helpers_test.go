package dataprivacycfg

import (
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
)

// testKeyID names the single key in the test keyring.
const testKeyID encryption.KeyID = "test"

// newTestEncryptorDecryptor builds a one-key AES keyring.
func newTestEncryptorDecryptor(key []byte) (encryption.EncryptorDecryptor, error) {
	cipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return encryption.NewKeyring(testKeyID, []encryption.RingKey{{ID: testKeyID, Cipher: cipher}})
}
