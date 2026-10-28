ALTER TABLE evaluation_runs ADD CONSTRAINT sandbox_run_identity UNIQUE(id,submission_id,generation);
CREATE TABLE sandbox_executions (
 execution_id TEXT PRIMARY KEY CHECK(execution_id ~ '^judgegrid-sandbox-[a-z2-7]{26}$'),
 controller_id TEXT NOT NULL CHECK(controller_id ~ '^[a-z2-7]{26}$'),
 run_id UUID NOT NULL,submission_id UUID NOT NULL,generation INTEGER NOT NULL CHECK(generation BETWEEN 1 AND 5),
 owner_hash TEXT NOT NULL CHECK(owner_hash ~ '^[0-9a-f]{64}$'),
 label_created TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 state TEXT NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','CLOSED','REAPED')),
 finished_at TIMESTAMPTZ,
 FOREIGN KEY(run_id,submission_id,generation) REFERENCES evaluation_runs(id,submission_id,generation),
 CHECK((state='OPEN' AND finished_at IS NULL) OR(state<>'OPEN' AND finished_at IS NOT NULL))
);
CREATE FUNCTION guard_sandbox_registry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'sandbox provenance is immutable' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.execution_id,NEW.controller_id,NEW.run_id,NEW.submission_id,NEW.generation,NEW.owner_hash,NEW.label_created,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.execution_id,OLD.controller_id,OLD.run_id,OLD.submission_id,OLD.generation,OLD.owner_hash,OLD.label_created,OLD.created_at)
 OR OLD.state='REAPED' OR NEW.state NOT IN ('CLOSED','REAPED') THEN RAISE EXCEPTION 'invalid sandbox registry update' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sandbox_registry_guard BEFORE UPDATE OR DELETE ON sandbox_executions FOR EACH ROW EXECUTE FUNCTION guard_sandbox_registry();
