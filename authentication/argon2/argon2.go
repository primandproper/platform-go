package argon2

import (
	"context"
	"math"
	"runtime"

	"github.com/primandproper/platform-go/authentication"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/alexedwards/argon2id"
)

var _ authentication.Authenticator = (*Argon2Authenticator)(nil)

const (
	serviceName        = "argon2"
	sixtyFourMegabytes = 2<<15 - 1
)

var (
	argonParams = &argon2id.Params{
		Memory:      sixtyFourMegabytes,
		Iterations:  1,
		Parallelism: uint8(math.Max(2, float64(runtime.NumCPU()))),
		SaltLength:  16,
		KeyLength:   32,
	}
)

type (
	// Argon2Authenticator is our argon2-based authenticator.
	Argon2Authenticator struct {
		o11y observability.Observer
	}
)

// ProvideArgon2Authenticator returns an argon2 powered Argon2Authenticator.
func ProvideArgon2Authenticator(logger logging.Logger, tracerProvider tracing.TracerProvider) authentication.Authenticator {
	return &Argon2Authenticator{
		o11y: observability.NewObserver(serviceName, logger, tracerProvider),
	}
}

// HashPassword takes a password and hashes it using argon2.
func (a *Argon2Authenticator) HashPassword(ctx context.Context, password string) (string, error) {
	_, op := a.o11y.Begin(ctx)
	defer op.End()

	op.SetValues(map[string]any{
		"argon2.memory":      argonParams.Memory,
		"argon2.iterations":  argonParams.Iterations,
		"argon2.parallelism": argonParams.Parallelism,
		"argon2.key_length":  argonParams.KeyLength,
	})

	return argon2id.CreateHash(password, argonParams)
}

// PasswordMatches reports whether password matches the argon2id-encoded hash.
// A non-match returns (false, nil); only genuine errors (malformed hash,
// runtime failure) populate err.
func (a *Argon2Authenticator) PasswordMatches(ctx context.Context, hash, password string) (bool, error) {
	_, op := a.o11y.Begin(ctx)
	defer op.End()

	op.SetValues(map[string]any{
		"argon2.memory":      argonParams.Memory,
		"argon2.iterations":  argonParams.Iterations,
		"argon2.parallelism": argonParams.Parallelism,
		"argon2.key_length":  argonParams.KeyLength,
	})

	matches, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return false, observability.PrepareError(err, op.Span(), "comparing argon2 hashed password")
	}

	return matches, nil
}
