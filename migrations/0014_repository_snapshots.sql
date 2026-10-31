-- Repository intelligence is independent of judge jobs and execution authority.
CREATE TABLE source_repositories (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner text NOT NULL CHECK(length(owner) BETWEEN 1 AND 200),
 name text NOT NULL CHECK(name ~ '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$'),
 location text NOT NULL CHECK(length(location) BETWEEN 1 AND 4096),
 origin text NOT NULL DEFAULT '' CHECK(length(origin)<=1024),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner,name)
);
CREATE TABLE ingestion_policies (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE source_snapshots (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'),
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 policy_id text NOT NULL REFERENCES ingestion_policies(id),
 object_format text NOT NULL CHECK(object_format IN ('sha1','sha256')),
 commit_oid text NOT NULL,
 tree_oid text NOT NULL,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','INGESTING','VERIFYING','READY','FAILED')),
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 claim_token uuid,
 lease_expires_at timestamptz,
 retry_at timestamptz,
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code IN ('','source','budget','storage','integrity','canceled','internal')),
 manifest_sha256 text CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),
 stats jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(stats)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(repository_id,object_format,commit_oid,policy_id),
 CHECK(commit_oid ~ '^[0-9a-f]+$' AND length(commit_oid)=CASE object_format WHEN 'sha1' THEN 40 ELSE 64 END),
 CHECK(tree_oid ~ '^[0-9a-f]+$' AND length(tree_oid)=length(commit_oid)),
 CHECK((state IN ('INGESTING','VERIFYING'))=(claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)),
 CHECK((state='READY')=(manifest_sha256 IS NOT NULL))
);
CREATE TABLE source_blobs (
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 policy_id text NOT NULL REFERENCES ingestion_policies(id),
 object_format text NOT NULL CHECK(object_format IN ('sha1','sha256')),
 git_oid text NOT NULL,
 raw_sha256 text NOT NULL CHECK(raw_sha256 ~ '^[0-9a-f]{64}$'),
 byte_size bigint NOT NULL CHECK(byte_size BETWEEN 0 AND 2097152),
 verified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(repository_id,policy_id,object_format,git_oid),
 CHECK(git_oid ~ '^[0-9a-f]+$' AND length(git_oid)=CASE object_format WHEN 'sha1' THEN 40 ELSE 64 END)
);
CREATE TABLE source_files (
 snapshot_id text NOT NULL REFERENCES source_snapshots(id),
 path text NOT NULL CHECK(octet_length(path) BETWEEN 1 AND 512),
 file_id text NOT NULL UNIQUE CHECK(file_id ~ '^[0-9a-f]{64}$'),
 git_mode text NOT NULL CHECK(git_mode IN ('40000','100644','100755','120000','160000')),
 git_type text NOT NULL CHECK(git_type IN ('tree','blob','commit')),
 git_oid text NOT NULL CHECK(git_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
 byte_size bigint NOT NULL CHECK(byte_size>=0),
 raw_sha256 text NOT NULL DEFAULT '' CHECK(raw_sha256='' OR raw_sha256 ~ '^[0-9a-f]{64}$'),
 language text NOT NULL CHECK(language IN ('','go','python','java','c','cpp','javascript','typescript','json','yaml','toml','sql','protobuf','shell','markdown','text','gomod','dockerfile')),
 reason text NOT NULL CHECK(reason IN ('included','directory','symlink','gitlink','sensitive_path','dependency_build','generated_minified','unsupported_type','docs_disabled','oversize','binary','encoding','lfs_pointer','secret_evidence')),
 PRIMARY KEY(snapshot_id,path),
 CHECK(reason<>'included' OR (git_type='blob' AND git_mode IN ('100644','100755') AND raw_sha256<>''))
);
CREATE FUNCTION source_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable repository provenance' USING ERRCODE='P0001'; END $$;
CREATE TRIGGER source_repository_immutable BEFORE UPDATE OR DELETE ON source_repositories FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER ingestion_policy_immutable BEFORE UPDATE OR DELETE ON ingestion_policies FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TRIGGER source_blob_immutable BEFORE UPDATE OR DELETE ON source_blobs FOR EACH ROW EXECUTE FUNCTION source_immutable();

CREATE FUNCTION source_file_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s source_snapshots;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'immutable source inventory' USING ERRCODE='P0001'; END IF;
 SELECT * INTO s FROM source_snapshots WHERE id=NEW.snapshot_id FOR UPDATE;
 IF s.state<>'VERIFYING' OR s.lease_expires_at<=clock_timestamp()
 OR current_setting('judgegrid.ingest_token',true) IS DISTINCT FROM s.claim_token::text
 OR current_setting('judgegrid.ingest_generation',true) IS DISTINCT FROM s.generation::text
 THEN RAISE EXCEPTION 'source publication fenced' USING ERRCODE='P0001'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER source_files_guard BEFORE INSERT OR UPDATE OR DELETE ON source_files FOR EACH ROW EXECUTE FUNCTION source_file_guard();

CREATE FUNCTION source_snapshot_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'source snapshot deletion forbidden' USING ERRCODE='P0001'; END IF;
 IF OLD.state='READY' OR ROW(NEW.id,NEW.repository_id,NEW.policy_id,NEW.object_format,NEW.commit_oid,NEW.tree_oid,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.repository_id,OLD.policy_id,OLD.object_format,OLD.commit_oid,OLD.tree_oid,OLD.created_at)
 THEN RAISE EXCEPTION 'immutable snapshot provenance' USING ERRCODE='P0001'; END IF;
 IF NEW.state='INGESTING' AND NEW.generation=OLD.generation+1 THEN
  IF OLD.state NOT IN ('PENDING','FAILED') AND NOT(OLD.state IN ('INGESTING','VERIFYING') AND OLD.lease_expires_at<=clock_timestamp())
  OR OLD.attempts>=5 OR NEW.attempts<>OLD.attempts+1 OR NEW.claim_token IS NULL OR NEW.claim_token IS NOT DISTINCT FROM OLD.claim_token
  OR NEW.lease_expires_at<=clock_timestamp() OR (OLD.state='FAILED' AND OLD.retry_at>clock_timestamp())
  THEN RAISE EXCEPTION 'ingestion claim unavailable' USING ERRCODE='P0001'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.state NOT IN ('INGESTING','VERIFYING') OR OLD.lease_expires_at<=clock_timestamp()
 OR NEW.generation<>OLD.generation OR NEW.attempts<>OLD.attempts
 OR current_setting('judgegrid.ingest_token',true) IS DISTINCT FROM OLD.claim_token::text
 OR current_setting('judgegrid.ingest_generation',true) IS DISTINCT FROM OLD.generation::text
 THEN RAISE EXCEPTION 'source generation fenced' USING ERRCODE='P0001'; END IF;
 IF NEW.state=OLD.state THEN
  IF NEW.claim_token IS DISTINCT FROM OLD.claim_token OR NEW.lease_expires_at<=clock_timestamp()
  THEN RAISE EXCEPTION 'invalid renewal' USING ERRCODE='P0001'; END IF;
 ELSIF NEW.state='VERIFYING' AND OLD.state='INGESTING' THEN NULL;
 ELSIF NEW.state='FAILED' THEN NULL;
 ELSIF NEW.state='READY' AND OLD.state='VERIFYING' THEN
  IF NEW.manifest_sha256 IS NULL
  OR NOT (NEW.stats ?& ARRAY['discovered','included','excluded','included_bytes'])
  OR (NEW.stats->>'discovered')::int<0 OR (NEW.stats->>'included')::int<0 OR (NEW.stats->>'excluded')::int<0
  OR (SELECT count(*) FROM source_files WHERE snapshot_id=OLD.id) <> (NEW.stats->>'discovered')::int
  OR (SELECT count(*) FROM source_files WHERE snapshot_id=OLD.id AND reason='included') <> (NEW.stats->>'included')::int
  OR (SELECT count(*) FROM source_files WHERE snapshot_id=OLD.id AND reason<>'included') <> (NEW.stats->>'excluded')::int
  OR (SELECT COALESCE(sum(byte_size),0) FROM source_files WHERE snapshot_id=OLD.id AND reason='included') <> (NEW.stats->>'included_bytes')::bigint
  OR EXISTS(SELECT 1 FROM source_files f WHERE f.snapshot_id=OLD.id AND f.reason='included' AND NOT EXISTS(
   SELECT 1 FROM source_blobs b WHERE b.repository_id=OLD.repository_id AND b.policy_id=OLD.policy_id AND b.object_format=OLD.object_format
    AND b.git_oid=f.git_oid AND b.raw_sha256=f.raw_sha256 AND b.byte_size=f.byte_size))
  THEN RAISE EXCEPTION 'incomplete source manifest' USING ERRCODE='P0001'; END IF;
 ELSE RAISE EXCEPTION 'invalid snapshot transition' USING ERRCODE='P0001';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER source_snapshots_guard BEFORE UPDATE OR DELETE ON source_snapshots FOR EACH ROW EXECUTE FUNCTION source_snapshot_guard();
