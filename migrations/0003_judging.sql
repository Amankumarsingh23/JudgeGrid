ALTER TABLE submissions ADD CONSTRAINT submissions_id_version_unique UNIQUE(id,problem_version_id);

CREATE TABLE problem_test_suites (
 problem_version_id BIGINT PRIMARY KEY REFERENCES problem_versions(id),
 bundle_sha TEXT NOT NULL CHECK(bundle_sha ~ '^[0-9a-f]{64}$'),
 comparator TEXT NOT NULL CHECK(comparator IN ('exact','tokens')),
 test_count INTEGER NOT NULL CHECK(test_count BETWEEN 1 AND 16),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(problem_version_id,bundle_sha)
);
CREATE TABLE suite_cases (
 problem_version_id BIGINT NOT NULL,
 case_id TEXT NOT NULL CHECK(case_id ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 16),
 is_sample BOOLEAN NOT NULL,
 input_sha TEXT NOT NULL CHECK(input_sha ~ '^[0-9a-f]{64}$'),
 expected_sha TEXT NOT NULL CHECK(expected_sha ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY(problem_version_id,case_id),
 UNIQUE(problem_version_id,ordinal),
 FOREIGN KEY(problem_version_id) REFERENCES problem_test_suites(problem_version_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE FUNCTION reject_judge_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'judging metadata/evidence is immutable' USING ERRCODE='23514'; END;
$$;
CREATE TRIGGER immutable_test_suite BEFORE UPDATE OR DELETE ON problem_test_suites FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();
CREATE TRIGGER immutable_suite_cases BEFORE UPDATE OR DELETE ON suite_cases FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();
CREATE FUNCTION guard_suite_case_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM problem_test_suites WHERE problem_version_id=NEW.problem_version_id) THEN
  RAISE EXCEPTION 'published test suite is sealed' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sealed_suite_cases BEFORE INSERT ON suite_cases FOR EACH ROW EXECUTE FUNCTION guard_suite_case_insert();

CREATE TABLE evaluation_runs (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 submission_id UUID NOT NULL,
 problem_version_id BIGINT NOT NULL,
 bundle_sha TEXT NOT NULL,
 generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0),
 evaluator_version TEXT NOT NULL CHECK(length(evaluator_version)>0),
 status TEXT NOT NULL DEFAULT 'RUNNING' CHECK(status IN ('RUNNING','COMPLETED','FAILED')),
 final_verdict TEXT CHECK(final_verdict IN ('AC','WA','CE','RE','TLE','MLE','OLE')),
 failure_code TEXT,
 runtime_image TEXT CHECK(runtime_image ~ '^sha256:[0-9a-f]{64}$'),
 execution_policy TEXT,
 compile_evidence JSONB,
 compile_reason TEXT,
 result_sha TEXT CHECK(result_sha ~ '^[0-9a-f]{64}$'),
 tests_total INTEGER NOT NULL CHECK(tests_total BETWEEN 1 AND 16),
 tests_run INTEGER NOT NULL DEFAULT 0 CHECK(tests_run>=0),
 tests_passed INTEGER NOT NULL DEFAULT 0 CHECK(tests_passed>=0),
 started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 finished_at TIMESTAMPTZ,
 FOREIGN KEY(submission_id,problem_version_id) REFERENCES submissions(id,problem_version_id),
 FOREIGN KEY(problem_version_id,bundle_sha) REFERENCES problem_test_suites(problem_version_id,bundle_sha),
 UNIQUE(submission_id,generation),
 UNIQUE(id,problem_version_id),
 CHECK(tests_passed<=tests_run AND tests_run<=tests_total),
 CHECK((status='COMPLETED' AND final_verdict IS NOT NULL AND result_sha IS NOT NULL AND runtime_image IS NOT NULL AND execution_policy IS NOT NULL AND length(execution_policy)>0)
   OR (status<>'COMPLETED' AND final_verdict IS NULL AND result_sha IS NULL)),
 CHECK((status='FAILED' AND failure_code IS NOT NULL AND length(failure_code)>0) OR(status<>'FAILED' AND failure_code IS NULL)),
 CHECK((status='RUNNING' AND finished_at IS NULL) OR(status<>'RUNNING' AND finished_at IS NOT NULL)),
 CHECK(final_verdict IS DISTINCT FROM 'AC' OR(tests_run=tests_total AND tests_passed=tests_total)),
 CHECK(final_verdict IS DISTINCT FROM 'CE' OR(tests_run=0 AND compile_evidence IS NOT NULL AND compile_reason IS NOT NULL)),
 CHECK(final_verdict IS NULL OR final_verdict IN ('AC','CE') OR(tests_run>0 AND tests_passed=tests_run-1))
);
CREATE INDEX evaluation_runs_submission_history ON evaluation_runs(submission_id,started_at DESC);

CREATE FUNCTION guard_evaluation_run_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status<>'RUNNING' OR NEW.status NOT IN ('COMPLETED','FAILED') OR
 ROW(NEW.id,NEW.submission_id,NEW.problem_version_id,NEW.bundle_sha,NEW.generation,NEW.evaluator_version,NEW.tests_total,NEW.started_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.submission_id,OLD.problem_version_id,OLD.bundle_sha,OLD.generation,OLD.evaluator_version,OLD.tests_total,OLD.started_at) THEN
  RAISE EXCEPTION 'invalid evaluation history update' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER guarded_evaluation_updates BEFORE UPDATE ON evaluation_runs FOR EACH ROW EXECUTE FUNCTION guard_evaluation_run_update();
CREATE TRIGGER no_evaluation_deletes BEFORE DELETE ON evaluation_runs FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();

CREATE TABLE evaluation_test_results (
 evaluation_run_id UUID NOT NULL,
 problem_version_id BIGINT NOT NULL,
 case_id TEXT NOT NULL,
 verdict TEXT NOT NULL CHECK(verdict IN ('AC','WA','RE','TLE','MLE','OLE')),
 output_match BOOLEAN,
 outcome TEXT NOT NULL CHECK(outcome IN ('exited','nonzero_exit','wall_timeout','confirmed_oom','output_limit')),
 exit_code INTEGER NOT NULL,
 wall_time_ns BIGINT NOT NULL CHECK(wall_time_ns>=0),
 oom_killed BOOLEAN NOT NULL,
 cpu_time_ns BIGINT CHECK(cpu_time_ns>=0),
 peak_memory_bytes BIGINT CHECK(peak_memory_bytes>=0),
 stdout_sha TEXT NOT NULL CHECK(stdout_sha ~ '^[0-9a-f]{64}$'),
 stderr_sha TEXT NOT NULL CHECK(stderr_sha ~ '^[0-9a-f]{64}$'),
 output_bytes INTEGER NOT NULL CHECK(output_bytes BETWEEN 0 AND 65536),
 PRIMARY KEY(evaluation_run_id,case_id),
 FOREIGN KEY(evaluation_run_id,problem_version_id) REFERENCES evaluation_runs(id,problem_version_id),
 FOREIGN KEY(problem_version_id,case_id) REFERENCES suite_cases(problem_version_id,case_id),
 CHECK((verdict IN ('AC','WA') AND output_match IS NOT NULL AND outcome='exited') OR(verdict NOT IN ('AC','WA') AND output_match IS NULL)),
 CHECK(verdict<>'MLE' OR oom_killed),
 CHECK(verdict<>'AC' OR output_match),
 CHECK(verdict<>'WA' OR NOT output_match)
);
CREATE TRIGGER immutable_test_results BEFORE UPDATE OR DELETE ON evaluation_test_results FOR EACH ROW EXECUTE FUNCTION reject_judge_history_mutation();
CREATE FUNCTION guard_test_result_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM evaluation_runs WHERE id=NEW.evaluation_run_id AND status='RUNNING') THEN
  RAISE EXCEPTION 'evaluation already finalized' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sealed_evaluation_results BEFORE INSERT ON evaluation_test_results FOR EACH ROW EXECUTE FUNCTION guard_test_result_insert();
