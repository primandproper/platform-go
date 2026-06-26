package sha256

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/primandproper/platform-go/cryptography/hashing"
)

var _ hashing.Hasher = (*sha256Hasher)(nil)

type (
	sha256Hasher struct{}
)

func NewSHA256Hasher() hashing.Hasher {
	return &sha256Hasher{}
}

func (s *sha256Hasher) Hash(content string) (string, error) {
	return hex.EncodeToString(sha256.New().Sum([]byte(content))), nil
}
