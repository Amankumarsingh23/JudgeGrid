CREATE TABLE public_event_counters(submission_id UUID PRIMARY KEY REFERENCES submissions(id),seq BIGINT NOT NULL DEFAULT 0 CHECK(seq>=0));
CREATE TABLE public_events (
 submission_id UUID NOT NULL REFERENCES submissions(id),seq BIGINT NOT NULL CHECK(seq>0),kind TEXT NOT NULL,
 payload JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(submission_id,seq),
 CHECK(kind IN ('snapshot','accepted','claimed','preparing','compiling','running','completed','retry','failed','rejudge_requested','rejudge_running','rejudge_completed','selected')),
 CHECK(jsonb_typeof(payload)='object' AND octet_length(payload::text)<=2048 AND payload-ARRAY['status','evaluation_job_id','evaluation_run_id','problem_version_id','generation','verdict','selected','failure_code']::text[]='{}'::jsonb)
);
CREATE TRIGGER immutable_public_events BEFORE UPDATE OR DELETE ON public_events FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();
CREATE FUNCTION append_public_event(sid UUID,event_kind TEXT,data JSONB) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE n BIGINT;
BEGIN
 -- Updating this row holds sequence allocation until commit. B cannot allocate
 -- after A and then commit ahead of A for the same submission.
 INSERT INTO public_event_counters(submission_id) VALUES(sid) ON CONFLICT DO NOTHING;
 UPDATE public_event_counters SET seq=seq+1 WHERE submission_id=sid RETURNING seq INTO n;
 INSERT INTO public_events(submission_id,seq,kind,payload) VALUES(sid,n,event_kind,data);
 RETURN n;
END;$$;
CREATE FUNCTION journal_submission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN PERFORM append_public_event(NEW.id,'accepted',jsonb_build_object('status','QUEUED','problem_version_id',NEW.problem_version_id));
 ELSIF NEW.status='COMPLETED' AND NEW.status<>OLD.status THEN PERFORM append_public_event(NEW.id,'completed',jsonb_build_object('status',NEW.status,'verdict',NEW.verdict,'problem_version_id',NEW.problem_version_id,'selected',true));
 ELSIF NEW.status='FAILED' AND NEW.status<>OLD.status THEN PERFORM append_public_event(NEW.id,'failed',jsonb_build_object('status',NEW.status,'failure_code',CASE WHEN NEW.failure_reason IN ('sandbox_infrastructure','bundle_verification_failed','evaluation_canceled','invalid_configuration','attempt_limit','lease_expired') THEN NEW.failure_reason ELSE 'infrastructure_failed' END));
 END IF;RETURN NEW;
END;$$;
CREATE TRIGGER public_submission_progress AFTER INSERT OR UPDATE OF status ON submissions FOR EACH ROW EXECUTE FUNCTION journal_submission();
CREATE FUNCTION journal_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN PERFORM append_public_event(NEW.submission_id,'rejudge_requested',jsonb_build_object('status','QUEUED','evaluation_job_id',NEW.id,'problem_version_id',NEW.problem_version_id));
 ELSIF NEW.state='COMPLETED' AND OLD.state<>NEW.state THEN PERFORM append_public_event(NEW.submission_id,'rejudge_completed',jsonb_build_object('status',NEW.state,'evaluation_job_id',NEW.id,'problem_version_id',NEW.problem_version_id,'selected',false));
 ELSIF NEW.state='FAILED' AND OLD.state<>NEW.state THEN PERFORM append_public_event(NEW.submission_id,'failed',jsonb_build_object('status',NEW.state,'evaluation_job_id',NEW.id));
 END IF;RETURN NEW;
END;$$;
CREATE TRIGGER public_rejudge_progress AFTER INSERT OR UPDATE OF state ON evaluation_jobs FOR EACH ROW EXECUTE FUNCTION journal_job();
CREATE FUNCTION journal_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE jid UUID; event_kind TEXT;
BEGIN
 SELECT job_id INTO jid FROM evaluation_runs WHERE id=NEW.run_id;
 IF TG_OP='INSERT' OR NEW.run_id<>OLD.run_id THEN
  event_kind:=CASE WHEN jid IS NULL THEN 'claimed' ELSE 'rejudge_running' END;
  PERFORM append_public_event(NEW.submission_id,event_kind,jsonb_build_object('status','RUNNING','evaluation_job_id',jid,'evaluation_run_id',NEW.run_id,'generation',NEW.generation));
 ELSIF NEW.state='RETRY' AND OLD.state<>NEW.state THEN PERFORM append_public_event(NEW.submission_id,'retry',jsonb_build_object('status','RETRY','evaluation_job_id',jid,'evaluation_run_id',NEW.run_id,'failure_code',CASE WHEN NEW.failure_code IN ('sandbox_infrastructure','bundle_verification_failed','evaluation_canceled','invalid_configuration','attempt_limit','lease_expired') THEN NEW.failure_code ELSE 'infrastructure_failed' END));
 END IF;RETURN NEW;
END;$$;
CREATE TRIGGER public_initial_claim AFTER INSERT OR UPDATE ON execution_ownership FOR EACH ROW EXECUTE FUNCTION journal_ownership();
CREATE TRIGGER public_rejudge_claim AFTER INSERT OR UPDATE ON evaluation_job_ownership FOR EACH ROW EXECUTE FUNCTION journal_ownership();
CREATE FUNCTION journal_selection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r evaluation_runs;
BEGIN SELECT * INTO r FROM evaluation_runs WHERE id=NEW.run_id;
 PERFORM append_public_event(NEW.submission_id,'selected',jsonb_build_object('status','COMPLETED','verdict',r.final_verdict,'evaluation_job_id',NEW.job_id,'evaluation_run_id',NEW.run_id,'problem_version_id',r.problem_version_id,'selected',true));RETURN NEW;
END;$$;
CREATE TRIGGER public_result_selection AFTER INSERT OR UPDATE ON selected_evaluations FOR EACH ROW EXECUTE FUNCTION journal_selection();
-- Historical records start with truthful snapshots rather than invented queued events.
INSERT INTO public_event_counters(submission_id) SELECT id FROM submissions;
SELECT append_public_event(s.id,'snapshot',jsonb_build_object('status',s.status,'verdict',COALESCE(r.final_verdict,s.verdict),'problem_version_id',COALESCE(r.problem_version_id,s.problem_version_id))) FROM submissions s LEFT JOIN selected_evaluations x ON x.submission_id=s.id LEFT JOIN evaluation_runs r ON r.id=x.run_id;

CREATE TABLE local_users(principal TEXT PRIMARY KEY CHECK(principal ~ '^[a-z][a-z0-9_-]{0,63}$'),salt BYTEA NOT NULL CHECK(octet_length(salt)=16),password_hash BYTEA NOT NULL CHECK(octet_length(password_hash)=32),created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp());
CREATE TABLE local_sessions(token_hash TEXT PRIMARY KEY CHECK(token_hash ~ '^[0-9a-f]{64}$'),principal TEXT NOT NULL REFERENCES local_users(principal),csrf_token TEXT NOT NULL CHECK(csrf_token ~ '^[0-9a-f]{64}$'),expires_at TIMESTAMPTZ NOT NULL);
CREATE INDEX local_session_expiry ON local_sessions(expires_at);
