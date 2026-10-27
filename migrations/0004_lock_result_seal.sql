-- Serialize evidence insertion against terminal finalization, including direct trusted SQL callers.
CREATE OR REPLACE FUNCTION guard_test_result_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM evaluation_runs WHERE id=NEW.evaluation_run_id AND status='RUNNING' FOR UPDATE;
 IF NOT FOUND THEN
  RAISE EXCEPTION 'evaluation already finalized' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;

CREATE FUNCTION validate_suite_seal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actual_count INTEGER; first_ordinal INTEGER; last_ordinal INTEGER;
BEGIN
 SELECT count(*),min(ordinal),max(ordinal) INTO actual_count,first_ordinal,last_ordinal
 FROM suite_cases WHERE problem_version_id=NEW.problem_version_id;
 IF actual_count<>NEW.test_count OR first_ordinal<>1 OR last_ordinal<>NEW.test_count THEN
  RAISE EXCEPTION 'test suite metadata is incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER validate_suite_seal BEFORE INSERT ON problem_test_suites FOR EACH ROW EXECUTE FUNCTION validate_suite_seal();

ALTER TABLE evaluation_runs ADD CONSTRAINT safe_evaluation_failure_code
 CHECK(failure_code IS NULL OR failure_code IN ('sandbox_infrastructure','bundle_verification_failed','evaluation_canceled','invalid_configuration'));
