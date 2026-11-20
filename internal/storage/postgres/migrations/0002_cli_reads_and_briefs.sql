-- Cards made by the Go card maker, under the rules every receipts.Store keeps (internal/receipts).
--
-- A source read is either a child of a pending top-level prepare_renewal_brief receipt, numbered from 1,
-- with that receipt's principal and token, or a top-level call of its own (the CLI's reads). Either way
-- it names its connection and is fenced by its generation. Only a source read claims record rows.
--
-- A card made here (kind go) is sealed by its own brief receipt: a save_brief receipt with no request ID,
-- whose arguments are exactly its account and brief (a card made by brief), or its prepare_renewal_brief
-- call (a prepared brief). Neither names a brief_id; the seal's row names the brief and the digest. The
-- card rests on exactly the reads its bytes embed in evidence.receipts, recorded in card_reads in order,
-- each embedded as stored and made for this brief by the seal's principal under the seal's token. Each
-- unit rests on one of them and on the record version it bound, which the bytes show as stored.
--
-- Operation IDs are unique per workspace. Receipts written from this migration on keep their arguments
-- exactly as given, key order included; earlier receipts keep jsonb's order.
--
-- 0001 allowed child reads and cards these rules refuse. A database holding any of them is not migrated:
-- this migration refuses it and names the rows.

DO $$
DECLARE
    found text;
BEGIN
    SELECT string_agg(problem, '; ') INTO found FROM (
        SELECT 'receipt ' || c.id || ' is a child read 0002 refuses' AS problem
          FROM receipts c JOIN receipts p ON p.id = c.parent_id
         WHERE p.parent_id IS NOT NULL OR p.tool <> 'prepare_renewal_brief' OR c.ordinal < 1
            OR c.connection_id IS NULL OR c.principal_id IS DISTINCT FROM p.principal_id
            OR c.token_id <> p.token_id
        UNION ALL
        SELECT 'card ' || id || ' has no recorded read set' FROM cards
        LIMIT 5) x;
    IF found IS NOT NULL THEN
        RAISE EXCEPTION 'migration 0002 refuses rows that 0001 accepted: %', found
            USING ERRCODE = 'GT018', HINT = 'Keep this database at schema 1, or start a new one.';
    END IF;
END
$$;

-- Arguments ---------------------------------------------------------------------------------------------

ALTER TABLE receipts ALTER COLUMN arguments TYPE json USING arguments::json;

-- Whether no object in a JSON value names one key twice; json, unlike jsonb, keeps both.
CREATE FUNCTION gtm_json_keys_unique(value json) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    item json;
BEGIN
    IF json_typeof(value) = 'object' THEN
        IF (SELECT count(*) <> count(DISTINCT k) FROM json_object_keys(value) k) THEN
            RETURN false;
        END IF;
        FOR item IN SELECT v FROM json_each(value) AS e(k, v) LOOP
            IF NOT gtm_json_keys_unique(item) THEN
                RETURN false;
            END IF;
        END LOOP;
    ELSIF json_typeof(value) = 'array' THEN
        FOR item IN SELECT json_array_elements(value) LOOP
            IF NOT gtm_json_keys_unique(item) THEN
                RETURN false;
            END IF;
        END LOOP;
    END IF;
    RETURN true;
END
$$;
ALTER TABLE receipts ADD CONSTRAINT receipts_arguments_keys_unique CHECK (gtm_json_keys_unique(arguments));

-- Operation IDs per workspace ---------------------------------------------------------------------------

ALTER TABLE receipts DROP CONSTRAINT receipts_operation_id_key;
ALTER TABLE receipts ADD CONSTRAINT receipts_operation_unique UNIQUE (workspace_id, operation_id);

-- Receipts ----------------------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION gtm_receipts_guard_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    current_generation bigint;
    connection_source text;
    parent receipts%ROWTYPE;
