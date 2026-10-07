-- The table recording where each login was last renewed from.
--
-- signin records how a login happened and nothing about the device behind it;
-- whether that is recorded at all is the consumer's decision. This is the table
-- a consumer who decided yes gets instead of writing one: a row per login,
-- written by the AfterIssueToken hook on the token's own transaction and
-- rewritten by every refresh, so it says where the login was last renewed from,
-- which is the device holding it now.
--
-- scope is whose directory the login is in — a reseller, a region, a product,
-- or, as the empty string, nobody. It is the scope the sign-in that minted the
-- login ran in, and every statement over this table filters on it but the
-- sweep, which spans every scope.
--
-- It has no default, deliberately. The empty string is a scope,
-- tenancy.Global(), and a column that supplies it for a write which did not name
-- one hands out the global scope to whoever forgot the column — the mistake
-- tenancy.Scope exists to make unspellable in Go. NOT NULL with nothing to fall
-- back on makes that write fail instead. See the tenancy package.
--
-- family_id is which login. It is signin's family identifier — the "sid" a
-- token carries and the key ListSignIns answers each login under — and it is the
-- key here because a login, not a token, is what a person sees listed: every
-- refresh mints a token in the same family, and lands on the same row.
--
-- user_id is whose login it is. It carries no REFERENCES, for the reason the
-- other signin tables carry none: this package is usable by an application whose
-- directory is not identity's, and a foreign key would make adopting a device
-- list mean adopting identity too. It is also why an erasure has to reach this
-- table on purpose — see the privacy package.
--
-- ip_address, user_agent and device_name are what the consumer's extractor read
-- off the request, bounded in Go before they are bound. They are what a client
-- says about itself, or what the deployment's edge says about the client, and
-- they are display rather than evidence.
--
-- first_seen_at is when the login was first recorded, and last_seen_at when it
-- was last renewed. Both are the store's clock rather than the server's, because
-- expires_at is a deadline signin computed on its own clock and the sweep
-- compares it against the store's, so one clock stamps every column.
--
-- expires_at is the latest the login could still be alive: its refresh token's
-- deadline, or its access token's for a login that has none. The sweep deletes
-- the row once it passes.
--
-- No convention triple. archived_at would keep a row nothing reads alive, and
-- last_updated_at would be a second name for last_seen_at, which is the only
-- mutation this row has.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_devices (
    scope         TEXT NOT NULL,
    family_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    ip_address    TEXT NOT NULL,
    user_agent    TEXT NOT NULL,
    device_name   TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (scope, family_id)
);

-- Serves both reads keyed on a person: the annotator's, over the families one
-- listing returned, and the export's, over all of them.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_devices_user_idx
    ON {{PREFIX}}signin_devices (scope, user_id);

-- Serves the sweep, which collects every row past its deadline across every
-- scope.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_devices_expires_at_idx
    ON {{PREFIX}}signin_devices (expires_at);
