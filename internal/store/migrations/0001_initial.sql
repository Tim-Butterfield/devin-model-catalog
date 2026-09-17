-- Initial schema.
--
-- Metrics are rows, not columns: a new benchmark or catalog attribute is a new
-- row in `metrics` plus rows in `observations`. No table here names a specific
-- benchmark.
--
-- The database holds current state for one selected Devin dataset. It is not a
-- history: refreshes replace what a source last published.

CREATE TABLE sources (
    code         TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('builtin', 'manual')),
    homepage     TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    retrieval    TEXT NOT NULL DEFAULT '',
    access_basis TEXT NOT NULL DEFAULT '',
    license      TEXT NOT NULL DEFAULT '',
    attribution  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE metrics (
    key              TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL,
    description      TEXT NOT NULL,
    value_kind       TEXT NOT NULL CHECK (value_kind IN ('number', 'integer', 'boolean', 'text')),
    unit             TEXT NOT NULL DEFAULT '',
    direction        TEXT NOT NULL CHECK (direction IN ('higher_is_better', 'lower_is_better', 'neutral')),
    scope            TEXT NOT NULL CHECK (scope IN ('catalog', 'evidence')),
    defined_by       TEXT NOT NULL REFERENCES sources (code),
    missing_possible INTEGER NOT NULL DEFAULT 1,
    status           TEXT NOT NULL DEFAULT 'current' CHECK (status IN ('current', 'deprecated'))
);

CREATE TABLE evaluation_contexts (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('devin', 'published', 'external_harness', 'direct'))
);

-- The currently published dataset inventory. IDs are compact 1..N selection
-- handles regenerated on every inventory refresh; they are never persisted
-- as the identity of a selection.
CREATE TABLE devin_datasets (
    id                 INTEGER PRIMARY KEY CHECK (id >= 1),
    source_key         TEXT NOT NULL UNIQUE,
    display_name       TEXT NOT NULL,
    component          TEXT NOT NULL,
    attributes_json    TEXT NOT NULL,
    position           INTEGER NOT NULL,
    supported          INTEGER NOT NULL,
    unsupported_reason TEXT NOT NULL DEFAULT '',
    model_row_count    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE dataset_inventory (
    singleton      INTEGER PRIMARY KEY CHECK (singleton = 1),
    refreshed_at   TEXT NOT NULL,
    source_url     TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    dataset_count  INTEGER NOT NULL,
    warnings_json  TEXT NOT NULL DEFAULT '[]'
);

-- The selected dataset, persisted by structural identity, not by its number.
CREATE TABLE selected_dataset (
    singleton            INTEGER PRIMARY KEY CHECK (singleton = 1),
    source_key           TEXT NOT NULL,
    display_name         TEXT NOT NULL,
    component            TEXT NOT NULL,
    attributes_json      TEXT NOT NULL,
    selected_at          TEXT NOT NULL,
    catalog_state        TEXT NOT NULL CHECK (catalog_state IN ('needs_refresh', 'current')),
    catalog_refreshed_at TEXT,
    catalog_run_id       INTEGER
);

CREATE TABLE source_runs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source_code       TEXT NOT NULL REFERENCES sources (code) ON DELETE CASCADE,
    started_at        TEXT NOT NULL,
    finished_at       TEXT,
    status            TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed')),
    error             TEXT NOT NULL DEFAULT '',
    published_rows    INTEGER NOT NULL DEFAULT 0,
    accepted_rows     INTEGER NOT NULL DEFAULT 0,
    unresolved_rows   INTEGER NOT NULL DEFAULT 0,
    quarantined_rows  INTEGER NOT NULL DEFAULT 0,
    excluded_rows     INTEGER NOT NULL DEFAULT 0,
    observation_count INTEGER NOT NULL DEFAULT 0,
    warnings_json     TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX source_runs_by_source ON source_runs (source_code, id);

-- Current catalog models of the selected dataset only.
CREATE TABLE catalog_models (
    uid             TEXT PRIMARY KEY,
    row_order       INTEGER NOT NULL,
    label           TEXT NOT NULL,
    provider        TEXT NOT NULL,
    base_name       TEXT NOT NULL,
    base_key        TEXT NOT NULL,
    effort          TEXT NOT NULL DEFAULT '',
    serving_variant TEXT NOT NULL DEFAULT '',
    context_variant TEXT NOT NULL DEFAULT '',
    canonical_key   TEXT NOT NULL,
    raw_json        TEXT NOT NULL,
    run_id          INTEGER NOT NULL
);

-- What became of every published row of the selected dataset.
CREATE TABLE catalog_row_dispositions (
    row_ref     TEXT PRIMARY KEY,
    row_order   INTEGER NOT NULL,
    model_uid   TEXT NOT NULL,
    label       TEXT NOT NULL,
    disposition TEXT NOT NULL CHECK (disposition IN ('accepted', 'excluded', 'quarantined', 'rejected')),
    reason      TEXT NOT NULL DEFAULT '',
    run_id      INTEGER NOT NULL
);

-- Current observations. catalog_uid is set only for catalog-scope metrics.
CREATE TABLE observations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    source_code        TEXT NOT NULL REFERENCES sources (code),
    run_id             INTEGER NOT NULL,
    metric_key         TEXT NOT NULL REFERENCES metrics (key),
    catalog_uid        TEXT NOT NULL DEFAULT '',
    source_model_name  TEXT NOT NULL,
    source_row_ref     TEXT NOT NULL,
    base_key           TEXT NOT NULL,
    effort             TEXT NOT NULL DEFAULT '',
    serving_variant    TEXT NOT NULL DEFAULT '',
    context_variant    TEXT NOT NULL DEFAULT '',
    evaluation_context TEXT NOT NULL REFERENCES evaluation_contexts (code),
    value_number       REAL,
    value_text         TEXT,
    details_json       TEXT NOT NULL DEFAULT '{}',
    observed_at        TEXT NOT NULL,
    CHECK ((value_number IS NULL) <> (value_text IS NULL)),
    UNIQUE (source_code, metric_key, source_row_ref, catalog_uid)
);

CREATE INDEX observations_by_metric ON observations (metric_key);
CREATE INDEX observations_by_base ON observations (base_key);

-- Published rows that did not become observations, with the reason.
CREATE TABLE unresolved_rows (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    source_code        TEXT NOT NULL REFERENCES sources (code),
    run_id             INTEGER NOT NULL,
    source_model_name  TEXT NOT NULL,
    source_row_ref     TEXT NOT NULL,
    reason             TEXT NOT NULL,
    quarantined        INTEGER NOT NULL DEFAULT 0,
    discovered_context TEXT NOT NULL DEFAULT '',
    details_json       TEXT NOT NULL DEFAULT '{}'
);

-- Confirmed identity decisions made by the user. They survive refreshes and
-- are applied before automatic matching.
CREATE TABLE model_aliases (
    source_code            TEXT NOT NULL,
    source_name_normalized TEXT NOT NULL,
    source_name            TEXT NOT NULL,
    target_label           TEXT NOT NULL,
    base_key               TEXT NOT NULL,
    effort                 TEXT NOT NULL DEFAULT '',
    note                   TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    PRIMARY KEY (source_code, source_name_normalized)
);
