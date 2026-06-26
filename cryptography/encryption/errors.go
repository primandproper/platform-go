package encryption

import (
	"github.com/primandproper/platform-go/errors"
)

var (
	ErrIncorrectKeyLength = errors.New("secret is not the right length")
)
