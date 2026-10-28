-- Initial submission workflow remains sealed. Rejudges get distinct logical jobs.
CREATE TABLE evaluation_jobs (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),submission_id UUID NOT NULL REFERENCES submissions(id),
 problem_version_id BIGINT NOT NULL REFERENCES problem_versions(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 200),operator TEXT NOT NULL,
 ordinal BIGINT NOT NULL CHECK(ordinal>0),
 state TEXT NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','COMPLETED','FAILED')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(submission_id,request_key),UNIQUE(submission_id,ordinal),UNIQUE(id,submission_id,problem_version_id),UNIQUE(id,submission_id)
);
ALTER TABLE evaluation_runs ADD COLUMN job_id UUID;
ALTER TABLE evaluation_runs DROP CONSTRAINT evaluation_runs_submission_id_problem_version_id_fkey;
ALTER TABLE evaluation_runs ADD FOREIGN KEY(submission_id) REFERENCES submissions(id);
ALTER TABLE evaluation_runs ADD FOREIGN KEY(job_id,submission_id,problem_version_id) REFERENCES evaluation_jobs(id,submission_id,problem_version_id);
ALTER TABLE evaluation_runs DROP CONSTRAINT evaluation_runs_submission_id_generation_key;
CREATE UNIQUE INDEX initial_evaluation_generation ON evaluation_runs(submission_id,generation) WHERE job_id IS NULL;
CREATE UNIQUE INDEX job_evaluation_generation ON evaluation_runs(job_id,generation) WHERE job_id IS NOT NULL;
CREATE FUNCTION guard_evaluation_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.job_id IS DISTINCT FROM NEW.job_id THEN RAISE EXCEPTION 'immutable evaluation job'; END IF;
 IF NEW.job_id IS NULL AND NOT EXISTS(SELECT 1 FROM submissions WHERE id=NEW.submission_id AND problem_version_id=NEW.problem_version_id) THEN RAISE EXCEPTION 'initial version binding mismatch'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER evaluation_scope BEFORE INSERT OR UPDATE ON evaluation_runs FOR EACH ROW EXECUTE FUNCTION guard_evaluation_scope();

ALTER TABLE outbox_events ADD COLUMN job_id UUID REFERENCES evaluation_jobs(id);
ALTER TABLE outbox_events ADD FOREIGN KEY(job_id,submission_id) REFERENCES evaluation_jobs(id,submission_id);
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_submission_id_event_type_intent_generation_key;
CREATE UNIQUE INDEX initial_outbox_identity ON outbox_events(submission_id,event_type,intent_generation) WHERE job_id IS NULL;
CREATE UNIQUE INDEX rejudge_outbox_identity ON outbox_events(job_id) WHERE job_id IS NOT NULL;
ALTER TABLE outbox_events ADD CHECK(job_id IS NULL OR(payload_version=3 AND intent_generation=1));

CREATE TABLE evaluation_job_ownership (
 job_id UUID PRIMARY KEY REFERENCES evaluation_jobs(id),submission_id UUID NOT NULL REFERENCES submissions(id),
 run_id UUID NOT NULL UNIQUE REFERENCES evaluation_runs(id),event_id UUID NOT NULL REFERENCES outbox_events(id),
 generation INTEGER NOT NULL CHECK(generation BETWEEN 1 AND 5),owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 200),
 mode TEXT NOT NULL DEFAULT 'agent' CHECK(mode='agent'),
 state TEXT NOT NULL CHECK(state IN ('ACTIVE','RETRY','DONE','EXHAUSTED')),
 lease_expires_at TIMESTAMPTZ NOT NULL,heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),max_attempts INTEGER NOT NULL CHECK(max_attempts BETWEEN 1 AND 5),
 started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),failure_code TEXT,
 FOREIGN KEY(run_id,submission_id,generation,owner_id) REFERENCES evaluation_runs(id,submission_id,generation,owner_id),CHECK(generation<=max_attempts)
);
CREATE TRIGGER job_owner_guard BEFORE UPDATE OR DELETE ON evaluation_job_ownership FOR EACH ROW EXECUTE FUNCTION guard_execution_owner();
CREATE FUNCTION guard_job_owner_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.job_id<>OLD.job_id THEN RAISE EXCEPTION 'immutable job ownership scope'; END IF;
 IF NOT EXISTS(SELECT 1 FROM evaluation_runs r JOIN evaluation_jobs j ON j.id=r.job_id JOIN outbox_events e ON e.job_id=j.id WHERE j.id=NEW.job_id AND r.id=NEW.run_id AND r.submission_id=NEW.submission_id AND e.id=NEW.event_id) THEN RAISE EXCEPTION 'job ownership identity mismatch'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER job_owner_scope BEFORE INSERT OR UPDATE ON evaluation_job_ownership FOR EACH ROW EXECUTE FUNCTION guard_job_owner_scope();
CREATE INDEX job_expiry ON evaluation_job_ownership(lease_expires_at) WHERE state='ACTIVE';
CREATE INDEX job_retry ON evaluation_job_ownership(next_attempt_at) WHERE state='RETRY';

