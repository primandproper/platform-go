package passwordreset

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultTablePrefix is the namespace the token table carries when none is
// configured, which is none — rendering plain "password_reset_tokens".
//
// The password_reset segment is the schema's, not the caller's: a table always
// says which package created it. Setting a namespace of "app" renders
// app_password_reset_tokens, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// Config configures a SQLStore.
//
// It carries no dialect. The dialect comes from the database.Client, which is
// the only place it can come from and be right: a configured dialect that
// disagrees with the client it is paired with produces syntactically valid SQL
// the server rejects at runtime.
//
// It carries no token lifetime either. A TTL is a per-issuance argument because
// it is a per-issuance decision — an administrator resetting somebody else's
// password wants minutes where a self-service link wants an hour — and a
// configured default would be the value nobody passed rather than the value
// somebody chose.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the token table's name. Empty
	// renders the schema's own name, "password_reset_tokens"; set it to share a
	// database between applications, which renders e.g.
	// app_password_reset_tokens. It must not end in '_' — the separator is
	// supplied for you.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The prefix is vetted against the schema rather than against a pattern,
// because a prefix that is a legal identifier on its own can still push an
// index name past what the supported engines accept — and that failure would
// otherwise surface as a migration that half ran.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
	)
}
