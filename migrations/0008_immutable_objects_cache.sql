CREATE TABLE bundle_publications (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 operator TEXT NOT NULL CHECK(length(operator) BETWEEN 1 AND 200),
 idempotency_key TEXT NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 problem_id BIGINT NOT NULL REFERENCES problems(id),
 bundle_sha TEXT NOT NULL CHECK(bundle_sha ~ '^[0-9a-f]{64}$'),
 metadata JSONB NOT NULL,
 state TEXT NOT NULL DEFAULT 'STAGED' CHECK(state IN ('STAGED','ACTIVE')),
 version_id BIGINT REFERENCES problem_versions(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(operator,idempotency_key),
 CHECK((state='ACTIVE')=(version_id IS NOT NULL))
);
CREATE FUNCTION guard_bundle_publication() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'publication retention disabled'; END IF;
 IF OLD.state<>'STAGED' OR NEW.state<>'ACTIVE' OR
 ROW(OLD.id,OLD.operator,OLD.idempotency_key,OLD.problem_id,OLD.bundle_sha,OLD.metadata,OLD.created_at)
 IS DISTINCT FROM ROW(NEW.id,NEW.operator,NEW.idempotency_key,NEW.problem_id,NEW.bundle_sha,NEW.metadata,NEW.created_at) THEN RAISE EXCEPTION 'immutable publication identity'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER sealed_publication BEFORE UPDATE OR DELETE ON bundle_publications FOR EACH ROW EXECUTE FUNCTION guard_bundle_publication();

CREATE TABLE compilation_cache (
 cache_key TEXT PRIMARY KEY CHECK(cache_key ~ '^[0-9a-f]{64}$'),key_json JSONB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('BUILDING','READY','EVICTED')),
 generation INTEGER NOT NULL CHECK(generation>0),owner TEXT NOT NULL,
 lease_expires_at TIMESTAMPTZ NOT NULL,artifact_sha TEXT CHECK(artifact_sha ~ '^[0-9a-f]{64}$'),
 artifact_bytes BIGINT CHECK(artifact_bytes BETWEEN 64 AND 16777216),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),ready_at TIMESTAMPTZ,
 CHECK(state<>'READY' OR(artifact_sha IS NOT NULL AND artifact_bytes IS NOT NULL AND ready_at IS NOT NULL)),
 CHECK(state<>'BUILDING' OR(artifact_sha IS NULL AND artifact_bytes IS NULL AND ready_at IS NULL))
);
CREATE FUNCTION guard_compilation_cache() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'cache history retention disabled'; END IF;
 IF NEW.cache_key<>OLD.cache_key OR NEW.key_json<>OLD.key_json OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'immutable cache provenance'; END IF;
 IF NEW.state='BUILDING' THEN
  IF NEW.generation<>OLD.generation+1 OR NOT(OLD.state='EVICTED' OR(OLD.state='BUILDING' AND OLD.lease_expires_at<=clock_timestamp())) THEN RAISE EXCEPTION 'invalid cache takeover'; END IF;
 ELSIF NEW.state='READY' THEN
  IF OLD.state<>'BUILDING' OR NEW.owner<>OLD.owner OR NEW.generation<>OLD.generation OR OLD.lease_expires_at<=clock_timestamp() THEN RAISE EXCEPTION 'stale cache publication'; END IF;
 ELSIF NEW.state='EVICTED' THEN
  IF OLD.state NOT IN ('BUILDING','READY') OR NEW.generation<>OLD.generation OR NEW.owner<>OLD.owner THEN RAISE EXCEPTION 'invalid cache eviction'; END IF;
 ELSE RAISE EXCEPTION 'invalid cache state'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER guarded_compilation_cache BEFORE UPDATE OR DELETE ON compilation_cache FOR EACH ROW EXECUTE FUNCTION guard_compilation_cache();
