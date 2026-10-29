CREATE TABLE scheduler_policy (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),enabled BOOLEAN NOT NULL DEFAULT false,
 global_active INTEGER NOT NULL DEFAULT 4 CHECK(global_active BETWEEN 1 AND 16),
 principal_active INTEGER NOT NULL DEFAULT 1 CHECK(principal_active BETWEEN 1 AND 16),
 pending_limit INTEGER NOT NULL DEFAULT 128 CHECK(pending_limit BETWEEN 1 AND 10000),
 rate_burst INTEGER NOT NULL DEFAULT 120 CHECK(rate_burst BETWEEN 1 AND 1000),
 rate_per_second DOUBLE PRECISION NOT NULL DEFAULT 2 CHECK(rate_per_second BETWEEN 0.01 AND 100),
 sse_global INTEGER NOT NULL DEFAULT 32 CHECK(sse_global BETWEEN 1 AND 128),
 sse_principal INTEGER NOT NULL DEFAULT 2 CHECK(sse_principal BETWEEN 1 AND 8),
 turn BIGINT NOT NULL DEFAULT 0 CHECK(turn>=0),epoch BIGINT NOT NULL DEFAULT 1,
 CHECK(principal_active<=global_active AND(global_active=1 OR principal_active<global_active))
);
INSERT INTO scheduler_policy(singleton) VALUES(true);
CREATE TABLE scheduler_principals(principal TEXT PRIMARY KEY CHECK(length(principal) BETWEEN 1 AND 200),enabled BOOLEAN NOT NULL DEFAULT false,last_turn BIGINT NOT NULL DEFAULT 0);
CREATE INDEX scheduler_principal_turn ON scheduler_principals(last_turn,principal) WHERE enabled;
CREATE TABLE scheduler_jobs (
 scope_id UUID PRIMARY KEY,submission_id UUID NOT NULL REFERENCES submissions(id),job_id UUID REFERENCES evaluation_jobs(id),
 principal TEXT NOT NULL REFERENCES scheduler_principals(principal),created_at TIMESTAMPTZ NOT NULL,done BOOLEAN NOT NULL DEFAULT false,
 CHECK(scope_id=COALESCE(job_id,submission_id)),UNIQUE(job_id)
);
CREATE INDEX scheduler_pending ON scheduler_jobs(principal,created_at,scope_id) WHERE NOT done;
CREATE INDEX initial_scheduler_active ON execution_ownership(lease_expires_at,submission_id) WHERE state='ACTIVE';
CREATE INDEX rejudge_scheduler_active ON evaluation_job_ownership(lease_expires_at,submission_id) WHERE state='ACTIVE';
CREATE FUNCTION scheduler_lock() RETURNS void LANGUAGE sql AS $$ SELECT pg_advisory_xact_lock(2349012510); $$;
CREATE FUNCTION scheduler_pending_admission(p TEXT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE cfg scheduler_policy; n INTEGER;
BEGIN
 PERFORM scheduler_lock();SELECT * INTO cfg FROM scheduler_policy;
 IF NOT cfg.enabled THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM scheduler_principals WHERE principal=p AND enabled) THEN RAISE EXCEPTION 'scheduler scope unavailable' USING ERRCODE='PZ503'; END IF;
 SELECT count(*) INTO n FROM(SELECT scope_id FROM scheduler_jobs WHERE principal=p AND NOT done LIMIT cfg.pending_limit) q;
 IF n>=cfg.pending_limit THEN RAISE EXCEPTION 'principal pending budget reached' USING ERRCODE='PZ429'; END IF;
