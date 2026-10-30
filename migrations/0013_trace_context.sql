-- Correlation only: this value cannot authorize dispatch, execution or a result.
ALTER TABLE outbox_events ADD COLUMN traceparent TEXT CHECK(traceparent IS NULL OR(traceparent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$' AND substring(traceparent,4,32)<>repeat('0',32) AND substring(traceparent,37,16)<>repeat('0',16)));
CREATE FUNCTION capture_outbox_trace() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p TEXT;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.traceparent IS DISTINCT FROM OLD.traceparent THEN RAISE EXCEPTION 'immutable producer trace context';END IF;
 ELSE
  p:=current_setting('judgegrid.traceparent',true);
  IF p ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$' AND substring(p,4,32)<>repeat('0',32) AND substring(p,37,16)<>repeat('0',16) THEN NEW.traceparent:=p;ELSE NEW.traceparent:=NULL;END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER outbox_trace BEFORE INSERT OR UPDATE OF traceparent ON outbox_events FOR EACH ROW EXECUTE FUNCTION capture_outbox_trace();
