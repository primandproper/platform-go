package fnv

import (
	"encoding/hex"
	"hash/fnv"

	"github.com/primandproper/platform-go/cryptography/hashing"
)

var _ hashing.Hasher = (*fnvHasher)(nil)

type (
	fnvHasher struct{}
)

func NewFNVHasher() hashing.Hasher {
	return &fnvHasher{}
}

func (s *fnvHasher) Hash(content string) (string, error) {
	return hex.EncodeToString(fnv.New128a().Sum([]byte(content))), nil
}
