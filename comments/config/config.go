/*
Package commentscfg assembles a comments Store from environment configuration.

There is one thing to configure and one thing to build, which is why this package
is smaller than most of its siblings: the dialect comes from the database.Client
so it cannot disagree with the database the statements run against, and
everything else about the store is either the schema's or an option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

What is deliberately not configurable is the target catalog. Which kinds of thing
this application accepts comments on is a declaration in Go — a set of
comments.TargetType constants, each optionally carrying an existence check that
reads the consumer's own tables — and no environment variable can express a
function. So it is a parameter of [NewStore] rather than a field on Config, the
same way webhooks takes its event catalog, and [RegisterStore] resolves it from
the injector.

The privacy seam is not here either, and that absence is the same reading the
composition root takes of every registry: comments/privacy needs a
ScopeResolver, which is a mapping from a person to the tenants they belong to,
and no environment variable can express one. A service that wants its comments in
its subject access requests registers the collector and the eraser itself, with
the store this package built.
*/
package commentscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a comments Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the comments table. It must match the prefix the
	// migrations were rendered with. Defaults to comments.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = comments.DefaultTablePrefix
	}
}

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders rather than
// against a pattern, so a prefix that is legal in isolation but produces an
// over-long index name fails here instead of at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg); err != nil {
		return err
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the comments
// table, and targets is the application's declaration of what can be commented
// on.
//
// An empty catalog is not refused here, and that is the leaf package's ruling
// rather than a gap in this one: a store with no targets accepts no writes, which
// is a wiring mistake that fails on the first comment rather than one that stores
// rows nothing will list. See comments.NewSQLStore.
//
// The store is built into a variable and returned only once its error is known
// to be nil. comments.NewSQLStore returns its own concrete type, so returning it
// straight through would convert a nil *comments.SQLStore into a non-nil
// comments.Store on the error path, and a caller testing the result against nil
// would find a store that panics on first use.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	targets comments.Targets,
	opts ...Option,
) (comments.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating comments config")
	}

	options := newOptions(opts)

	base := []comments.SQLStoreOption{
		comments.WithTablePrefix(cfg.TablePrefix),
		comments.WithTargets(targets),
		comments.WithStoreLogger(options.logger),
		comments.WithStoreTracerProvider(options.tracerProvider),
		comments.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := comments.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}
