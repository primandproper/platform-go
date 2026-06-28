package crc64

import (
	"encoding/hex"
	"hash/crc64"

	"github.com/primandproper/platform-go/cryptography/hashing"
)

var _ hashing.Hasher = (*crc64Hasher)(nil)

type (
	crc64Hasher struct{}
)

func NewCRC64Hasher() hashing.Hasher {
	return &crc64Hasher{}
}

func (s *crc64Hasher) Hash(content string) (string, error) {
	h := crc64.New(crc64.MakeTable(crc64.ISO))
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
