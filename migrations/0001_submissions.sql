CREATE TABLE problems (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 slug TEXT NOT NULL UNIQUE CHECK (length(slug) BETWEEN 1 AND 100),
 title TEXT NOT NULL CHECK (length(trim(title)) > 0),
 current_version_id BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE problem_versions (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 problem_id BIGINT NOT NULL REFERENCES problems(id),
 version_number INTEGER NOT NULL CHECK (version_number > 0),
 statement TEXT NOT NULL,
 time_limit_ms INTEGER NOT NULL CHECK (time_limit_ms > 0),
 memory_limit_kb INTEGER NOT NULL CHECK (memory_limit_kb > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (problem_id, version_number),
 UNIQUE (id, problem_id)
);
ALTER TABLE problems ADD CONSTRAINT current_version_belongs_to_problem
 FOREIGN KEY (current_version_id, id) REFERENCES problem_versions(id, problem_id);

CREATE FUNCTION reject_problem_version_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'problem versions are immutable' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER immutable_problem_versions BEFORE UPDATE OR DELETE ON problem_versions
 FOR EACH ROW EXECUTE FUNCTION reject_problem_version_mutation();

CREATE TABLE submissions (
 id UUID PRIMARY KEY,
 problem_id BIGINT NOT NULL REFERENCES problems(id),
 problem_version_id BIGINT NOT NULL,
 principal_id TEXT NOT NULL CHECK (length(trim(principal_id)) BETWEEN 1 AND 200),
 language TEXT NOT NULL CHECK (language IN ('cpp17', 'python3', 'go')),
 source_code TEXT NOT NULL CHECK (octet_length(source_code) BETWEEN 1 AND 65536),
 status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','RUNNING','EVALUATING','COMPLETED','FAILED')),
 verdict TEXT CHECK (verdict IN ('AC','WA','TLE','MLE','RE','CE','OLE')),
 failure_reason TEXT,
 state_version BIGINT NOT NULL DEFAULT 0 CHECK (state_version >= 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY (problem_version_id, problem_id) REFERENCES problem_versions(id, problem_id),
 UNIQUE (id, principal_id),
 CHECK ((status = 'COMPLETED' AND verdict IS NOT NULL) OR (status <> 'COMPLETED' AND verdict IS NULL)),
 CHECK ((status = 'FAILED' AND length(trim(failure_reason)) > 0 AND failure_reason IS NOT NULL)
   OR (status <> 'FAILED' AND failure_reason IS NULL))
);
CREATE INDEX submissions_owner_cursor ON submissions(principal_id, created_at DESC, id DESC);
CREATE INDEX submissions_status ON submissions(status, created_at, id);

CREATE FUNCTION guard_submission_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.problem_id,NEW.problem_version_id,NEW.principal_id,NEW.language,NEW.source_code,NEW.created_at)
   IS DISTINCT FROM ROW(OLD.id,OLD.problem_id,OLD.problem_version_id,OLD.principal_id,OLD.language,OLD.source_code,OLD.created_at) THEN
  RAISE EXCEPTION 'submission intent is immutable' USING ERRCODE = '23514';
 END IF;
 IF NOT ((OLD.status='QUEUED' AND NEW.status IN ('RUNNING','FAILED'))
   OR (OLD.status='RUNNING' AND NEW.status IN ('EVALUATING','FAILED'))
   OR (OLD.status='EVALUATING' AND NEW.status IN ('COMPLETED','FAILED')))
   OR NEW.state_version <> OLD.state_version+1 THEN
  RAISE EXCEPTION 'invalid submission transition' USING ERRCODE = '23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER guarded_submission_updates BEFORE UPDATE ON submissions
 FOR EACH ROW EXECUTE FUNCTION guard_submission_update();

CREATE TABLE submission_idempotency (
 principal_id TEXT NOT NULL,
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
 request_fingerprint TEXT NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
 submission_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (principal_id, idempotency_key),
 FOREIGN KEY (submission_id, principal_id) REFERENCES submissions(id, principal_id)
);