CREATE TABLE selected_evaluations (
 submission_id UUID PRIMARY KEY REFERENCES submissions(id),job_id UUID NOT NULL REFERENCES evaluation_jobs(id),
 run_id UUID NOT NULL UNIQUE REFERENCES evaluation_runs(id),ordinal BIGINT NOT NULL,
 selected_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION guard_selected_evaluation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'selection retention disabled'; END IF;
 IF NOT EXISTS(SELECT 1 FROM evaluation_jobs j JOIN evaluation_runs r ON r.job_id=j.id JOIN evaluation_job_ownership o ON o.job_id=j.id AND o.run_id=r.id WHERE j.id=NEW.job_id AND j.submission_id=NEW.submission_id AND j.ordinal=NEW.ordinal AND r.id=NEW.run_id AND r.status='COMPLETED' AND j.state='COMPLETED' AND o.state='DONE' AND NOT EXISTS(SELECT 1 FROM evaluation_jobs newer WHERE newer.submission_id=j.submission_id AND newer.ordinal>j.ordinal)) THEN RAISE EXCEPTION 'invalid selected evaluation'; END IF;
 IF TG_OP='UPDATE' AND (NEW.submission_id<>OLD.submission_id OR NEW.ordinal<=OLD.ordinal) THEN RAISE EXCEPTION 'selection cannot regress'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER selected_evaluation_guard BEFORE INSERT OR UPDATE OR DELETE ON selected_evaluations FOR EACH ROW EXECUTE FUNCTION guard_selected_evaluation();
CREATE FUNCTION guard_evaluation_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'evaluation job retention disabled'; END IF;
 IF ROW(NEW.id,NEW.submission_id,NEW.problem_version_id,NEW.request_key,NEW.operator,NEW.ordinal,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.submission_id,OLD.problem_version_id,OLD.request_key,OLD.operator,OLD.ordinal,OLD.created_at) OR NOT((OLD.state='QUEUED' AND NEW.state='RUNNING') OR(OLD.state='RUNNING' AND NEW.state IN ('COMPLETED','FAILED'))) THEN RAISE EXCEPTION 'immutable evaluation job/invalid transition'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER evaluation_job_guard BEFORE UPDATE OR DELETE ON evaluation_jobs FOR EACH ROW EXECUTE FUNCTION guard_evaluation_job();
CREATE FUNCTION guard_new_evaluation_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM submissions s JOIN problem_versions original ON original.id=s.problem_version_id JOIN problem_versions target ON target.id=NEW.problem_version_id JOIN problem_test_suites suite ON suite.problem_version_id=target.id WHERE s.id=NEW.submission_id AND s.principal_id=NEW.operator AND s.status='COMPLETED' AND s.language IN ('cpp17','python3') AND target.problem_id=s.problem_id AND target.version_number>original.version_number) THEN RAISE EXCEPTION 'ineligible rejudge scope'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER new_evaluation_job_scope BEFORE INSERT ON evaluation_jobs FOR EACH ROW EXECUTE FUNCTION guard_new_evaluation_job();
CREATE FUNCTION guard_outbox_job_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.job_id IS DISTINCT FROM OLD.job_id THEN RAISE EXCEPTION 'immutable outbox job identity'; END IF;RETURN NEW;
END;$$;
CREATE TRIGGER outbox_job_scope BEFORE UPDATE ON outbox_events FOR EACH ROW EXECUTE FUNCTION guard_outbox_job_scope();

CREATE OR REPLACE FUNCTION assert_execution_fence(sid UUID,rid UUID DEFAULT NULL) RETURNS void LANGUAGE plpgsql AS $$
DECLARE o RECORD; jid UUID;
BEGIN
 SELECT job_id INTO jid FROM evaluation_runs WHERE id=rid;
 IF jid IS NOT NULL THEN SELECT * INTO o FROM evaluation_job_ownership WHERE job_id=jid FOR UPDATE;
 ELSE SELECT * INTO o FROM execution_ownership WHERE submission_id=sid FOR UPDATE; END IF;
 IF NOT FOUND THEN
  IF jid IS NOT NULL THEN RAISE EXCEPTION 'missing job authority'; END IF;
  RETURN;
 END IF;
 IF o.state<>'ACTIVE' OR o.lease_expires_at<=clock_timestamp() OR o.submission_id<>sid OR o.owner_id IS DISTINCT FROM current_setting('judgegrid.execution_owner',true) OR o.generation::text IS DISTINCT FROM current_setting('judgegrid.execution_generation',true) OR o.run_id::text IS DISTINCT FROM current_setting('judgegrid.execution_run',true) OR(rid IS NOT NULL AND rid<>o.run_id) THEN RAISE EXCEPTION 'execution ownership lost' USING ERRCODE='23514'; END IF;
END;$$;
CREATE OR REPLACE FUNCTION fence_evaluation_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.owner_id IS DISTINCT FROM NEW.owner_id THEN RAISE EXCEPTION 'immutable attempt owner'; END IF;
 IF NEW.status='FAILED' AND NEW.failure_code='lease_expired' AND OLD.status='RUNNING' AND
 (EXISTS(SELECT 1 FROM execution_ownership WHERE run_id=OLD.id AND state='ACTIVE' AND lease_expires_at<=clock_timestamp()) OR EXISTS(SELECT 1 FROM evaluation_job_ownership WHERE run_id=OLD.id AND state='ACTIVE' AND lease_expires_at<=clock_timestamp())) THEN RETURN NEW; END IF;
 PERFORM assert_execution_fence(OLD.submission_id,OLD.id);RETURN NEW;
END;$$;
CREATE VIEW sandbox_authority AS
 SELECT submission_id AS scope_id,submission_id,run_id,generation,owner_id,state,lease_expires_at FROM execution_ownership
 UNION ALL SELECT job_id,submission_id,run_id,generation,owner_id,state,lease_expires_at FROM evaluation_job_ownership;

