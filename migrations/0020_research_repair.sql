CREATE TABLE research_repairs(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner text NOT NULL,
 workspace uuid NOT NULL REFERENCES research_workspaces(id),
 research_run uuid NOT NULL REFERENCES research_runs(id),
 repository uuid NOT NULL REFERENCES source_repositories(id),
 base_version text NOT NULL,
 baseline text NOT NULL REFERENCES research_validations(id),
 baseline_digest text NOT NULL CHECK(baseline_digest ~ '^[a-f0-9]{64}$'),
 profile text NOT NULL CHECK(profile='go-unit'),
 validation_policy text NOT NULL,
 image text NOT NULL,
 planner text NOT NULL,
 policy text NOT NULL,
 target text NOT NULL CHECK(octet_length(target) BETWEEN 1 AND 512),
 requirement text NOT NULL CHECK(octet_length(requirement) BETWEEN 1 AND 256),
 current_version text NOT NULL,
 current_validation text NOT NULL REFERENCES research_validations(id),
 state text NOT NULL DEFAULT 'BASELINE_VERIFIED' CHECK(state IN ('BASELINE_VERIFIED','DIAGNOSING','WAITING_APPROVAL','APPLYING','VALIDATING','ITERATION_FAILED','COMPLETED_VALIDATED','STOPPED_UNRESOLVED','APPROVAL_DENIED','BUDGET_EXHAUSTED','INFRA_BLOCKED','POLICY_DENIED','CANCELLED','FAILED_RECOVERY')),
 iterations int NOT NULL DEFAULT 0 CHECK(iterations BETWEEN 0 AND 2),
 model_calls int NOT NULL DEFAULT 0 CHECK(model_calls BETWEEN 0 AND 4),
 attempted_bytes int NOT NULL DEFAULT 0 CHECK(attempted_bytes BETWEEN 0 AND 32768),
 generation bigint NOT NULL DEFAULT 0,
 claim uuid,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '25 minutes',
 FOREIGN KEY(workspace,base_version) REFERENCES research_workspace_versions(workspace,id),
 FOREIGN KEY(workspace,current_version) REFERENCES research_workspace_versions(workspace,id),
 CHECK ((claim IS NULL)=(lease_until IS NULL))
);
CREATE INDEX repair_owner_time ON research_repairs(owner,created_at);
CREATE UNIQUE INDEX repair_live_workspace ON research_repairs(workspace)
 WHERE state IN ('BASELINE_VERIFIED','DIAGNOSING','WAITING_APPROVAL','APPLYING','VALIDATING','ITERATION_FAILED');
CREATE TABLE research_repair_iterations(
 repair uuid NOT NULL REFERENCES research_repairs(id),
 ordinal int NOT NULL CHECK(ordinal BETWEEN 1 AND 2),
 workspace uuid NOT NULL,
 patch text NOT NULL,
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 base_version text NOT NULL,
 baseline text NOT NULL REFERENCES research_validations(id),
 baseline_digest text NOT NULL,
 source_digest text NOT NULL,
 proposal_ns bigint NOT NULL CHECK(proposal_ns>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 new_version text,
 validation text REFERENCES research_validations(id),
 report_sha256 text,
 outcome text,
 PRIMARY KEY(repair,ordinal),
 UNIQUE(repair,fingerprint),
 FOREIGN KEY(workspace,patch) REFERENCES research_edit_proposals(workspace,digest),
 FOREIGN KEY(workspace,base_version) REFERENCES research_workspace_versions(workspace,id),
 FOREIGN KEY(workspace,new_version) REFERENCES research_workspace_versions(workspace,id)
);
CREATE TABLE research_repair_events(
 repair uuid NOT NULL REFERENCES research_repairs(id),
 sequence bigint GENERATED ALWAYS AS IDENTITY,
 generation bigint NOT NULL,
 action text NOT NULL,
 ordinal int NOT NULL,
 evidence text NOT NULL CHECK(octet_length(evidence)<=128),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(repair,sequence)
);
CREATE TRIGGER repair_events_immutable BEFORE UPDATE OR DELETE ON research_repair_events FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE UNIQUE INDEX repair_logical_approval ON research_repair_events(repair,ordinal,action) WHERE action IN ('grant','deny');

CREATE FUNCTION research_repair_iteration_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE pending text[]:=ARRAY['new_version','validation','report_sha256','outcome'];
BEGIN
 IF (to_jsonb(OLD)-pending) IS DISTINCT FROM (to_jsonb(NEW)-pending) OR
 (OLD.new_version IS NOT NULL AND NEW.new_version IS DISTINCT FROM OLD.new_version) OR
 (OLD.validation IS NOT NULL AND NEW.validation IS DISTINCT FROM OLD.validation) OR
 OLD.report_sha256 IS NOT NULL
 THEN RAISE EXCEPTION 'immutable repair iteration receipt'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER repair_iteration_immutable BEFORE UPDATE ON research_repair_iterations FOR EACH ROW EXECUTE FUNCTION research_repair_iteration_guard();

CREATE FUNCTION research_repair_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE dynamic text[]:=ARRAY['current_version','current_validation','state','iterations','model_calls','attempted_bytes','generation','claim','lease_until'];
BEGIN
 IF (to_jsonb(OLD)-dynamic) IS DISTINCT FROM (to_jsonb(NEW)-dynamic) OR
 NEW.iterations<OLD.iterations OR NEW.model_calls<OLD.model_calls OR NEW.attempted_bytes<OLD.attempted_bytes OR NEW.generation<OLD.generation
 THEN RAISE EXCEPTION 'repair identity or budget regression'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER repair_identity_immutable BEFORE UPDATE ON research_repairs FOR EACH ROW EXECUTE FUNCTION research_repair_identity_guard();
