package sha512

import (
	"crypto/sha512"
	"encoding/hex"

	"github.com/primandproper/platform-go/v2/cryptography/hashing"
)

var _ hashing.Hasher = (*sha512Hasher)(nil)

type (
	sha512Hasher struct{}
)

func NewSHA512Hasher() hashing.Hasher {
	return &sha512Hasher{}
}

func (s *sha512Hasher) Hash(content string) (string, error) {
	h := sha512.New()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
