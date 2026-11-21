-- Cards and receipts the Python package made, admitted into a workspace by the admit command.
--
-- They are kept apart from the cards and receipts made here. A Python read names no connection and binds
-- its units by tool name, so the rules the receipts and cards tables hold for this binary's writers would
-- refuse it. Each admitted card was verified against its seal and its reads before it was written, and is
-- verified again each time it is read.
--
-- An admitted receipt is one row of the Python audit log, as its export holds it: one JSON object, with
-- arguments, request_id and rows as JSON text. An admitted card is its bytes as the folder held them,
-- sealed by an admitted receipt that finished ok naming the bytes' digest. Neither is ever changed or
-- removed, so admitting a folder again adds nothing.

CREATE TABLE admitted_receipts (
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    audit_id uuid NOT NULL,
    row_text text NOT NULL CHECK (octet_length(row_text) <= 1048576),
    admitted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, audit_id),
    CHECK (json_typeof(row_text::json) = 'object' AND row_text::json ->> 'audit_id' = audit_id::text)
);
CREATE TRIGGER admitted_receipts_immutable BEFORE UPDATE OR DELETE ON admitted_receipts
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER admitted_receipts_no_truncate BEFORE TRUNCATE ON admitted_receipts
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- kind is cli for a card saved by brief or demo, and prepared for a brief saved by prepare_renewal_brief.
CREATE TABLE admitted_cards (
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    brief_id text NOT NULL CHECK (brief_id ~ '^brief-[0-9a-f]{32}$'),
    kind text NOT NULL CHECK (kind IN ('cli', 'prepared')),
    account_ref text NOT NULL CHECK (account_ref ~ '^acc-([0-9]{3}|sf-[A-Za-z0-9]{18})$'),
    card_bytes bytea NOT NULL,
    card_sha256 bytea NOT NULL CHECK (card_sha256 = sha256(card_bytes)),
    seal_audit_id uuid NOT NULL,
    admitted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, brief_id),
    FOREIGN KEY (workspace_id, seal_audit_id) REFERENCES admitted_receipts (workspace_id, audit_id)
);
CREATE TRIGGER admitted_cards_immutable BEFORE UPDATE OR DELETE ON admitted_cards
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER admitted_cards_no_truncate BEFORE TRUNCATE ON admitted_cards
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- What is wrong with an admitted card, or NULL: its seal finished ok naming its bytes' digest, as the tool
-- its kind is saved by, and no card made here in its workspace has its brief ID.
CREATE FUNCTION gtm_admitted_card_violation(workspace uuid, brief text) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    c admitted_cards%ROWTYPE;
    seal json;
    sealed boolean;
BEGIN
    SELECT * INTO c FROM admitted_cards WHERE workspace_id = workspace AND brief_id = brief;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    SELECT row_text::json INTO seal FROM admitted_receipts
     WHERE workspace_id = c.workspace_id AND audit_id = c.seal_audit_id;
    BEGIN
        sealed := seal ->> 'status' = 'ok'
            AND seal ->> 'tool' = CASE c.kind WHEN 'cli' THEN 'save_brief' ELSE 'prepare_renewal_brief' END
            AND (seal ->> 'rows')::jsonb = jsonb_build_array('briefs/' || c.brief_id || '/' || encode(c.card_sha256, 'hex'));
    EXCEPTION WHEN others THEN
        sealed := false;
    END;
    IF sealed IS NOT TRUE THEN
        RETURN 'admitted card ' || c.brief_id || ' has no finished seal naming its bytes';
    END IF;
    IF EXISTS (SELECT 1 FROM cards WHERE workspace_id = c.workspace_id AND brief_id = c.brief_id) THEN
        RETURN 'admitted card ' || c.brief_id || ' has the brief ID of a card made here';
    END IF;
    RETURN NULL;
END
$$;

CREATE FUNCTION gtm_admitted_cards_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text := gtm_admitted_card_violation(NEW.workspace_id, NEW.brief_id);
BEGIN
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT009';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER admitted_cards_checked AFTER INSERT ON admitted_cards
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_admitted_cards_check();

-- A restore checks the admitted cards too. It keeps every check gtm_integrity_violations held, under
-- another name, and adds this one. A later migration that replaces gtm_integrity_violations() must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_violations_before_admitted_cards;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_violations_before_admitted_cards() v
    UNION ALL
    SELECT v FROM (SELECT gtm_admitted_card_violation(workspace_id, brief_id) AS v FROM admitted_cards) x
     WHERE v IS NOT NULL
$$;
