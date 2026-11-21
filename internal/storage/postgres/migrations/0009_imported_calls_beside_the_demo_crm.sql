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
