ALTER TABLE evaluation_runs ADD COLUMN cache_hit BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE evaluation_runs ADD CHECK(NOT cache_hit OR(status='COMPLETED' AND final_verdict<>'CE' AND compile_evidence IS NOT NULL));
