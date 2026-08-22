package database

import (
	"context"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server/database/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Store.
//
// It carries no dialect. The dialect comes from the database.Client, which is
// the only place it can come from and be right: a configured dialect that
// disagrees with the client it is paired with produces syntactically valid SQL
// the server rejects at runtime.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to this package's four table
	// names. Empty renders the schema's own names; set it to share a database
	// between applications, which renders e.g. ddb_oauth2_clients. It must not
	// end in '_' — the separator is supplied for you.
	//
	// The longest identifier here is 49 bytes before a prefix is applied, so
	// there is less room than in most schemas in this module; a prefix that
	// works elsewhere can be rejected here.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The prefix is vetted against the schema rather than against a pattern,
// because a prefix that is a legal identifier on its own can still push an
// index name past what the supported engines accept — and that failure would
// otherwise surface as a migration that half ran, leaving two of four tables.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
	)
}
