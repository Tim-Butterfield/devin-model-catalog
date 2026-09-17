-- Replace the unresolved/quarantined pair with one concrete state: rejected.
--
-- "Quarantined" meant "parsed, but this build does not recognise one of its
-- labels". Collectors no longer have that state: a harness or mode the build
-- has never seen now becomes its own evaluation context, so those rows are
-- ordinary observations. What remains is a row this build cannot represent
-- faithfully at all — no model identity, no usable score — which is a
-- rejection with an exact reason.
--
-- The migration therefore drops the previously quarantined rows rather than
-- recording them as rejections: they were never rejections, and the next
-- refresh ingests them as evidence. Rows that were unresolved and not
-- quarantined carry over unchanged, because they were rejections under both
-- models. Run counts are adjusted the same way, so the stored counts stay
-- consistent with the stored rows until the next refresh replaces both.

CREATE TABLE rejected_rows (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source_code       TEXT NOT NULL REFERENCES sources (code),
    run_id            INTEGER NOT NULL,
    source_model_name TEXT NOT NULL,
    source_row_ref    TEXT NOT NULL,
    reason            TEXT NOT NULL,
    details_json      TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO rejected_rows (source_code, run_id, source_model_name, source_row_ref, reason, details_json)
SELECT source_code, run_id, source_model_name, source_row_ref, reason, details_json
FROM unresolved_rows WHERE quarantined = 0;

DROP TABLE unresolved_rows;

ALTER TABLE source_runs RENAME COLUMN unresolved_rows TO rejected_rows;
UPDATE source_runs SET rejected_rows = max(rejected_rows - quarantined_rows, 0);
ALTER TABLE source_runs DROP COLUMN quarantined_rows;

-- A dataset row that collided with another on identity was recorded as
-- "quarantined"; that has always been a rejection with an exact reason, so it
-- keeps its reason and joins the rejected state. The CHECK constraint is the
-- reason this table is rebuilt rather than updated in place.
CREATE TABLE catalog_row_dispositions_new (
    row_ref     TEXT PRIMARY KEY,
    row_order   INTEGER NOT NULL,
    model_uid   TEXT NOT NULL,
    label       TEXT NOT NULL,
    disposition TEXT NOT NULL CHECK (disposition IN ('accepted', 'excluded', 'rejected')),
    reason      TEXT NOT NULL DEFAULT '',
    run_id      INTEGER NOT NULL
);

INSERT INTO catalog_row_dispositions_new (row_ref, row_order, model_uid, label, disposition, reason, run_id)
SELECT row_ref, row_order, model_uid, label,
       CASE disposition WHEN 'quarantined' THEN 'rejected' ELSE disposition END,
       reason, run_id
FROM catalog_row_dispositions;

DROP TABLE catalog_row_dispositions;
ALTER TABLE catalog_row_dispositions_new RENAME TO catalog_row_dispositions;
