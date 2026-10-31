-- Session 13 has no judge execution authority and never stores copied source bytes.
CREATE TABLE analysis_policies (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'),
 parser_identity text NOT NULL CHECK(parser_identity ~ '^[0-9a-f]{64}$'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object')
);
CREATE TRIGGER analysis_policy_immutable BEFORE UPDATE OR DELETE ON analysis_policies FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TABLE analysis_indexes (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'),
 snapshot_id text NOT NULL REFERENCES source_snapshots(id),
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 policy_id text NOT NULL REFERENCES analysis_policies(id),
 source_manifest text NOT NULL CHECK(source_manifest ~ '^[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','ANALYZING','VERIFYING','READY','FAILED')),
 generation bigint NOT NULL DEFAULT 0,
 attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 claim_token uuid,
 lease_expires_at timestamptz,
 retry_at timestamptz,
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code IN ('','parser','budget','integrity','canceled','internal')),
 manifest_sha256 text,
 manifest_body text,
 stats jsonb NOT NULL DEFAULT '{}'::jsonb,
 UNIQUE(snapshot_id,policy_id),
 CHECK((state IN ('ANALYZING','VERIFYING'))=(claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)),
 CHECK((state='READY')=(manifest_sha256 IS NOT NULL AND manifest_body IS NOT NULL))
);
CREATE TABLE analysis_file_attempts (
 index_id text NOT NULL REFERENCES analysis_indexes(id), generation bigint NOT NULL,
 source_file_id text NOT NULL REFERENCES source_files(file_id),
 state text NOT NULL CHECK(state IN ('PARSING','PARSED','PARTIAL','UNSUPPORTED','EXCLUDED','FAILED')),
 failure_code text NOT NULL DEFAULT '',
 PRIMARY KEY(index_id,generation,source_file_id)
);
CREATE TABLE analysis_cache (
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 policy_id text NOT NULL REFERENCES analysis_policies(id),
 identity text NOT NULL CHECK(identity ~ '^[0-9a-f]{64}$'),
 sha256 text NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
 body text NOT NULL CHECK(octet_length(body)<=8388608),
 PRIMARY KEY(repository_id,policy_id,identity)
);
CREATE TRIGGER analysis_cache_immutable BEFORE UPDATE OR DELETE ON analysis_cache FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TABLE analysis_files (
 index_id text NOT NULL REFERENCES analysis_indexes(id),
 id text NOT NULL, source_file_id text NOT NULL REFERENCES source_files(file_id),
 payload jsonb NOT NULL, PRIMARY KEY(index_id,id), UNIQUE(index_id,source_file_id)
);
CREATE TABLE analysis_symbols (
 index_id text NOT NULL, id text NOT NULL, file_id text NOT NULL,
 payload jsonb NOT NULL, PRIMARY KEY(index_id,id), FOREIGN KEY(index_id,file_id) REFERENCES analysis_files(index_id,id)
);
CREATE TABLE analysis_imports (LIKE analysis_symbols INCLUDING ALL);
ALTER TABLE analysis_imports ADD FOREIGN KEY(index_id,file_id) REFERENCES analysis_files(index_id,id);
CREATE TABLE analysis_calls (LIKE analysis_symbols INCLUDING ALL);
ALTER TABLE analysis_calls ADD FOREIGN KEY(index_id,file_id) REFERENCES analysis_files(index_id,id);
CREATE TABLE analysis_edges (LIKE analysis_symbols INCLUDING ALL);
ALTER TABLE analysis_edges ADD FOREIGN KEY(index_id,file_id) REFERENCES analysis_files(index_id,id);
CREATE TABLE analysis_chunks (LIKE analysis_symbols INCLUDING ALL);
ALTER TABLE analysis_chunks ADD FOREIGN KEY(index_id,file_id) REFERENCES analysis_files(index_id,id);

CREATE FUNCTION analysis_owned(s analysis_indexes) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT s.state IN ('ANALYZING','VERIFYING') AND s.lease_expires_at>clock_timestamp()
 AND current_setting('judgegrid.analysis_token',true)=s.claim_token::text
 AND current_setting('judgegrid.analysis_generation',true)=s.generation::text
