package adler32

import (
	"encoding/hex"
	"hash/adler32"

	"github.com/primandproper/platform-go/cryptography/hashing"
)

var _ hashing.Hasher = (*adler32Hasher)(nil)

type (
	adler32Hasher struct{}
)

func NewAdler32Hasher() hashing.Hasher {
	return &adler32Hasher{}
}

func (s *adler32Hasher) Hash(content string) (string, error) {
	return hex.EncodeToString(adler32.New().Sum([]byte(content))), nil
}