END;$$;
CREATE FUNCTION maintain_scheduler_job() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p TEXT; sid UUID; jid UUID; scope UUID; stamp TIMESTAMPTZ;
BEGIN
 IF TG_TABLE_NAME='submissions' THEN
  sid:=NEW.id;jid:=NULL;scope:=sid;p:=NEW.principal_id;stamp:=NEW.created_at;
  IF TG_OP='UPDATE' THEN UPDATE scheduler_jobs SET done=NEW.status IN ('COMPLETED','FAILED') WHERE scope_id=scope;RETURN NEW; END IF;
 ELSE
  sid:=NEW.submission_id;jid:=NEW.id;scope:=jid;stamp:=NEW.created_at;SELECT principal_id INTO p FROM submissions WHERE id=sid;
  IF TG_OP='UPDATE' THEN UPDATE scheduler_jobs SET done=NEW.state IN ('COMPLETED','FAILED') WHERE scope_id=scope;RETURN NEW; END IF;
 END IF;
 PERFORM scheduler_pending_admission(p);
 INSERT INTO scheduler_principals(principal) VALUES(p) ON CONFLICT DO NOTHING;
 INSERT INTO scheduler_jobs(scope_id,submission_id,job_id,principal,created_at) VALUES(scope,sid,jid,p,stamp);
 RETURN NEW;
END;$$;
CREATE TRIGGER submission_scheduler AFTER INSERT OR UPDATE OF status ON submissions FOR EACH ROW EXECUTE FUNCTION maintain_scheduler_job();
CREATE TRIGGER rejudge_scheduler AFTER INSERT OR UPDATE OF state ON evaluation_jobs FOR EACH ROW EXECUTE FUNCTION maintain_scheduler_job();
INSERT INTO scheduler_principals(principal) SELECT DISTINCT principal_id FROM submissions ON CONFLICT DO NOTHING;
INSERT INTO scheduler_jobs SELECT id,id,NULL,principal_id,created_at,status IN ('COMPLETED','FAILED') FROM submissions;
INSERT INTO scheduler_jobs SELECT j.id,j.submission_id,j.id,s.principal_id,j.created_at,j.state IN ('COMPLETED','FAILED') FROM evaluation_jobs j JOIN submissions s ON s.id=j.submission_id;

-- One oldest non-active head per explicitly enrolled principal. At most 16 valid
-- active leases can precede a pending head; inspect at most 17 rows per principal.
CREATE FUNCTION scheduler_next() RETURNS TABLE(scope_id UUID,submission_id UUID,job_id UUID,principal TEXT,event_id UUID) LANGUAGE sql STABLE AS $$
 SELECT h.scope_id,h.submission_id,h.job_id,p.principal,e.id
 FROM (SELECT principal,last_turn FROM scheduler_principals WHERE enabled ORDER BY last_turn,principal LIMIT 128) p
 CROSS JOIN LATERAL (
  SELECT c.* FROM (SELECT q.* FROM scheduler_jobs q WHERE q.principal=p.principal AND NOT q.done ORDER BY q.created_at,q.scope_id LIMIT 17) c
  WHERE NOT EXISTS(SELECT 1 FROM sandbox_authority a WHERE a.scope_id=c.scope_id AND a.state='ACTIVE' AND a.lease_expires_at>statement_timestamp())
  ORDER BY c.created_at,c.scope_id LIMIT 1
 ) h
 JOIN outbox_events e ON e.submission_id=h.submission_id AND e.job_id IS NOT DISTINCT FROM h.job_id AND e.intent_generation=1 AND e.status='PUBLISHED'
 LEFT JOIN execution_ownership initial_owner ON initial_owner.submission_id=h.submission_id AND h.job_id IS NULL
 LEFT JOIN evaluation_job_ownership job_owner ON job_owner.job_id=h.job_id
 CROSS JOIN scheduler_policy cfg
 WHERE cfg.enabled
 AND (SELECT count(*) FROM sandbox_authority a JOIN submissions s ON s.id=a.submission_id WHERE s.principal_id=p.principal AND a.state='ACTIVE' AND a.lease_expires_at>statement_timestamp())<cfg.principal_active
 AND ((h.job_id IS NULL AND((initial_owner.submission_id IS NULL AND EXISTS(SELECT 1 FROM submissions s WHERE s.id=h.submission_id AND s.status='QUEUED')) OR(initial_owner.mode='agent' AND((initial_owner.state='ACTIVE' AND initial_owner.lease_expires_at<=statement_timestamp()) OR(initial_owner.state='RETRY' AND initial_owner.next_attempt_at<=statement_timestamp())))))
 OR(h.job_id IS NOT NULL AND((job_owner.job_id IS NULL AND EXISTS(SELECT 1 FROM evaluation_jobs j WHERE j.id=h.job_id AND j.state='QUEUED')) OR(job_owner.state='ACTIVE' AND job_owner.lease_expires_at<=statement_timestamp()) OR(job_owner.state='RETRY' AND job_owner.next_attempt_at<=statement_timestamp()))))
 AND (SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>statement_timestamp())<cfg.global_active
 ORDER BY p.last_turn,p.principal LIMIT 1;
