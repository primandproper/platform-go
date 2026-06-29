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
	h := fnv.New128a()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