BEGIN
    IF NEW.status <> 'pending' OR NEW.finishing_lease IS NOT NULL THEN
        RAISE EXCEPTION 'a receipt starts pending' USING ERRCODE = 'GT011';
    END IF;
    IF NEW.parent_id IS NOT NULL THEN
        SELECT * INTO parent FROM receipts WHERE id = NEW.parent_id AND status = 'pending' FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'parent receipt % is not pending', NEW.parent_id USING ERRCODE = 'GT006';
        END IF;
        IF parent.parent_id IS NOT NULL OR parent.tool <> 'prepare_renewal_brief' THEN
            RAISE EXCEPTION 'receipt % is not a top-level prepare_renewal_brief call', NEW.parent_id
                USING ERRCODE = 'GT017';
        END IF;
        IF NEW.ordinal < 1 OR NEW.connection_id IS NULL OR NEW.principal_id IS DISTINCT FROM parent.principal_id
           OR NEW.token_id <> parent.token_id THEN
            RAISE EXCEPTION 'a child read is numbered from 1, names its connection, and has its parent''s token'
                USING ERRCODE = 'GT017';
        END IF;
    END IF;
    IF NEW.connection_id IS NOT NULL THEN
        SELECT generation, source INTO current_generation, connection_source
          FROM connections WHERE id = NEW.connection_id;
        IF NEW.scope IS DISTINCT FROM connection_source || ':read'
           OR NEW.connection_generation IS DISTINCT FROM current_generation THEN
            RAISE EXCEPTION 'a source read names its connection, that source''s scope and its generation'
                USING ERRCODE = 'GT012';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

