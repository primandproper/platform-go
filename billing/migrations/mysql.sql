-- scope is whose billing this is: a reseller, a region, a brand, or — as the
-- empty string — nobody. Every read of every table here filters on it.
--
-- It has no default. The empty string is a scope, tenancy.Global(), and a
-- column that supplied it for a write which did not name one would hand out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists
-- to make unspellable in Go. NOT NULL with nothing to fall back on makes that
-- write fail instead. See the tenancy package.
--
-- The string columns here are VARCHAR where the Postgres schema says TEXT, for
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- the same ones the rest of this module uses — 64 for an id, 255 for a scope or
-- a name, 32 for a status word.
--
-- The unique indexes are declared inline as UNIQUE KEY because MySQL has no
-- CREATE UNIQUE INDEX IF NOT EXISTS, and the plain indexes carry archived_at in
-- the key where the other two dialects put it in a partial WHERE clause — MySQL
-- has no partial index, so the column joins the key rather than filtering it.
--
-- See billing/migrations/postgres.sql for what every column and index is for;
-- the reasoning is written once, there.
CREATE TABLE IF NOT EXISTS {{PREFIX}}billing_products (
    id                      VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                   VARCHAR(255) NOT NULL,
    name                    VARCHAR(255) NOT NULL,
    description             VARCHAR(1024) NOT NULL DEFAULT '',
    kind                    VARCHAR(32) NOT NULL,
    amount_cents            BIGINT NOT NULL,
    currency                VARCHAR(8) NOT NULL,
    billing_interval_months BIGINT,
    external_product_id     VARCHAR(255),
    created_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at         DATETIME(6),
    archived_at             DATETIME(6),
    UNIQUE KEY {{PREFIX}}billing_products_external_uniq (scope, external_product_id),
    KEY {{PREFIX}}billing_products_scope_idx (scope, archived_at, id)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}billing_subscriptions (
    id                       VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                    VARCHAR(255) NOT NULL,
    belongs_to_account       VARCHAR(64) NOT NULL,
    product_id               VARCHAR(64) NOT NULL,
    external_subscription_id VARCHAR(255),
    status                   VARCHAR(32) NOT NULL,
    current_period_start     DATETIME(6) NOT NULL,
    current_period_end       DATETIME(6) NOT NULL,
    created_at               DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at          DATETIME(6),
    archived_at              DATETIME(6),
    UNIQUE KEY {{PREFIX}}billing_subscriptions_external_uniq (scope, external_subscription_id),
    KEY {{PREFIX}}billing_subscriptions_account_idx
        (scope, belongs_to_account, archived_at, id),
    KEY {{PREFIX}}billing_subscriptions_current_idx
        (scope, belongs_to_account, archived_at, current_period_end, id),
    CONSTRAINT {{PREFIX}}billing_subscriptions_product_fk
        FOREIGN KEY (product_id) REFERENCES {{PREFIX}}billing_products (id)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}billing_purchases (
    id                      VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                   VARCHAR(255) NOT NULL,
    belongs_to_account      VARCHAR(64) NOT NULL,
    product_id              VARCHAR(64) NOT NULL,
    external_transaction_id VARCHAR(255),
    amount_cents            BIGINT NOT NULL,
    currency                VARCHAR(8) NOT NULL,
    completed_at            DATETIME(6),
    created_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at         DATETIME(6),
    archived_at             DATETIME(6),
    UNIQUE KEY {{PREFIX}}billing_purchases_external_uniq (scope, external_transaction_id),
    KEY {{PREFIX}}billing_purchases_account_idx
        (scope, belongs_to_account, archived_at, id),
    CONSTRAINT {{PREFIX}}billing_purchases_product_fk
        FOREIGN KEY (product_id) REFERENCES {{PREFIX}}billing_products (id)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}billing_transactions (
    id                      VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                   VARCHAR(255) NOT NULL,
    belongs_to_account      VARCHAR(64) NOT NULL,
    subscription_id         VARCHAR(64),
    purchase_id             VARCHAR(64),
    external_transaction_id VARCHAR(255),
    status                  VARCHAR(32) NOT NULL,
    amount_cents            BIGINT NOT NULL,
    currency                VARCHAR(8) NOT NULL,
    created_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at         DATETIME(6),
    archived_at             DATETIME(6),
    UNIQUE KEY {{PREFIX}}billing_transactions_external_uniq (scope, external_transaction_id),
    KEY {{PREFIX}}billing_transactions_account_idx
        (scope, belongs_to_account, archived_at, id),
    CONSTRAINT {{PREFIX}}billing_transactions_subscription_fk
        FOREIGN KEY (subscription_id) REFERENCES {{PREFIX}}billing_subscriptions (id),
    CONSTRAINT {{PREFIX}}billing_transactions_purchase_fk
        FOREIGN KEY (purchase_id) REFERENCES {{PREFIX}}billing_purchases (id)
);
