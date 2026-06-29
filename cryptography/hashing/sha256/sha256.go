package sha256

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/primandproper/platform-go/v2/cryptography/hashing"
)

var _ hashing.Hasher = (*sha256Hasher)(nil)

type (
	sha256Hasher struct{}
)

func NewSHA256Hasher() hashing.Hasher {
	return &sha256Hasher{}
}

func (s *sha256Hasher) Hash(content string) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
