package files

import (
	"github.com/primandproper/platform-go/errors"
)

var (
	// ErrOffsetBeyondEOF is returned when a slice offset lands at or past the end of the input.
	ErrOffsetBeyondEOF = errors.New("offset is at or beyond end of input")
	// ErrNonPositiveChunkSize is returned when a chunk size is zero or negative.
	ErrNonPositiveChunkSize = errors.New("chunk size must be greater than zero")
	// ErrNegativeOffset is returned when a slice offset is negative.
	ErrNegativeOffset = errors.New("offset must not be negative")
	// ErrNegativeCount is returned when a slice count is negative.
	ErrNegativeCount = errors.New("count must not be negative")
)
