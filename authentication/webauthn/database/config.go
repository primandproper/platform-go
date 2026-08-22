package database

import (
	"context"

	"github.com/primandproper/platform-go/v13/authentication/webauthn/database/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultTablePrefix is the namespace the ceremony session table carries when
// none is configured, which is none — rendering plain "webauthn_sessions".
//
// The webauthn segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_webauthn_sessions, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// Config configures a SessionStore.
//
// It carries no dialect. The dialect comes from the database.Client, which is
// the only place it can come from and be right: a configured dialect that
// disagrees with the client it is paired with produces syntactically valid SQL
// the server rejects at runtime.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the ceremony session table's
	// name. Empty renders the schema's own name, "webauthn_sessions"; set it to
	// share a database between applications, which renders e.g.
	// ddb_webauthn_sessions. It must not end in '_' — the separator is supplied
	// for you.
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
