-- Every rule of the database reads the tables of its own schema.
--
-- A rule here is a function: a guard a trigger runs, or a check a restore runs. Each named its tables bare, as
-- receipts or deals, and PostgreSQL looks a bare name up along the session's search path, where a session's
-- temporary tables come first. A writer who may make temporary tables could make one named receipts, put a
-- row in it, and have a guard read that row in the real one's place, while the foreign key still pointed at
-- a real receipt. So each function that can read a table now carries its own path: the system's catalog, its
-- own schema, and temporary tables last.
--
-- A function declared immutable keeps the caller's path, which lets it be folded into a constraint or an
-- index as before. That it reads no table is this project's rule for an immutable function, not something
-- PostgreSQL holds it to. This sweep runs once: a migration that makes or replaces a function later sets the
-- path itself, and the storage tests refuse one that does not.
DO $$
DECLARE
    rule record;
BEGIN
    FOR rule IN
        SELECT p.oid::regprocedure AS signature, n.nspname AS schema
          FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
         WHERE n.nspname = current_schema() AND p.proname LIKE 'gtm\_%' AND p.provolatile <> 'i'
         ORDER BY p.proname, p.oid
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I, pg_temp', rule.signature, rule.schema);
    END LOOP;
END
$$;
