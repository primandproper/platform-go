package devices

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a SQLStore.
//
// It carries no dialect. The dialect comes from the database.Client, which is
// the only place it can come from and be right: a configured dialect that
// disagrees with the client it is paired with produces syntactically valid SQL
// the server rejects at runtime.
//
// It carries no sweep interval. Whether this process sweeps is WithSweeper's, a
// context and an interval a caller holds rather than an environment variable,
// because a fleet wants one sweeper rather than one per replica.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the sign-in device table's name.
	// Empty renders the schema's own name, "signin_devices"; set it to share a
	// database between applications, which renders e.g. app_signin_devices. It
	// must not end in '_' — the separator is supplied for you.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The prefix is vetted against the schema rather than against a pattern, because
// a prefix that is a legal identifier on its own can still push an index name
// past what the supported engines accept — and that failure would otherwise
// surface as a migration that half ran.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
	)
}