-- Whether a receipt row names a source record.
CREATE FUNCTION gtm_record_rows(rows jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT EXISTS (SELECT 1 FROM jsonb_array_elements_text(rows) r WHERE r ~ '^(crm|calls|slack)/')
$$;

-- As in 0001, with the arguments compared as the text they were given, and record rows only on a source
-- read, which claims exactly the versions it bound.
CREATE OR REPLACE FUNCTION gtm_receipts_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    presented uuid := NEW.finishing_lease;
    current_generation bigint;
BEGIN
    NEW.finishing_lease := NULL;
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'receipt % is already finished', OLD.id USING ERRCODE = 'GT001';
    END IF;
    IF (NEW.id, NEW.workspace_id, NEW.operation_id, NEW.parent_id, NEW.ordinal, NEW.principal_id,
        NEW.token_id, NEW.tool, NEW.scope, NEW.requested_account, NEW.account_ref, NEW.connection_id,
        NEW.connection_generation, NEW.arguments::text, NEW.request_id, NEW.brief_id, NEW.started_at)
       IS DISTINCT FROM
       (OLD.id, OLD.workspace_id, OLD.operation_id, OLD.parent_id, OLD.ordinal, OLD.principal_id,
        OLD.token_id, OLD.tool, OLD.scope, OLD.requested_account, OLD.account_ref, OLD.connection_id,
        OLD.connection_generation, OLD.arguments::text, OLD.request_id, OLD.brief_id, OLD.started_at) THEN
        RAISE EXCEPTION 'a receipt''s request columns never change' USING ERRCODE = 'GT002';
    END IF;
    IF NEW.status = 'pending' THEN
        IF NEW.lease_token = OLD.lease_token THEN
            IF presented IS DISTINCT FROM OLD.lease_token OR NEW.lease_fence <> OLD.lease_fence
               OR OLD.lease_expires_at <= clock_timestamp() THEN
                RAISE EXCEPTION 'the lease on receipt % is not held', OLD.id USING ERRCODE = 'GT003';
            END IF;
        ELSIF OLD.lease_expires_at > clock_timestamp() OR NEW.lease_fence <= OLD.lease_fence THEN
            RAISE EXCEPTION 'the lease on receipt % is held', OLD.id USING ERRCODE = 'GT004';
        END IF;
        RETURN NEW;
    END IF;
    IF presented IS DISTINCT FROM OLD.lease_token
       OR (NEW.lease_token, NEW.lease_fence, NEW.lease_expires_at)
          IS DISTINCT FROM (OLD.lease_token, OLD.lease_fence, OLD.lease_expires_at)
       OR OLD.lease_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'the lease on receipt % is not held', OLD.id USING ERRCODE = 'GT003';
    END IF;
    IF EXISTS (SELECT 1 FROM receipts WHERE parent_id = OLD.id AND status = 'pending') THEN
        RAISE EXCEPTION 'receipt % has a pending child', OLD.id USING ERRCODE = 'GT005';
    END IF;
    IF OLD.connection_id IS NOT NULL THEN
        IF NEW.status = 'ok' THEN
            SELECT generation INTO current_generation FROM connections WHERE id = OLD.connection_id FOR SHARE;
            IF current_generation <> OLD.connection_generation THEN
                RAISE EXCEPTION 'the source changed after read % started', OLD.id USING ERRCODE = 'GT013';
            END IF;
            IF gtm_bound_rows(OLD.id) <> NEW.rows THEN
                RAISE EXCEPTION 'read % claims rows it did not bind', OLD.id USING ERRCODE = 'GT014';
            END IF;
        ELSIF EXISTS (SELECT 1 FROM receipt_records WHERE receipt_id = OLD.id) THEN
            RAISE EXCEPTION 'read % failed but bound records', OLD.id USING ERRCODE = 'GT014';
        END IF;
    ELSIF gtm_record_rows(NEW.rows) THEN
        RAISE EXCEPTION 'receipt % reads no source and claims a record row', OLD.id USING ERRCODE = 'GT014';
    END IF;
    RETURN NEW;
END
$$;

-- started_at as the audit log writes it: UTC, with microseconds only when there are any.
CREATE FUNCTION gtm_started_at_text(t timestamptz) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT to_char(t AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS')
        || CASE WHEN extract(microseconds FROM t) % 1000000 = 0 THEN ''
                ELSE to_char(t AT TIME ZONE 'UTC', '.US') END || 'Z'
$$;

-- A receipt as the audit log's readers return it, as a card embeds it.
CREATE FUNCTION gtm_receipt_json(r receipts) RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object('audit_id', (r).id::text, 'started_at', gtm_started_at_text((r).started_at),
        'token_id', (r).token_id, 'tool', (r).tool, 'arguments', (r).arguments::jsonb,
        'request_id', (r).request_id, 'brief_id', (r).brief_id, 'status', (r).status,
        'http_status', (r).http_status, 'row_count', (r).row_count, 'rows', (r).rows,
        'elapsed_ms', (r).elapsed_ms, 'error', (r).error)
$$;

-- A stored record body as JSON, or NULL if it is not JSON.
CREATE FUNCTION gtm_body_json(body bytea) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN convert_from(body, 'UTF8')::jsonb;
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

-- Cards -------------------------------------------------------------------------------------------------

-- A card made here is sealed by its own brief receipt, and one card rests on one brief receipt.
ALTER TABLE cards ADD COLUMN read_count integer NOT NULL DEFAULT 0 CHECK (read_count >= 0);
ALTER TABLE cards ALTER COLUMN read_count DROP DEFAULT;
ALTER TABLE cards ADD CONSTRAINT cards_go_sealed_by_its_brief CHECK (kind <> 'go' OR parent_receipt_id = seal_receipt_id);
ALTER TABLE cards ADD CONSTRAINT cards_parent_unique UNIQUE (parent_receipt_id);

-- The reads a card rests on: exactly those its bytes embed in evidence.receipts, in order.
CREATE TABLE card_reads (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    receipt_id uuid NOT NULL,
    PRIMARY KEY (card_id, ordinal),
    UNIQUE (card_id, receipt_id),
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id)
);
CREATE INDEX card_reads_receipt ON card_reads (receipt_id);
CREATE TRIGGER card_reads_immutable BEFORE UPDATE OR DELETE ON card_reads
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER card_reads_no_truncate BEFORE TRUNCATE ON card_reads
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- Whether a read is one of the card's brief: of its account, made for its brief through a connection, by
-- the seal's principal under the seal's token, and a top-level read under a save_brief seal or a child of a
-- prepare_renewal_brief seal.
CREATE FUNCTION gtm_read_of_card(rd receipts, c cards) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT coalesce((rd).brief_id = (c).brief_id AND (rd).account_ref = (c).account_ref
        AND (rd).connection_id IS NOT NULL AND (c).parent_receipt_id = (c).seal_receipt_id
        AND EXISTS (SELECT 1 FROM receipts s WHERE s.id = (c).seal_receipt_id
                       AND s.principal_id IS NOT DISTINCT FROM (rd).principal_id AND s.token_id = (rd).token_id
                       AND ((s.tool = 'save_brief' AND (rd).parent_id IS NULL)
                            OR (s.tool = 'prepare_renewal_brief' AND (rd).parent_id = s.id))), false)
$$;

-- The first record of the evidence with this reference, as the card's numbers resolve it.
CREATE FUNCTION gtm_record_shown(evidence jsonb, ref text) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT rec FROM jsonb_array_elements(evidence -> 'records') WITH ORDINALITY AS e(rec, i)
     WHERE rec ->> 'record_ref' = ref ORDER BY i LIMIT 1
$$;

-- What is wrong with a card, or NULL. Only cards made here (kind go) are held until admission of Python
-- cards exists. The seal is the card's own and finished ok naming the bytes' digest; the bytes name the
-- brief, the account and the seal; the units and gaps are exactly the bytes' own; the read set is exactly
-- evidence.receipts, each a finished read of this brief embedded as stored; and each unit rests on one of
-- those reads, finished ok under its tool, and on a version it bound that the bytes show as stored. At
-- publication every read's generation must also be current; a restore checks everything else.
CREATE OR REPLACE FUNCTION gtm_card_violation(card uuid, at_publication boolean) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    c cards%ROWTYPE;
    s receipts%ROWTYPE;
    doc jsonb;
    ev jsonb;
    bad text;
BEGIN
    SELECT * INTO c FROM cards WHERE id = card;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF c.kind <> 'go' THEN
        RETURN 'card ' || c.id || ' is of kind ' || c.kind || '; only cards made here are held';
    END IF;
    SELECT * INTO s FROM receipts WHERE id = c.seal_receipt_id;
    IF NOT (s.parent_id IS NULL AND s.status = 'ok' AND s.account_ref = c.account_ref AND s.brief_id IS NULL
            AND c.parent_receipt_id = s.id
            AND s.rows = jsonb_build_array('briefs/' || c.brief_id || '/' || encode(c.card_sha256, 'hex'))
            AND ((s.tool = 'save_brief' AND s.request_id = 'null'::jsonb
                  AND s.arguments::jsonb = jsonb_build_object('account_id', c.account_ref, 'brief_id', c.brief_id))
                 OR (s.tool = 'prepare_renewal_brief' AND s.arguments::jsonb ->> 'account_id' = c.account_ref))) THEN
        RETURN 'card ' || c.id || ' has no finished seal of its own naming its bytes';
    END IF;
    BEGIN
        doc := convert_from(c.card_bytes, 'UTF8')::jsonb;
    EXCEPTION WHEN others THEN
        RETURN 'card ' || c.id || ' is not a JSON document';
    END;
    ev := doc -> 'evidence';
    IF doc ->> 'brief_id' IS DISTINCT FROM c.brief_id OR doc ->> 'account_id' IS DISTINCT FROM c.account_ref
       OR ev ->> 'brief_id' IS DISTINCT FROM c.brief_id OR ev ->> 'account_id' IS DISTINCT FROM c.account_ref
       OR jsonb_typeof(ev -> 'receipts') IS DISTINCT FROM 'array'
       OR jsonb_typeof(ev -> 'records') IS DISTINCT FROM 'array'
       OR (CASE WHEN s.tool = 'save_brief'
                THEN doc ? 'kind' OR doc ->> 'sealed_by' IS DISTINCT FROM s.id::text
                ELSE doc ->> 'kind' IS DISTINCT FROM 'selected_brief'
                     OR doc -> 'prepared_by' ->> 'outer_audit_id' IS DISTINCT FROM s.id::text
                     OR doc -> 'prepared_by' ->> 'token_id' IS DISTINCT FROM s.token_id END) THEN
        RETURN 'card ' || c.id || ' is not its brief''s card under its seal';
    END IF;
    IF jsonb_typeof(doc -> 'units') IS DISTINCT FROM 'array' OR jsonb_typeof(doc -> 'gaps') IS DISTINCT FROM 'array'
       OR jsonb_array_length(doc -> 'units') <> c.unit_count OR jsonb_array_length(doc -> 'gaps') <> c.gap_count
       OR jsonb_array_length(ev -> 'receipts') <> c.read_count
       OR (SELECT count(*) FROM evidence_units WHERE card_id = c.id) <> c.unit_count
       OR (SELECT count(*) FROM gaps WHERE card_id = c.id) <> c.gap_count
       OR (SELECT count(*) FROM card_reads WHERE card_id = c.id) <> c.read_count THEN
        RETURN 'card ' || c.id || ' does not hold exactly its units, gaps and reads';
    END IF;
    SELECT 'unit ' || u.n INTO bad FROM evidence_units u
     WHERE u.card_id = c.id
       AND (u.n > c.unit_count OR u.body IS DISTINCT FROM doc -> 'units' -> (u.n - 1)
            OR u.body ->> 'kind' IS DISTINCT FROM u.kind OR u.body ->> 'record_ref' IS DISTINCT FROM u.record_ref
            OR u.body ->> 'tool' IS DISTINCT FROM u.tool
            OR (u.kind = 'sentence' AND u.body ->> 'shown' IS DISTINCT FROM u.shown))
     LIMIT 1;
    IF bad IS NULL THEN
        SELECT 'gap ' || g.code INTO bad FROM gaps g
         WHERE g.card_id = c.id
           AND (g.ordinal >= c.gap_count OR doc -> 'gaps' -> g.ordinal IS DISTINCT FROM jsonb_build_object(
                'code', g.code, 'disposition', g.disposition, 'count', g.count, 'by_role', g.by_role,
                'text', g.text))
         LIMIT 1;
    END IF;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' differs from the sealed bytes';
    END IF;
    SELECT 'read ' || cr.ordinal INTO bad FROM card_reads cr
      JOIN receipts r ON r.id = cr.receipt_id
      LEFT JOIN connections cn ON cn.id = r.connection_id
     WHERE cr.card_id = c.id
       AND (cr.ordinal >= c.read_count OR r.status = 'pending' OR NOT gtm_read_of_card(r, c)
            OR ev -> 'receipts' -> cr.ordinal IS DISTINCT FROM gtm_receipt_json(r)
            OR (at_publication AND r.connection_generation IS DISTINCT FROM cn.generation))
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' is not a read of its brief embedded as stored';
    END IF;
    SELECT 'unit ' || u.n INTO bad FROM evidence_units u
      LEFT JOIN receipts r ON r.id = u.receipt_id
      LEFT JOIN source_records v ON v.id = u.source_record_id
     WHERE u.card_id = c.id
       AND (u.receipt_id IS NULL OR u.source_record_id IS NULL
            OR NOT EXISTS (SELECT 1 FROM card_reads cr WHERE cr.card_id = c.id AND cr.receipt_id = u.receipt_id)
            OR r.status <> 'ok' OR r.tool <> u.tool
            OR v.source || '/' || v.record_table || '/' || v.record_id <> u.record_ref
            OR gtm_record_shown(ev, u.record_ref) IS DISTINCT FROM gtm_body_json(v.raw_body))
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' cites a read or record the card may not rest on';
    END IF;
    RETURN NULL;
END
$$;

-- As in 0001, and no read is added to a published card.
CREATE OR REPLACE FUNCTION gtm_cards_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    IF TG_TABLE_NAME = 'cards' THEN
        problem := gtm_card_violation(NEW.id, true);
    ELSIF TG_TABLE_NAME = 'evidence_units' THEN
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.unit_count = (SELECT count(*) FROM evidence_units WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a unit was added to published card ' || NEW.card_id;
        END IF;
    ELSIF TG_TABLE_NAME = 'card_reads' THEN
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.read_count = (SELECT count(*) FROM card_reads WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a read was added to published card ' || NEW.card_id;
        END IF;
    ELSE
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.gap_count = (SELECT count(*) FROM gaps WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a gap was added to published card ' || NEW.card_id;
        END IF;
    END IF;
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT009';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER card_reads_counted AFTER INSERT ON card_reads
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_cards_check();

-- As in 0001, with top-level source reads allowed, each child checked against its parent, and record rows
-- only on source reads.
CREATE OR REPLACE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
        SELECT 'receipt ' || c.id || ' is pending under a finished parent'
          FROM receipts c JOIN receipts p ON p.id = c.parent_id
         WHERE c.status = 'pending' AND p.status <> 'pending';
    RETURN QUERY
        SELECT 'receipt ' || c.id || ' is not a numbered source read of a prepare_renewal_brief call'
          FROM receipts c JOIN receipts p ON p.id = c.parent_id
         WHERE p.parent_id IS NOT NULL OR p.tool <> 'prepare_renewal_brief' OR c.ordinal < 1
            OR c.connection_id IS NULL OR c.principal_id IS DISTINCT FROM p.principal_id
            OR c.token_id <> p.token_id;
    RETURN QUERY
        SELECT 'receipt ' || r.id || ' does not match its connection or its bound records'
          FROM receipts r JOIN connections c ON c.id = r.connection_id
         WHERE r.scope IS DISTINCT FROM c.source || ':read'
            OR (r.status = 'ok' AND gtm_bound_rows(r.id) <> r.rows)
            OR (r.status <> 'ok' AND EXISTS (SELECT 1 FROM receipt_records WHERE receipt_id = r.id));
    RETURN QUERY
        SELECT 'receipt ' || r.id || ' reads no source and claims a record row'
          FROM receipts r WHERE r.connection_id IS NULL AND gtm_record_rows(r.rows);
    RETURN QUERY
        SELECT 'binding ' || b.receipt_id || '/' || b.ordinal || ' is out of order or of another source'
          FROM receipt_records b
          JOIN receipts r ON r.id = b.receipt_id
          JOIN source_records v ON v.id = b.source_record_id
          LEFT JOIN connections c ON c.id = r.connection_id
         WHERE c.id IS NULL OR v.source <> c.source
            OR b.ordinal >= (SELECT count(*) FROM receipt_records x WHERE x.receipt_id = b.receipt_id);
    RETURN QUERY
        SELECT 'delivery of pending receipt ' || d.receipt_id
          FROM receipt_deliveries d JOIN receipts r ON r.id = d.receipt_id WHERE r.status = 'pending';
    RETURN QUERY
        SELECT v FROM (SELECT gtm_card_violation(id, false) AS v FROM cards) x WHERE v IS NOT NULL;
END
$$;
