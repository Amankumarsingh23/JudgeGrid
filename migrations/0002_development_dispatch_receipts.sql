-- Isolated development harness effects. These are NOT judge outcomes or attempt ownership.
CREATE TABLE development_dispatch_receipts (
 submission_id UUID NOT NULL REFERENCES submissions(id),
 attempt BIGINT NOT NULL CHECK (attempt > 0),
 consumer_id TEXT NOT NULL CHECK (length(consumer_id) BETWEEN 1 AND 200),
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (submission_id, attempt)
);
