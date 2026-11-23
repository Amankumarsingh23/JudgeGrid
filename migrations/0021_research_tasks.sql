CREATE TABLE research_tasks(
 id uuid PRIMARY KEY,
 owner text NOT NULL,
 repository uuid NOT NULL REFERENCES source_repositories(id),
 index_id text NOT NULL REFERENCES retrieval_indexes(id),
 snapshot text NOT NULL REFERENCES source_snapshots(id),
 analysis text NOT NULL REFERENCES analysis_indexes(id),
 commit_oid text NOT NULL,
 goal text NOT NULL CHECK(octet_length(goal) BETWEEN 1 AND 256),
 target text NOT NULL CHECK(octet_length(target)<=512),
 policy text NOT NULL,
 provider text NOT NULL,
 research uuid REFERENCES research_runs(id),
 workspace uuid REFERENCES research_workspaces(id),
 baseline text REFERENCES research_validations(id),
 repair uuid REFERENCES research_repairs(id),
 state text NOT NULL DEFAULT 'CREATED' CHECK(state IN ('CREATED','RESEARCHING','PLANNING','PREPARING_WORKSPACE','VALIDATING_BASELINE','REPAIRING','WAITING_APPROVAL','COMPLETED_RESEARCH','COMPLETED_VALIDATED','STOPPED_UNRESOLVED','BUDGET_EXHAUSTED','DENIED','INFRA_BLOCKED','CANCELLED','FAILED_RECOVERY')),
 generation bigint NOT NULL DEFAULT 0 CHECK(generation BETWEEN 0 AND 32),
 claim uuid,
 claim_since timestamptz,
 lease_until timestamptz,
 active_us bigint NOT NULL DEFAULT 0 CHECK(active_us>=0),
 calls int NOT NULL DEFAULT 0 CHECK(calls BETWEEN 0 AND 5),
 input_tokens int NOT NULL DEFAULT 0 CHECK(input_tokens BETWEEN 0 AND 8192),
 output_tokens int NOT NULL DEFAULT 0 CHECK(output_tokens BETWEEN 0 AND 4096),
 evidence_bytes int NOT NULL DEFAULT 0 CHECK(evidence_bytes BETWEEN 0 AND 65536),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 minutes',
 report jsonb,
 report_sha256 text CHECK(report_sha256~'^[a-f0-9]{64}$'),
 CHECK ((report IS NULL)=(report_sha256 IS NULL)),
 CHECK (report IS NULL OR octet_length(report::text)<=65536),
 CHECK ((claim IS NULL)=(lease_until IS NULL) AND (claim IS NULL)=(claim_since IS NULL))
);
CREATE INDEX task_owner_time ON research_tasks(owner,created_at);
CREATE TABLE research_task_events(
 task uuid NOT NULL REFERENCES research_tasks(id),
 sequence bigint GENERATED ALWAYS AS IDENTITY,
 generation bigint NOT NULL,
 action text NOT NULL CHECK(octet_length(action) BETWEEN 1 AND 48),
 receipt text NOT NULL CHECK(octet_length(receipt)<=128),
 at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(task,sequence)
);
CREATE TABLE research_task_reservations(
 task uuid NOT NULL REFERENCES research_tasks(id),
 kind text NOT NULL CHECK(kind IN ('research','validation','repair')),
 maximum int NOT NULL CHECK(maximum BETWEEN 1 AND 12),
 PRIMARY KEY(task,kind)
);
CREATE UNIQUE INDEX task_grant_receipt ON research_task_events(task,action,receipt) WHERE action='grant';
CREATE TRIGGER task_events_immutable BEFORE UPDATE OR DELETE ON research_task_events FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER task_reservations_immutable BEFORE UPDATE OR DELETE ON research_task_reservations FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE FUNCTION research_task_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mutable text[]:=ARRAY['research','workspace','baseline','repair','state','generation','claim','claim_since','lease_until','active_us','calls','input_tokens','output_tokens','evidence_bytes','report','report_sha256'];
BEGIN
 IF (to_jsonb(OLD)-mutable) IS DISTINCT FROM (to_jsonb(NEW)-mutable)
 OR NEW.generation<OLD.generation OR NEW.calls<OLD.calls OR NEW.input_tokens<OLD.input_tokens OR NEW.output_tokens<OLD.output_tokens OR NEW.evidence_bytes<OLD.evidence_bytes OR NEW.active_us<OLD.active_us
 OR (OLD.research IS NOT NULL AND OLD.research IS DISTINCT FROM NEW.research)
 OR (OLD.workspace IS NOT NULL AND OLD.workspace IS DISTINCT FROM NEW.workspace)
 OR (OLD.baseline IS NOT NULL AND OLD.baseline IS DISTINCT FROM NEW.baseline)
 OR (OLD.repair IS NOT NULL AND OLD.repair IS DISTINCT FROM NEW.repair)
 OR (OLD.report IS NOT NULL AND (OLD.report IS DISTINCT FROM NEW.report OR OLD.report_sha256 IS DISTINCT FROM NEW.report_sha256 OR OLD.state IS DISTINCT FROM NEW.state))
 THEN RAISE EXCEPTION 'immutable task scope or receipt'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER task_scope_immutable BEFORE UPDATE ON research_tasks FOR EACH ROW EXECUTE FUNCTION research_task_guard();