$$;
CREATE FUNCTION analysis_row_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s analysis_indexes;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'immutable analysis row'; END IF;
 SELECT * INTO s FROM analysis_indexes WHERE id=NEW.index_id FOR UPDATE;
 IF s.state<>'VERIFYING' OR NOT COALESCE(analysis_owned(s),false) THEN RAISE EXCEPTION 'analysis publication fenced'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER analysis_files_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_files FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE TRIGGER analysis_symbols_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_symbols FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE TRIGGER analysis_imports_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_imports FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE TRIGGER analysis_calls_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_calls FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE TRIGGER analysis_edges_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_edges FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE TRIGGER analysis_chunks_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_chunks FOR EACH ROW EXECUTE FUNCTION analysis_row_guard();
CREATE FUNCTION analysis_attempt_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s analysis_indexes;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'analysis attempt deletion forbidden'; END IF;
 SELECT * INTO s FROM analysis_indexes WHERE id=NEW.index_id FOR UPDATE;
 IF NOT COALESCE(analysis_owned(s),false) OR s.generation<>NEW.generation
 OR NOT EXISTS(SELECT 1 FROM source_files WHERE file_id=NEW.source_file_id AND snapshot_id=s.snapshot_id)
 THEN RAISE EXCEPTION 'analysis attempt fenced'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER analysis_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON analysis_file_attempts FOR EACH ROW EXECUTE FUNCTION analysis_attempt_guard();
CREATE FUNCTION analysis_index_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.state='READY' OR ROW(NEW.id,NEW.snapshot_id,NEW.repository_id,NEW.policy_id,NEW.source_manifest)
 IS DISTINCT FROM ROW(OLD.id,OLD.snapshot_id,OLD.repository_id,OLD.policy_id,OLD.source_manifest)
 THEN RAISE EXCEPTION 'immutable analysis provenance'; END IF;
 IF NEW.state='ANALYZING' AND NEW.generation=OLD.generation+1 THEN
  IF OLD.state NOT IN ('PENDING','FAILED') AND NOT(OLD.state IN ('ANALYZING','VERIFYING') AND OLD.lease_expires_at<=clock_timestamp())
  OR OLD.attempts>=5 OR NEW.attempts<>OLD.attempts+1 OR NEW.claim_token IS NULL OR NEW.claim_token IS NOT DISTINCT FROM OLD.claim_token
  OR NEW.lease_expires_at<=clock_timestamp() OR (OLD.state='FAILED' AND OLD.retry_at>clock_timestamp())
  THEN RAISE EXCEPTION 'analysis claim unavailable'; END IF;
  RETURN NEW;
 END IF;
 IF NOT COALESCE(analysis_owned(OLD),false) OR NEW.generation<>OLD.generation OR NEW.attempts<>OLD.attempts
 THEN RAISE EXCEPTION 'analysis generation fenced'; END IF;
 IF NEW.state=OLD.state THEN
  IF NEW.claim_token IS DISTINCT FROM OLD.claim_token OR NEW.lease_expires_at<=clock_timestamp() THEN RAISE EXCEPTION 'invalid analysis renewal'; END IF;
 ELSIF NEW.state='VERIFYING' AND OLD.state='ANALYZING' THEN NULL;
 ELSIF NEW.state='FAILED' THEN NULL;
 ELSIF NEW.state='READY' AND OLD.state='VERIFYING' THEN
  IF NOT EXISTS(SELECT 1 FROM source_snapshots WHERE id=OLD.snapshot_id AND repository_id=OLD.repository_id AND state='READY' AND manifest_sha256=OLD.source_manifest)
  OR EXISTS(SELECT file_id FROM source_files WHERE snapshot_id=OLD.snapshot_id EXCEPT SELECT source_file_id FROM analysis_files WHERE index_id=OLD.id)
  OR EXISTS(SELECT source_file_id FROM analysis_files WHERE index_id=OLD.id EXCEPT SELECT file_id FROM source_files WHERE snapshot_id=OLD.snapshot_id)
  OR encode(sha256(convert_to(NEW.manifest_body,'UTF8')),'hex')<>NEW.manifest_sha256
  THEN RAISE EXCEPTION 'incomplete analysis publication'; END IF;
 ELSE RAISE EXCEPTION 'invalid analysis transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER analysis_index_guard BEFORE UPDATE OR DELETE ON analysis_indexes FOR EACH ROW EXECUTE FUNCTION analysis_index_guard();
