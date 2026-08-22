package totp_test

import (
	"testing"
	"time"

	authtotp "github.com/primandproper/platform-go/v13/authentication/totp"

	"github.com/pquerna/otp/totp"
	"github.com/shoenig/test/must"
)

func BenchmarkVerifier_Verify(b *testing.B) {
	v := authtotp.NewVerifier()
	ctx := b.Context()
	const secret = "HEREISASECRETWHICHIVEMADEUPBECAUSEIWANNATESTRELIABLY"

	code, err := totp.GenerateCode(secret, time.Now().UTC())
	must.NoError(b, err)

	for b.Loop() {
		errSink = v.Verify(ctx, secret, code)
	}
}

var errSink error
