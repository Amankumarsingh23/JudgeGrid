ALTER TABLE evaluation_runs ADD COLUMN owner_id TEXT;
ALTER TABLE evaluation_runs ADD CONSTRAINT evaluation_owner_identity UNIQUE(id,submission_id,generation,owner_id);
ALTER TABLE evaluation_runs DROP CONSTRAINT safe_evaluation_failure_code;
ALTER TABLE evaluation_runs ADD CONSTRAINT safe_evaluation_failure_code CHECK(failure_code IS NULL OR failure_code IN ('sandbox_infrastructure','bundle_verification_failed','evaluation_canceled','invalid_configuration','lease_expired','attempt_limit'));
CREATE TABLE execution_ownership (
 submission_id UUID PRIMARY KEY REFERENCES submissions(id),
 run_id UUID NOT NULL UNIQUE REFERENCES evaluation_runs(id),
 event_id UUID REFERENCES outbox_events(id),
 generation INTEGER NOT NULL CHECK(generation>0),
 owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 200),
 mode TEXT NOT NULL CHECK(mode IN ('local','agent')),
 state TEXT NOT NULL CHECK(state IN ('ACTIVE','RETRY','DONE','EXHAUSTED')),
 lease_expires_at TIMESTAMPTZ NOT NULL,
 heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 max_attempts INTEGER NOT NULL CHECK(max_attempts BETWEEN 1 AND 5),
 started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 failure_code TEXT,
 FOREIGN KEY(run_id,submission_id,generation,owner_id) REFERENCES evaluation_runs(id,submission_id,generation,owner_id),
 CHECK(generation<=max_attempts),
 CHECK(mode<>'agent' OR event_id IS NOT NULL)
);
CREATE INDEX execution_expired ON execution_ownership(lease_expires_at,submission_id) WHERE state='ACTIVE' AND mode='agent';
CREATE INDEX execution_retry ON execution_ownership(next_attempt_at,submission_id) WHERE state='RETRY' AND mode='agent';
CREATE INDEX execution_queued_principal ON submissions(principal_id,created_at,id) WHERE status='QUEUED';
CREATE TABLE execution_rejections (
 submission_id UUID PRIMARY KEY REFERENCES submissions(id),event_id UUID NOT NULL REFERENCES outbox_events(id),
 reason TEXT NOT NULL CHECK(reason IN ('unsupported_language','bundle_unavailable')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER immutable_execution_rejections BEFORE UPDATE OR DELETE ON execution_rejections FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();

CREATE FUNCTION assert_execution_fence(sid UUID,rid UUID DEFAULT NULL) RETURNS void LANGUAGE plpgsql AS $$
DECLARE o execution_ownership;
BEGIN
 SELECT * INTO o FROM execution_ownership WHERE submission_id=sid FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF; -- Historical unleased records cannot gain a new owner through result writes.
 IF o.state<>'ACTIVE' OR o.lease_expires_at<=clock_timestamp() OR
    o.owner_id IS DISTINCT FROM current_setting('judgegrid.execution_owner',true) OR
    o.generation::text IS DISTINCT FROM current_setting('judgegrid.execution_generation',true) OR
    o.run_id::text IS DISTINCT FROM current_setting('judgegrid.execution_run',true) OR
    (rid IS NOT NULL AND rid<>o.run_id) THEN
  RAISE EXCEPTION 'execution ownership lost' USING ERRCODE='23514';
 END IF;
END;
$$;
CREATE FUNCTION fence_submission_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='FAILED' AND NEW.failure_reason='attempt_limit' AND EXISTS(SELECT 1 FROM execution_ownership o JOIN evaluation_runs r ON r.id=o.run_id WHERE o.submission_id=OLD.id AND o.state='ACTIVE' AND o.generation=o.max_attempts AND o.lease_expires_at<=clock_timestamp() AND r.status='FAILED' AND r.failure_code='lease_expired') THEN RETURN NEW; END IF;
 PERFORM assert_execution_fence(OLD.id); RETURN NEW; END;
$$;
CREATE TRIGGER execution_submission_fence BEFORE UPDATE ON submissions FOR EACH ROW EXECUTE FUNCTION fence_submission_write();
CREATE FUNCTION fence_evaluation_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.owner_id IS DISTINCT FROM NEW.owner_id THEN RAISE EXCEPTION 'immutable attempt owner' USING ERRCODE='23514'; END IF;
 -- Only the recovery transaction can abandon an expired RUNNING attempt, without result evidence.
 IF NEW.status='FAILED' AND NEW.failure_code='lease_expired' AND OLD.status='RUNNING' AND
 EXISTS(SELECT 1 FROM execution_ownership WHERE run_id=OLD.id AND state='ACTIVE' AND lease_expires_at<=clock_timestamp()) THEN RETURN NEW; END IF;
 PERFORM assert_execution_fence(OLD.submission_id,OLD.id); RETURN NEW;
END;
$$;
CREATE TRIGGER execution_evaluation_fence BEFORE UPDATE ON evaluation_runs FOR EACH ROW EXECUTE FUNCTION fence_evaluation_write();
CREATE FUNCTION fence_evidence_write() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE sid UUID;
BEGIN SELECT submission_id INTO sid FROM evaluation_runs WHERE id=NEW.evaluation_run_id;
 PERFORM assert_execution_fence(sid,NEW.evaluation_run_id); RETURN NEW; END;
$$;
CREATE TRIGGER execution_evidence_fence BEFORE INSERT ON evaluation_test_results FOR EACH ROW EXECUTE FUNCTION fence_evidence_write();
CREATE FUNCTION guard_execution_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'ownership history cannot be deleted' USING ERRCODE='23514'; END IF;
 IF NEW.submission_id<>OLD.submission_id OR NEW.mode<>OLD.mode OR NEW.event_id IS DISTINCT FROM OLD.event_id OR NEW.max_attempts<>OLD.max_attempts OR NEW.started_at<>OLD.started_at OR OLD.state IN ('DONE','EXHAUSTED') THEN
 RAISE EXCEPTION 'invalid ownership history' USING ERRCODE='23514'; END IF;
 IF NEW.generation=OLD.generation THEN
  IF NEW.owner_id<>OLD.owner_id OR NEW.run_id<>OLD.run_id OR OLD.state<>'ACTIVE' OR (OLD.lease_expires_at<=clock_timestamp() AND NOT(NEW.state='EXHAUSTED' AND OLD.generation=OLD.max_attempts)) THEN
   RAISE EXCEPTION 'expired or changed ownership' USING ERRCODE='23514'; END IF;
  IF NEW.state NOT IN ('ACTIVE','RETRY','DONE','EXHAUSTED') THEN RAISE EXCEPTION 'invalid lease transition'; END IF;
 ELSIF NEW.generation=OLD.generation+1 THEN
  IF NOT ((OLD.state='ACTIVE' AND OLD.lease_expires_at<=clock_timestamp()) OR(OLD.state='RETRY' AND OLD.next_attempt_at<=clock_timestamp())) OR NEW.state<>'ACTIVE' OR NEW.run_id=OLD.run_id THEN
   RAISE EXCEPTION 'invalid takeover' USING ERRCODE='23514'; END IF;
 ELSE RAISE EXCEPTION 'generation regression or jump' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER execution_owner_guard BEFORE UPDATE OR DELETE ON execution_ownership FOR EACH ROW EXECUTE FUNCTION guard_execution_owner();
