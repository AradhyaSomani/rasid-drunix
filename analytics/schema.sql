-- RASID analytics schema. Runs on YugabyteDB (YSQL) and PostgreSQL. The dev
-- indexer also runs it on SQLite. One row per receivable, one per holding.
CREATE TABLE IF NOT EXISTS receivable (
    id            TEXT PRIMARY KEY,
    supplier_id   TEXT NOT NULL,
    buyer_id      TEXT NOT NULL,
    origin_msp    TEXT NOT NULL,
    e_invoice     BOOLEAN NOT NULL,
    amount_paise  BIGINT NOT NULL,
    funded_paise  BIGINT NOT NULL DEFAULT 0,
    repaid_paise  BIGINT NOT NULL DEFAULT 0,
    invoice_date  DATE NOT NULL,
    due_date      DATE NOT NULL,
    status        TEXT NOT NULL,
    risk_score    INTEGER,
    days_late     INTEGER NOT NULL DEFAULT 0,
    breach_45     BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS receivable_buyer ON receivable (buyer_id);
CREATE INDEX IF NOT EXISTS receivable_due ON receivable (due_date);

CREATE TABLE IF NOT EXISTS holding (
    receivable_id TEXT NOT NULL,
    holder_id     TEXT NOT NULL,
    units         BIGINT NOT NULL,
    PRIMARY KEY (receivable_id, holder_id)
);
