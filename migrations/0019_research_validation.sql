CREATE TABLE research_validations (
 id text PRIMARY KEY CHECK(id ~ '^[a-f0-9]{64}$'),
 owner text NOT NULL,
 workspace uuid NOT NULL,
 version text NOT NULL,
 profile text NOT NULL,
 policy text NOT NULL,
 image text NOT NULL,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','MATERIALIZING','RUNNING','COMPLETED','FAILED','CANCELLED','EXPIRED')),
 generation bigint NOT NULL DEFAULT 0,
 claim uuid,
 lease_until timestamptz,
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 minutes',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 result jsonb CHECK(octet_length(result::text)<=524288),
 result_sha256 text CHECK(result_sha256 ~ '^[a-f0-9]{64}$'),
 FOREIGN KEY(workspace,version) REFERENCES research_workspace_versions(workspace,id),
 CHECK ((claim IS NULL)=(lease_until IS NULL)),
 CHECK ((state IN ('MATERIALIZING','RUNNING'))=(claim IS NOT NULL)),
 CHECK ((result IS NULL)=(result_sha256 IS NULL)),
 CHECK ((state='COMPLETED')=(result IS NOT NULL))
);
CREATE INDEX validation_owner_time ON research_validations(owner,created_at);
CREATE TABLE research_validation_attempts (
 validation text NOT NULL REFERENCES research_validations(id),
 generation bigint NOT NULL CHECK(generation BETWEEN 1 AND 3),
 sandbox text NOT NULL UNIQUE CHECK(sandbox ~ '^jg-v19-[a-f0-9]{32}$'),
 cleanup_required boolean NOT NULL DEFAULT true,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(validation,generation)
);
CREATE FUNCTION research_validation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state='COMPLETED' OR
 (to_jsonb(OLD)-ARRAY['state','generation','claim','lease_until','result','result_sha256']) IS DISTINCT FROM
 (to_jsonb(NEW)-ARRAY['state','generation','claim','lease_until','result','result_sha256']) OR NEW.generation<OLD.generation
 THEN RAISE EXCEPTION 'immutable validation identity or report'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER validation_identity_immutable BEFORE UPDATE ON research_validations FOR EACH ROW EXECUTE FUNCTION research_validation_immutable();
