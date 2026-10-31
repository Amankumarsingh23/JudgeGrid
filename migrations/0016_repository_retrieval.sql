-- Requires the pinned PG17-compatible pgvector image. Shared extension objects
-- live in public; every private table remains in the current application schema.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public VERSION '0.8.2';
DO $$ BEGIN
 IF (SELECT extversion FROM pg_extension WHERE extname='vector')<>'0.8.2'
 THEN RAISE EXCEPTION 'JudgeGrid requires pgvector 0.8.2'; END IF;
END $$;
CREATE TABLE retrieval_policies(id text PRIMARY KEY CHECK(id ~ '^[a-f0-9]{64}$'),body jsonb NOT NULL);
CREATE TRIGGER retrieval_policy_immutable BEFORE UPDATE OR DELETE ON retrieval_policies FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE TABLE retrieval_indexes(
 id text PRIMARY KEY CHECK(id ~ '^[a-f0-9]{64}$'),
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 snapshot_id text NOT NULL REFERENCES source_snapshots(id),
 analysis_id text NOT NULL REFERENCES analysis_indexes(id),
 analysis_manifest text NOT NULL CHECK(analysis_manifest ~ '^[a-f0-9]{64}$'),
 policy_id text NOT NULL REFERENCES retrieval_policies(id),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','BUILDING','VERIFYING','READY','FAILED')),
 generation bigint NOT NULL DEFAULT 0,attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 claim_token uuid,lease_expires_at timestamptz,retry_at timestamptz,
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code IN ('','model','budget','integrity','canceled','internal')),
 manifest_sha256 text,manifest_body text,stats jsonb NOT NULL DEFAULT '{}'::jsonb,
 UNIQUE(analysis_id,policy_id),
 CHECK((state IN ('BUILDING','VERIFYING'))=(claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)),
 CHECK((state='READY')=(manifest_sha256 IS NOT NULL AND manifest_body IS NOT NULL))
);
-- Each published index receives its own LIST partition and HNSW graph. Vectors
-- belonging to another owner/commit cannot influence this graph's traversal.
CREATE TABLE retrieval_documents(
 index_id text NOT NULL REFERENCES retrieval_indexes(id),
 chunk_id text NOT NULL,payload jsonb NOT NULL,
 embedding public.vector(384) NOT NULL,
 PRIMARY KEY(index_id,chunk_id),
 CHECK(abs(public.vector_norm(embedding)-1)<0.0001)
) PARTITION BY LIST(index_id);
CREATE TABLE retrieval_embedding_cache(
 repository_id uuid NOT NULL REFERENCES source_repositories(id),
 model_policy text NOT NULL CHECK(model_policy ~ '^[a-f0-9]{64}$'),
 input_sha256 text NOT NULL CHECK(input_sha256 ~ '^[a-f0-9]{64}$'),
 vector_body text NOT NULL CHECK(octet_length(vector_body)<=16384),
 vector_sha256 text NOT NULL CHECK(vector_sha256 ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(repository_id,model_policy,input_sha256)
);
CREATE TRIGGER retrieval_cache_immutable BEFORE UPDATE OR DELETE ON retrieval_embedding_cache FOR EACH ROW EXECUTE FUNCTION source_immutable();
CREATE FUNCTION retrieval_owned(s retrieval_indexes) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT s.state IN ('BUILDING','VERIFYING') AND s.lease_expires_at>clock_timestamp()
 AND current_setting('judgegrid.retrieval_token',true)=s.claim_token::text
 AND current_setting('judgegrid.retrieval_generation',true)=s.generation::text
$$;
CREATE FUNCTION retrieval_document_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s retrieval_indexes;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'immutable retrieval document'; END IF;
 SELECT * INTO s FROM retrieval_indexes WHERE id=NEW.index_id FOR UPDATE;
 IF s.state<>'VERIFYING' OR NOT COALESCE(retrieval_owned(s),false)
 OR NOT EXISTS(SELECT 1 FROM analysis_chunks WHERE index_id=s.analysis_id AND id=NEW.chunk_id)
 THEN RAISE EXCEPTION 'retrieval publication fenced'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retrieval_document_guard BEFORE INSERT OR UPDATE OR DELETE ON retrieval_documents FOR EACH ROW EXECUTE FUNCTION retrieval_document_guard();
CREATE FUNCTION retrieval_cache_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM retrieval_indexes s WHERE s.repository_id=NEW.repository_id
 AND (SELECT body->>'model' FROM retrieval_policies WHERE id=s.policy_id)=NEW.model_policy
 AND COALESCE(retrieval_owned(s),false))
 THEN RAISE EXCEPTION 'retrieval cache fenced'; END IF;
 IF encode(sha256(convert_to(NEW.vector_body,'UTF8')),'hex')<>NEW.vector_sha256
 THEN RAISE EXCEPTION 'invalid retrieval cache'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retrieval_cache_guard BEFORE INSERT ON retrieval_embedding_cache FOR EACH ROW EXECUTE FUNCTION retrieval_cache_guard();
