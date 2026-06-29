package adler32

import (
	"encoding/hex"
	"hash/adler32"

	"github.com/primandproper/platform-go/v2/cryptography/hashing"
)

var _ hashing.Hasher = (*adler32Hasher)(nil)

type (
	adler32Hasher struct{}
)

func NewAdler32Hasher() hashing.Hasher {
	return &adler32Hasher{}
}

func (s *adler32Hasher) Hash(content string) (string, error) {
	h := adler32.New()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
