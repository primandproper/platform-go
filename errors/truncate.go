package errors

import (
	"github.com/primandproper/platform-go/v13/charset"
)

// TruncateError renders err for storage in a bounded column — a last_error, a
// recorded delivery attempt, a failure map — cutting the message to at most
// limit bytes. A nil error renders as the empty string.
//
// Several packages in this module keep the cause of a failed attempt on the row
// it failed against, so an operator reading the table learns why without
// correlating logs. That rendering has to be bounded, because the cause is a
// driver's or a remote server's string and nothing stops it from being a
// megabyte; and it has to stay valid UTF-8, because the column it lands in
// rejects anything else. Cutting on a byte index satisfies the first and breaks
// the second, which is why this delegates to charset.TruncateUTF8 rather than
// slicing.
//
// A caller storing into a nullable column and distinguishing "never failed"
// from "failed with an empty message" wants NULL rather than the empty string
// for a nil error, and should check for nil before calling this.
func TruncateError(err error, limit int) string {
	if err == nil {
		return ""
	}

	return charset.TruncateUTF8(err.Error(), limit)
}
