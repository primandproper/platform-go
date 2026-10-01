package database

import (
	"context"

	"github.com/primandproper/platform-go/v14/links/database/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Store.
//
// It carries no dialect. The dialect comes from the database.Client, which is
// the only place it can come from and be right: a configured dialect that
// disagrees with the client it is paired with produces syntactically valid SQL
// the server rejects at runtime.
//
// It carries no retention window and no lifetime either. Both belong to the
// Minter — a link's expiry comes from the action's policy and its purge
// deadline from links.WithRetention — and a second copy of either here would be
// the value nobody passed competing with the value somebody chose.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the action link table's name.
	// Empty renders the schema's own name, "action_links"; set it to share a
	// database between applications, which renders e.g. app_action_links. It
	// must not end in '_' — the separator is supplied for you.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The prefix is vetted against the schema rather than against a pattern,
// because a prefix that is a legal identifier on its own can still push the
// index name past what the supported engines accept — and that failure would
// otherwise surface as a migration that half ran.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
	)
}
