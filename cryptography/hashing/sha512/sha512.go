package sha512

import (
	"crypto/sha512"
	"encoding/hex"

	"github.com/primandproper/platform-go/cryptography/hashing"
)

var _ hashing.Hasher = (*sha512Hasher)(nil)

type (
	sha512Hasher struct{}
)

func NewSHA512Hasher() hashing.Hasher {
	return &sha512Hasher{}
}

func (s *sha512Hasher) Hash(content string) (string, error) {
	return hex.EncodeToString(sha512.New().Sum([]byte(content))), nil
}
