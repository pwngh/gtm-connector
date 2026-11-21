-- A restore checks the rule of 0009 too: no workspace has its calls connected to imported transcripts
-- once a read there returned a call record that is no import.
--
-- A restore loads its rows with the tables' triggers off, so gtm_imported_calls_alone does not see them,
-- and gtm_integrity_violations is what checks them. It keeps every check it held, under another name, and
-- adds this one. A later migration that replaces gtm_integrity_violations() must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_receipt_card_and_line_violations;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_receipt_card_and_line_violations() v
    UNION ALL
    SELECT 'workspace ' || c.workspace_id || ' connects imported calls where a read returned a demo call note'
      FROM connections c
     WHERE c.source = 'calls' AND c.mode = 'imported'
       AND EXISTS (SELECT 1 FROM receipts r, jsonb_array_elements_text(r.rows) AS row
                    WHERE r.workspace_id = c.workspace_id AND r.status = 'ok' AND row LIKE 'calls/%'
                      AND row !~ '^calls/call_notes/note-[0-9a-f]{64}$')
$$;
