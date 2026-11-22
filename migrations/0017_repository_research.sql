CREATE TABLE research_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 200),
 repository uuid NOT NULL REFERENCES source_repositories(id),
 snapshot text NOT NULL REFERENCES source_snapshots(id),
 analysis text NOT NULL REFERENCES analysis_indexes(id),
 retrieval text NOT NULL REFERENCES retrieval_indexes(id),
 commit_oid text NOT NULL,
 planner text NOT NULL,
 goal text NOT NULL CHECK(octet_length(goal) BETWEEN 1 AND 256),
 state text NOT NULL DEFAULT 'CREATED' CHECK(state IN ('CREATED','PLANNING','RUNNING','WAITING_APPROVAL','COMPLETED','FAILED','CANCELLED','EXPIRED')),
 generation bigint NOT NULL DEFAULT 0,
 planner_calls int NOT NULL DEFAULT 0 CHECK(planner_calls BETWEEN 0 AND 12),
 claim uuid,
 lease_until timestamptz,
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '15 minutes',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 failure text NOT NULL DEFAULT '' CHECK(failure IN ('','policy','backend','budget')),
 outcome text NOT NULL DEFAULT '' CHECK(outcome IN ('','NO_EVIDENCE','INSUFFICIENT_EVIDENCE','VERIFIED_REFERENCES')),
 CHECK ((state IN ('RUNNING','PLANNING'))=(claim IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK ((state='COMPLETED')=(outcome<>''))
);
CREATE INDEX research_runs_owner ON research_runs(owner, created_at DESC);
CREATE TABLE research_steps (
 run_id uuid NOT NULL REFERENCES research_runs(id),
 ordinal int NOT NULL CHECK(ordinal BETWEEN 1 AND 12),
 action_sha256 text NOT NULL CHECK(action_sha256 ~ '^[a-f0-9]{64}$'),
 decision jsonb NOT NULL CHECK(octet_length(decision::text)<=4096),
 observation jsonb CHECK(octet_length(observation::text)<=32768),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(run_id, ordinal)
);
CREATE TABLE research_attempts (
 run_id uuid NOT NULL,
 ordinal int NOT NULL,
 attempt int NOT NULL CHECK(attempt BETWEEN 1 AND 3),
 generation bigint NOT NULL CHECK(generation>0),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(run_id,ordinal,attempt),
 FOREIGN KEY(run_id,ordinal) REFERENCES research_steps(run_id,ordinal)
);
CREATE TRIGGER research_attempt_immutable BEFORE UPDATE OR DELETE ON research_attempts FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TABLE research_approvals (
 run_id uuid NOT NULL,
 ordinal int NOT NULL,
 action_sha256 text NOT NULL CHECK(action_sha256 ~ '^[a-f0-9]{64}$'),
 requested_by text NOT NULL,
 decided_by text,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','GRANTED','DENIED','EXPIRED','CANCELLED')),
 expires_at timestamptz NOT NULL,
 decided_at timestamptz,
 PRIMARY KEY(run_id,ordinal),
 FOREIGN KEY(run_id,ordinal) REFERENCES research_steps(run_id,ordinal),
 CHECK ((state='PENDING')=(decided_at IS NULL)),
 CHECK ((state IN ('GRANTED','DENIED'))=(decided_by IS NOT NULL))
);
CREATE FUNCTION research_preserve_step() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'research step is immutable'; END IF;
 IF OLD.observation IS NOT NULL OR (to_jsonb(NEW)-'observation') IS DISTINCT FROM (to_jsonb(OLD)-'observation') OR NEW.observation IS NULL
 THEN RAISE EXCEPTION 'research step is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER research_step_immutable BEFORE UPDATE OR DELETE ON research_steps FOR EACH ROW EXECUTE FUNCTION research_preserve_step();
CREATE FUNCTION research_preserve_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.owner,NEW.repository,NEW.snapshot,NEW.analysis,NEW.retrieval,NEW.commit_oid,NEW.planner,NEW.goal,NEW.deadline,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.owner,OLD.repository,OLD.snapshot,OLD.analysis,OLD.retrieval,OLD.commit_oid,OLD.planner,OLD.goal,OLD.deadline,OLD.created_at)
 THEN RAISE EXCEPTION 'research run scope is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER research_run_scope_immutable BEFORE UPDATE ON research_runs FOR EACH ROW EXECUTE FUNCTION research_preserve_scope();
