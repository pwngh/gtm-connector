-- The checks of 0001 and 0002 on a card's units, gaps and reads and on a read's bindings, in time that grows
-- with the rows rather than with their square. The rules are the same.

-- As in 0002. At commit every unit, gap and read inserted in the transaction fires this trigger, and a
-- card's rows of one kind are all checked against the same rows, so only the card's highest-numbered row
-- counts them. A row added later to a published card numbers above every row it has, so it is counted.
CREATE OR REPLACE FUNCTION gtm_cards_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    IF TG_TABLE_NAME = 'cards' THEN
        problem := gtm_card_violation(NEW.id, true);
    ELSIF TG_TABLE_NAME = 'evidence_units' THEN
        IF NEW.n = (SELECT max(n) FROM evidence_units WHERE card_id = NEW.card_id) THEN
            PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
               AND c.unit_count = (SELECT count(*) FROM evidence_units WHERE card_id = c.id);
            IF NOT FOUND THEN
                problem := 'a unit was added to published card ' || NEW.card_id;
            END IF;
        END IF;
    ELSIF TG_TABLE_NAME = 'card_reads' THEN
        IF NEW.ordinal = (SELECT max(ordinal) FROM card_reads WHERE card_id = NEW.card_id) THEN
            PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
               AND c.read_count = (SELECT count(*) FROM card_reads WHERE card_id = c.id);
            IF NOT FOUND THEN
                problem := 'a read was added to published card ' || NEW.card_id;
            END IF;
        END IF;
    ELSIF NEW.ordinal = (SELECT max(ordinal) FROM gaps WHERE card_id = NEW.card_id) THEN
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

-- As in 0002, reading the bytes' units and records once. Each unit is compared with the bytes' unit of its
-- number, and each distinct citation (read, record version, reference and tool) is checked once, against
-- the first record of the evidence with its reference.
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
      LEFT JOIN jsonb_array_elements(doc -> 'units') WITH ORDINALITY AS d(body, i) ON d.i = u.n
     WHERE u.card_id = c.id
       AND (u.n > c.unit_count OR u.body IS DISTINCT FROM d.body
            OR u.body ->> 'kind' IS DISTINCT FROM u.kind OR u.body ->> 'record_ref' IS DISTINCT FROM u.record_ref
            OR u.body ->> 'tool' IS DISTINCT FROM u.tool
            OR (u.kind = 'sentence' AND u.body ->> 'shown' IS DISTINCT FROM u.shown))
     ORDER BY u.n
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
    WITH cited AS (
        SELECT u.receipt_id, u.source_record_id, u.record_ref, u.tool, min(u.n) AS n FROM evidence_units u
         WHERE u.card_id = c.id GROUP BY u.receipt_id, u.source_record_id, u.record_ref, u.tool),
    shown AS (
        SELECT DISTINCT ON (e.rec ->> 'record_ref') e.rec ->> 'record_ref' AS ref, e.rec
          FROM jsonb_array_elements(ev -> 'records') WITH ORDINALITY AS e(rec, i)
         ORDER BY e.rec ->> 'record_ref', e.i)
    SELECT 'unit ' || k.n INTO bad FROM cited k
      LEFT JOIN receipts r ON r.id = k.receipt_id
      LEFT JOIN source_records v ON v.id = k.source_record_id
      LEFT JOIN shown sh ON sh.ref = k.record_ref
     WHERE k.receipt_id IS NULL OR k.source_record_id IS NULL
        OR NOT EXISTS (SELECT 1 FROM card_reads cr WHERE cr.card_id = c.id AND cr.receipt_id = k.receipt_id)
        OR r.status <> 'ok' OR r.tool <> k.tool
        OR v.source || '/' || v.record_table || '/' || v.record_id <> k.record_ref
        OR sh.rec IS DISTINCT FROM gtm_body_json(v.raw_body)
     ORDER BY k.n
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' cites a read or record the card may not rest on';
    END IF;
    RETURN NULL;
END
$$;

DROP FUNCTION gtm_record_shown(jsonb, text);

-- As in 0001, with the next binding's number found from the highest bound so far. A read's bindings are
-- numbered from 0 in order and never change, so the highest number plus one is their count.
CREATE OR REPLACE FUNCTION gtm_receipt_records_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    read_connection uuid;
BEGIN
    SELECT connection_id INTO read_connection FROM receipts
     WHERE id = NEW.receipt_id AND workspace_id = NEW.workspace_id AND status = 'pending' FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'receipt % is not pending', NEW.receipt_id USING ERRCODE = 'GT007';
    END IF;
    IF read_connection IS NULL OR NOT EXISTS (
        SELECT 1 FROM source_records v JOIN connections c ON c.id = read_connection
         WHERE v.id = NEW.source_record_id AND v.source = c.source) THEN
        RAISE EXCEPTION 'receipt % reads no source of record %', NEW.receipt_id, NEW.source_record_id
            USING ERRCODE = 'GT015';
    END IF;
    IF NEW.ordinal <> coalesce((SELECT max(ordinal) + 1 FROM receipt_records WHERE receipt_id = NEW.receipt_id), 0) THEN
        RAISE EXCEPTION 'bindings of receipt % are numbered in order', NEW.receipt_id USING ERRCODE = 'GT015';
    END IF;
    RETURN NEW;
END
$$;

-- As in 0002, with each read's bindings counted once.
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
          FROM (SELECT x.*, count(*) OVER (PARTITION BY x.receipt_id) AS bound FROM receipt_records x) b
          JOIN receipts r ON r.id = b.receipt_id
          JOIN source_records v ON v.id = b.source_record_id
          LEFT JOIN connections c ON c.id = r.connection_id
         WHERE c.id IS NULL OR v.source <> c.source OR b.ordinal >= b.bound;
    RETURN QUERY
        SELECT 'delivery of pending receipt ' || d.receipt_id
          FROM receipt_deliveries d JOIN receipts r ON r.id = d.receipt_id WHERE r.status = 'pending';
    RETURN QUERY
        SELECT v FROM (SELECT gtm_card_violation(id, false) AS v FROM cards) x WHERE v IS NOT NULL;
END
$$;
