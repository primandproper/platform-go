-- One table. A privacy request is a single row with a status, and the prior art
-- that split it across a request table and a disclosure table gained nothing
-- from the join except the possibility of the two disagreeing about whether an
-- artifact still existed.
CREATE TABLE IF NOT EXISTS {{PREFIX}}dataprivacy_requests (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    request_type    VARCHAR(32) NOT NULL,
    status          VARCHAR(32) NOT NULL,
    operation_id    VARCHAR(64) NOT NULL DEFAULT '',
    subject_id      VARCHAR(255) NOT NULL,
    subject_type    VARCHAR(64) NOT NULL DEFAULT '',
    -- subject_scope is whose request this is: a reseller, a region, a product,
    -- or — as the empty string — nobody. A read that names a subject names their
    -- scope with it, and the one read that deliberately does not says so in its
    -- own name.
    --
    -- It has no default, unlike the text columns around it. Their empty string
    -- is an absence — a request no operation drove, a subject whose type the
    -- caller did not name, an artifact never written — and a write that omits
    -- one means exactly that. This one's empty string is not an absence but a
    -- value, tenancy.Global(), so a default would hand the global scope to a
    -- write that forgot the column, filing a subject's request in the tenant
    -- that matches nobody — the mistake tenancy.Scope exists to make
    -- unspellable in Go. NOT NULL with nothing to fall back on makes that write
    -- fail instead. See the tenancy package.
    subject_scope   VARCHAR(255) NOT NULL,
    -- The convention triple. created_at is when the request was submitted — the
    -- instant the statutory clock starts — and no longer wears a second name for
    -- the row's creation time. last_updated_at is NULL until the fulfiller first
    -- moves the request. archived_at is written by nothing in this package: a
    -- served request is reaped on its retention window rather than hidden, and
    -- the column is here because a table querygen can read a shape from has all
    -- three of these or none of them.
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    due_at          DATETIME(6) NOT NULL,
    expires_at      DATETIME(6),
    completed_at    DATETIME(6),
    artifact_ref    TEXT NOT NULL,
    artifact_bytes  BIGINT NOT NULL DEFAULT 0,
    deleted_rows    BIGINT NOT NULL DEFAULT 0,
    anonymized_rows BIGINT NOT NULL DEFAULT 0,
    failures        BLOB,
    retained        BLOB,
    last_error      TEXT,
    key_shredded_at DATETIME(6),

    -- MySQL has no partial indexes, so unlike the Postgres schema the ones
    -- below cover the whole table and the predicate columns lead. Every query
    -- they serve filters on status first, so putting it in front keeps the
    -- index selective for the same queries the partial clauses serve elsewhere.
    --
    -- "What has been asked in this person's name." Leading with the subject
    -- rather than the time is what makes List a range scan instead of a filter
    -- over every request the system has ever served.
    KEY {{PREFIX}}dataprivacy_requests_subject_idx
        (subject_id, subject_scope, created_at, id),

    -- Serves both the artifact expiry sweep and the confirmation-window lapse
    -- sweep; they differ only in the status they filter on, which leads the
    -- index.
    KEY {{PREFIX}}dataprivacy_requests_expiry_idx (status, expires_at),

    -- Serves the overdue gauge.
    KEY {{PREFIX}}dataprivacy_requests_status_due_idx (status, due_at),

    -- Serves the retention reap.
    KEY {{PREFIX}}dataprivacy_requests_reap_idx (completed_at, id)
);