CREATE FUNCTION retrieval_index_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.state='READY' OR ROW(NEW.id,NEW.repository_id,NEW.snapshot_id,NEW.analysis_id,NEW.analysis_manifest,NEW.policy_id)
 IS DISTINCT FROM ROW(OLD.id,OLD.repository_id,OLD.snapshot_id,OLD.analysis_id,OLD.analysis_manifest,OLD.policy_id)
 THEN RAISE EXCEPTION 'immutable retrieval provenance'; END IF;
 IF NEW.state='BUILDING' AND NEW.generation=OLD.generation+1 THEN
  IF OLD.state NOT IN ('PENDING','FAILED') AND NOT(OLD.state IN ('BUILDING','VERIFYING') AND OLD.lease_expires_at<=clock_timestamp())
  OR OLD.attempts>=5 OR NEW.attempts<>OLD.attempts+1 OR NEW.claim_token IS NULL OR NEW.claim_token IS NOT DISTINCT FROM OLD.claim_token
  OR NEW.lease_expires_at<=clock_timestamp() OR (OLD.state='FAILED' AND OLD.retry_at>clock_timestamp())
  THEN RAISE EXCEPTION 'retrieval claim unavailable'; END IF;
  RETURN NEW;
 END IF;
 IF NOT COALESCE(retrieval_owned(OLD),false) OR NEW.generation<>OLD.generation OR NEW.attempts<>OLD.attempts
 THEN RAISE EXCEPTION 'retrieval generation fenced'; END IF;
 IF NEW.state=OLD.state THEN
  IF NEW.claim_token IS DISTINCT FROM OLD.claim_token OR NEW.lease_expires_at<=clock_timestamp() THEN RAISE EXCEPTION 'invalid retrieval renewal'; END IF;
 ELSIF NEW.state='VERIFYING' AND OLD.state='BUILDING' THEN NULL;
 ELSIF NEW.state='FAILED' THEN NULL;
 ELSIF NEW.state='READY' AND OLD.state='VERIFYING' THEN
  IF NOT EXISTS(SELECT 1 FROM analysis_indexes WHERE id=OLD.analysis_id AND snapshot_id=OLD.snapshot_id AND repository_id=OLD.repository_id AND state='READY' AND manifest_sha256=OLD.analysis_manifest)
  OR encode(sha256(convert_to(NEW.manifest_body,'UTF8')),'hex')<>NEW.manifest_sha256
  OR (SELECT count(*) FROM retrieval_documents WHERE index_id=OLD.id)<>jsonb_array_length(NEW.manifest_body::jsonb->'documents')
  OR EXISTS(SELECT id FROM analysis_chunks WHERE index_id=OLD.analysis_id EXCEPT
   (SELECT chunk_id FROM retrieval_documents WHERE index_id=OLD.id UNION ALL SELECT value->>'chunk' FROM jsonb_array_elements(NEW.manifest_body::jsonb->'excluded')))
  THEN RAISE EXCEPTION 'incomplete retrieval publication'; END IF;
 ELSE RAISE EXCEPTION 'invalid retrieval transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retrieval_index_guard BEFORE UPDATE OR DELETE ON retrieval_indexes FOR EACH ROW EXECUTE FUNCTION retrieval_index_guard();
