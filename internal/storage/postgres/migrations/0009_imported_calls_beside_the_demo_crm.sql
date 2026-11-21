-- Imported call transcripts beside the demo CRM.
--
-- A demo workspace holds demo connections only, with one exception: its calls may be imported
-- transcripts, which the operator reads from local files and binds to the demo CRM's accounts. A demo
-- workspace's CRM and Slack stay demo data, and a live workspace still holds no demo connection.
ALTER TABLE connections DROP CONSTRAINT connections_check;
ALTER TABLE connections ADD CONSTRAINT connections_check CHECK (
    mode = 'none'
    OR (workspace_kind = 'demo') = (mode IN ('demo', 'fixture'))
    OR (workspace_kind = 'demo' AND source = 'calls' AND mode = 'imported'));

-- Demo call notes and imported transcripts never share a workspace: a calls connection becomes imported
-- only where no finished read returned a call record that is not an import. The trigger runs with the
-- connection's row locked, and a read finishes ok only under a share lock on that row at its own
-- generation, so a demo read either committed before this check, and is seen, or finishes after the
-- change, and is refused as changed.
CREATE FUNCTION gtm_imported_calls_alone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM receipts r, jsonb_array_elements_text(r.rows) AS row
                WHERE r.workspace_id = NEW.workspace_id AND r.status = 'ok' AND row LIKE 'calls/%'
                  AND row !~ '^calls/call_notes/note-[0-9a-f]{64}$') THEN
        RAISE EXCEPTION 'this workspace has read demo call notes, which never share a workspace with imports'
            USING ERRCODE = 'GT020';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER connections_imported_calls_alone BEFORE INSERT OR UPDATE ON connections
    FOR EACH ROW WHEN (NEW.source = 'calls' AND NEW.mode = 'imported')
    EXECUTE FUNCTION gtm_imported_calls_alone();
