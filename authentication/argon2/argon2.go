package argon2

import (
	"context"
	"runtime"

	"github.com/primandproper/platform-go/v4/authentication"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/alexedwards/argon2id"
)

var _ authentication.Authenticator = (*Argon2Authenticator)(nil)

const (
	serviceName = "argon2"
	// sixtyFourMegabytes is argon2's memory cost, expressed in KiB.
	sixtyFourMegabytes = 64 * 1024

	// minParallelism and maxParallelism bound the argon2 parallelism degree.
	// The lower bound keeps a floor of concurrency; the upper bound prevents an
	// overflow to 0 when uint8-narrowing runtime.NumCPU() on hosts with >255
	// CPUs, which would panic x/crypto argon2 (parallelism degree too low).
	minParallelism = 2
	maxParallelism = 255
)

var (
	argonParams = &argon2id.Params{
		Memory:      sixtyFourMegabytes,
		Iterations:  1,
		Parallelism: uint8(min(maxParallelism, max(minParallelism, runtime.NumCPU()))),
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
