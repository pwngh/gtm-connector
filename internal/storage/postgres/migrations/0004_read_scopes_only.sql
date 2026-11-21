-- The connector is read-only: a token carries read scopes only, and a receipt names a read scope or none.
--
-- 0001 let a machine token and a receipt hold crm:write. The gate refuses any token that carries a scope
-- outside crm:read, calls:read and slack:read, so the store no longer holds one.
--
-- A database holding a token or a receipt with crm:write is not migrated: this migration refuses it and
-- names the rows. Nothing is deleted or rewritten.

DO $$
DECLARE
    found text;
BEGIN
    SELECT string_agg(problem, '; ') INTO found FROM (
        SELECT 'machine token ' || id || ' carries a scope that is not a read scope' AS problem
          FROM machine_tokens
         WHERE NOT scopes <@ ARRAY['crm:read', 'calls:read', 'slack:read']::text[]
        UNION ALL
        SELECT 'receipt ' || id || ' names the scope crm:write' FROM receipts WHERE scope = 'crm:write'
        LIMIT 5) x;
    IF found IS NOT NULL THEN
        RAISE EXCEPTION 'the read-scopes migration refuses rows that 0001 accepted: %', found
            USING ERRCODE = 'GT018', HINT = 'Keep this database at its schema, or start a new one.';
    END IF;
END
$$;

ALTER TABLE machine_tokens DROP CONSTRAINT machine_tokens_scopes_check;
ALTER TABLE machine_tokens ADD CONSTRAINT machine_tokens_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['crm:read', 'calls:read', 'slack:read']::text[]);

ALTER TABLE receipts DROP CONSTRAINT receipts_scope_check;
ALTER TABLE receipts ADD CONSTRAINT receipts_scope_check CHECK (scope IN ('crm:read', 'calls:read', 'slack:read'));
