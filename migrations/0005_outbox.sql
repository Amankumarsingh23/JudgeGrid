CREATE TABLE outbox_events (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 submission_id UUID NOT NULL REFERENCES submissions(id),
 event_type TEXT NOT NULL DEFAULT 'SUBMISSION_READY' CHECK(event_type='SUBMISSION_READY'),
 payload_version INTEGER NOT NULL DEFAULT 2 CHECK(payload_version>0),
 intent_generation BIGINT NOT NULL DEFAULT 1 CHECK(intent_generation>0),
 status TEXT NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','CLAIMED','PUBLISHED','QUARANTINED')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 published_at TIMESTAMPTZ,
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 retry_base INTEGER NOT NULL DEFAULT 0 CHECK(retry_base>=0 AND retry_base<=attempts),
 claim_generation BIGINT NOT NULL DEFAULT 0 CHECK(claim_generation>=0),
 claim_token UUID,
 claim_expires_at TIMESTAMPTZ,
 last_error_code TEXT CHECK(last_error_code IN ('redis_unavailable_or_uncertain','redis_configuration','malformed_event','retry_exhausted','publication_record_uncertain','operator_retry')),
 stream_id TEXT,
 UNIQUE(submission_id,event_type,intent_generation),
 CHECK((status='CLAIMED' AND claim_token IS NOT NULL AND claim_expires_at IS NOT NULL) OR(status<>'CLAIMED' AND claim_token IS NULL AND claim_expires_at IS NULL)),
 CHECK((status='PUBLISHED' AND published_at IS NOT NULL AND stream_id IS NOT NULL) OR(status<>'PUBLISHED' AND published_at IS NULL AND stream_id IS NULL))
);
CREATE INDEX outbox_due ON outbox_events(next_attempt_at,created_at,id) WHERE status='PENDING';
CREATE INDEX outbox_expired_claims ON outbox_events(claim_expires_at,id) WHERE status='CLAIMED';
CREATE INDEX outbox_status_age ON outbox_events(status,created_at,id);
CREATE FUNCTION guard_outbox_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'outbox retention is disabled' USING ERRCODE='23514'; END IF;
 IF OLD.status='PUBLISHED' OR
 ROW(NEW.id,NEW.submission_id,NEW.event_type,NEW.payload_version,NEW.intent_generation,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.submission_id,OLD.event_type,OLD.payload_version,OLD.intent_generation,OLD.created_at) OR
 NEW.attempts<OLD.attempts OR NEW.claim_generation<OLD.claim_generation THEN
  RAISE EXCEPTION 'immutable outbox identity/history' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER guarded_outbox_history BEFORE UPDATE OR DELETE ON outbox_events FOR EACH ROW EXECUTE FUNCTION guard_outbox_history();
-- Deliberately no historical backfill: operators must select individual QUEUED IDs.
