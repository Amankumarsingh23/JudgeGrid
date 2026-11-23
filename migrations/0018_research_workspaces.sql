CREATE TABLE research_workspaces(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL REFERENCES research_runs(id),
 owner text NOT NULL,
 repository uuid NOT NULL REFERENCES source_repositories(id),
 snapshot text NOT NULL REFERENCES source_snapshots(id),
 commit_oid text NOT NULL,
 object_format text NOT NULL CHECK(object_format IN ('sha1','sha256')),
 policy text NOT NULL,
 selected text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 generation bigint NOT NULL DEFAULT 0,
 claim uuid,
 lease_until timestamptz,
 CHECK ((claim IS NULL)=(lease_until IS NULL)),
 UNIQUE(run_id)
);
CREATE INDEX research_workspaces_owner_time ON research_workspaces(owner, created_at);
CREATE TABLE research_workspace_objects(
 workspace uuid NOT NULL REFERENCES research_workspaces(id),
 hash text NOT NULL CHECK(hash ~ '^[a-f0-9]{64}$'),
 body bytea NOT NULL CHECK(octet_length(body)<=4194304),
 PRIMARY KEY(workspace,hash)
);
CREATE TABLE research_workspace_versions(
 workspace uuid NOT NULL REFERENCES research_workspaces(id),
 id text NOT NULL CHECK(id ~ '^[a-f0-9]{64}$'),
 inventory jsonb NOT NULL CHECK(octet_length(inventory::text)<=524288),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace,id)
);
ALTER TABLE research_workspaces ADD FOREIGN KEY(id,selected) REFERENCES research_workspace_versions(workspace,id);
CREATE TABLE research_edit_proposals(
 workspace uuid NOT NULL REFERENCES research_workspaces(id),
 digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),
 base text NOT NULL,
 patch jsonb NOT NULL CHECK(octet_length(patch::text)<=524288),
 preview text NOT NULL CHECK(octet_length(preview)<=393216),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace,digest),
 FOREIGN KEY(workspace,base) REFERENCES research_workspace_versions(workspace,id)
);
CREATE TABLE research_edit_approvals(
 workspace uuid NOT NULL,
 digest text NOT NULL,
 requested_by text NOT NULL,
 decided_by text,
 expires_at timestamptz NOT NULL,
 decided_at timestamptz,
 state text NOT NULL CHECK(state IN ('PENDING','GRANTED','DENIED','CANCELLED','EXPIRED')),
 PRIMARY KEY(workspace,digest),
 FOREIGN KEY(workspace,digest) REFERENCES research_edit_proposals(workspace,digest),
 CHECK ((state='PENDING')=(decided_at IS NULL)),
 CHECK ((state IN ('GRANTED','DENIED'))=(decided_by IS NOT NULL))
);
CREATE TABLE research_edit_attempts(
 workspace uuid NOT NULL,
 digest text NOT NULL,
 generation bigint NOT NULL,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace,generation),
 FOREIGN KEY(workspace,digest) REFERENCES research_edit_proposals(workspace,digest)
);
CREATE TABLE research_edit_journal(
 workspace uuid NOT NULL REFERENCES research_workspaces(id),
 sequence bigint GENERATED ALWAYS AS IDENTITY,
 action text NOT NULL CHECK(action IN ('prepare_workspace','propose_patch','inspect_patch','inspect_workspace_version','request_patch_approval','grant','deny','cancel','apply_approved_patch')),
 digest text NOT NULL,
 generation bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace,sequence),
 UNIQUE(workspace,action,digest)
);
CREATE TABLE research_edit_publications(
 workspace uuid NOT NULL,
 digest text NOT NULL,
 version text NOT NULL,
 generation bigint NOT NULL,
 published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace,digest),
 FOREIGN KEY(workspace,digest) REFERENCES research_edit_proposals(workspace,digest),
 FOREIGN KEY(workspace,version) REFERENCES research_workspace_versions(workspace,id)
);
CREATE TRIGGER workspace_objects_immutable BEFORE UPDATE OR DELETE ON research_workspace_objects FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER workspace_versions_immutable BEFORE UPDATE OR DELETE ON research_workspace_versions FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER edit_proposals_immutable BEFORE UPDATE OR DELETE ON research_edit_proposals FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER edit_journal_immutable BEFORE UPDATE OR DELETE ON research_edit_journal FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER edit_attempts_immutable BEFORE UPDATE OR DELETE ON research_edit_attempts FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER edit_publications_immutable BEFORE UPDATE OR DELETE ON research_edit_publications FOR EACH ROW EXECUTE FUNCTION source_immutable();

CREATE FUNCTION research_workspace_scope_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD)-ARRAY['selected','generation','claim','lease_until']) IS DISTINCT FROM
    (to_jsonb(NEW)-ARRAY['selected','generation','claim','lease_until']) OR NEW.generation<OLD.generation
 THEN RAISE EXCEPTION 'immutable workspace scope'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER workspace_scope_immutable BEFORE UPDATE ON research_workspaces FOR EACH ROW EXECUTE FUNCTION research_workspace_scope_guard();