$$;
CREATE FUNCTION scheduler_admit(scope UUID,p TEXT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE cfg scheduler_policy; chosen RECORD; next_turn BIGINT;
BEGIN
 PERFORM scheduler_lock();SELECT * INTO cfg FROM scheduler_policy;
 IF NOT cfg.enabled THEN RETURN; END IF;
 IF EXISTS(SELECT 1 FROM scheduler_jobs q WHERE q.scope_id=scope AND q.principal=p AND q.done) THEN RETURN; END IF;
 SELECT * INTO chosen FROM scheduler_next();
 IF NOT FOUND OR chosen.scope_id<>scope OR chosen.principal<>p THEN RAISE EXCEPTION 'scheduler admission deferred' USING ERRCODE='PZ503'; END IF;
 UPDATE scheduler_policy SET turn=turn+1 RETURNING turn INTO next_turn;
 UPDATE scheduler_principals SET last_turn=next_turn WHERE principal=p;
 PERFORM set_config('judgegrid.scheduler_scope',scope::text,true);
END;$$;
CREATE FUNCTION guard_scheduler_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE scope UUID; p TEXT; cfg scheduler_policy;
BEGIN
 IF TG_OP='UPDATE' AND NEW.run_id=OLD.run_id THEN RETURN NEW; END IF;
 SELECT * INTO cfg FROM scheduler_policy;IF NOT cfg.enabled THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='execution_ownership' THEN scope:=NEW.submission_id;ELSE scope:=NEW.job_id; END IF;
 IF scope::text IS DISTINCT FROM current_setting('judgegrid.scheduler_scope',true) THEN RAISE EXCEPTION 'missing scheduler permit'; END IF;
 SELECT principal_id INTO p FROM submissions WHERE id=NEW.submission_id;
 IF (SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>clock_timestamp())>=cfg.global_active OR
 (SELECT count(*) FROM sandbox_authority a JOIN submissions s ON s.id=a.submission_id WHERE s.principal_id=p AND a.state='ACTIVE' AND a.lease_expires_at>clock_timestamp())>=cfg.principal_active THEN RAISE EXCEPTION 'scheduler capacity reached' USING ERRCODE='PZ503'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER initial_scheduler_permit BEFORE INSERT OR UPDATE ON execution_ownership FOR EACH ROW EXECUTE FUNCTION guard_scheduler_ownership();
CREATE TRIGGER rejudge_scheduler_permit BEFORE INSERT OR UPDATE ON evaluation_job_ownership FOR EACH ROW EXECUTE FUNCTION guard_scheduler_ownership();

CREATE TABLE request_buckets(bucket TEXT PRIMARY KEY,tokens DOUBLE PRECISION NOT NULL,updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE sse_connections(id UUID PRIMARY KEY DEFAULT gen_random_uuid(),principal TEXT NOT NULL,expires_at TIMESTAMPTZ NOT NULL);
CREATE INDEX sse_connection_expiry ON sse_connections(expires_at);
CREATE INDEX sse_connection_principal ON sse_connections(principal,expires_at);
