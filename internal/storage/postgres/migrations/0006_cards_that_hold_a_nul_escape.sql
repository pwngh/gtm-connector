-- A NUL character in source text stays in a card's stored evidence, and in the record's stored body, as the
-- JSON escape \u0000. PostgreSQL's jsonb cannot hold that escape, so the checks of 0003 refused such a card
-- as not a JSON document and the account got no card. The bytes are stored and sealed as before. Only the
-- checks read them differently: as jsonb in which a stand-in character takes the place of each such escape.
-- A stand-in is a character outside ASCII, so it is named only where a document holds the escape: a database
-- whose encoding cannot hold it checks every other card as before.

-- Whether a JSON document holds the escape \u0000. Each escaped backslash is set aside first, so the six
-- characters after one are not taken for the escape. Bytes that are not UTF-8 hold no escape; the cast
-- refuses them.
CREATE FUNCTION gtm_holds_nul_escape(document bytea) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN position(chr(92) || 'u0000' IN replace(convert_from(document, 'UTF8'), chr(92) || chr(92), '')) > 0;
EXCEPTION WHEN others THEN
    RETURN false;
END
$$;

-- A JSON document as jsonb, with stand_in, or U+FFFD when it is NULL, in place of each \u0000 escape. A
-- document without that escape is cast as it is, and the stand-in is not looked at. In any other, each
-- escaped backslash is set aside first, so that every \u0000 left is the escape and never the six characters
-- after an escaped backslash; U+0001, which no JSON document holds unescaped, keeps the place of each. What
-- is not JSON is still refused by the cast: the stand-in is a character outside ASCII, which is JSON only
-- inside a string, and nothing else is changed.
CREATE FUNCTION gtm_jsonb_without_nul(document bytea, stand_in text DEFAULT NULL) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    doc text := convert_from(document, 'UTF8');
    backslash constant text := chr(92);
BEGIN
    IF NOT gtm_holds_nul_escape(document) OR position(chr(1) IN doc) > 0 THEN
        RETURN doc::jsonb;
    END IF;
    RETURN replace(replace(replace(doc, backslash || backslash, chr(1)), backslash || 'u0000',
                           coalesce(stand_in, chr(65533))),
                   chr(1), backslash || backslash)::jsonb;
END
$$;

-- Whether a member name anywhere in a JSON value holds stand_in. jsonb keeps the last of two members of one
-- name, so in a document that held the escape, a member whose name held it, or held the stand-in, can be
-- overwritten by another and leave no trace in the comparisons. Such a document is refused instead.
CREATE FUNCTION gtm_names_hold(document jsonb, stand_in text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    WITH RECURSIVE v(value) AS (
        SELECT document
        UNION ALL
        SELECT e.value FROM v, LATERAL (
            SELECT o.value FROM jsonb_each(CASE WHEN jsonb_typeof(v.value) = 'object' THEN v.value END) o
            UNION ALL
            SELECT a.value FROM jsonb_array_elements(CASE WHEN jsonb_typeof(v.value) = 'array' THEN v.value END) a
        ) e)
    SELECT EXISTS (
        SELECT 1 FROM v, jsonb_object_keys(CASE WHEN jsonb_typeof(v.value) = 'object' THEN v.value END) k
         WHERE position(stand_in IN k) > 0)
$$;

-- A stored record body in that form, or NULL if it is not JSON.
DROP FUNCTION gtm_body_json(bytea);
CREATE FUNCTION gtm_body_json(body bytea, stand_in text) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN gtm_jsonb_without_nul(body, stand_in);
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

-- The rules of 0003, with the card's bytes and each cited record's body read in that form.
CREATE FUNCTION gtm_card_violation_as(card uuid, at_publication boolean, stand_in text) RETURNS text
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
        doc := gtm_jsonb_without_nul(c.card_bytes, stand_in);
    EXCEPTION WHEN others THEN
        RETURN 'card ' || c.id || ' is not a JSON document';
    END;
    IF gtm_holds_nul_escape(c.card_bytes) AND gtm_names_hold(doc, stand_in) THEN
        RETURN 'card ' || c.id || ' holds the escape \u0000 and a member name that holds it or its stand-in';
    END IF;
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
        OR sh.rec IS DISTINCT FROM gtm_body_json(v.raw_body, stand_in)
        OR (gtm_holds_nul_escape(v.raw_body) AND gtm_names_hold(gtm_body_json(v.raw_body, stand_in), stand_in))
     ORDER BY k.n
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' cites a read or record the card may not rest on';
    END IF;
    RETURN NULL;
END
$$;

-- What is wrong with a card, or NULL, under the rules of 0003 held exactly. Read with one stand-in alone,
-- bytes that hold the escape would pass for rows that hold that stand-in in its place, and a record shown
-- with the stand-in would pass for a stored body that holds the escape. Two values that are equal under two
-- different stand-ins are equal, so a card whose bytes or whose cited records hold the escape is checked
-- under both. Every other card is checked once, as before, with no stand-in named.
CREATE OR REPLACE FUNCTION gtm_card_violation(card uuid, at_publication boolean) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    problem text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM cards c WHERE c.id = card AND gtm_holds_nul_escape(c.card_bytes))
       AND NOT EXISTS (SELECT 1 FROM evidence_units u JOIN source_records v ON v.id = u.source_record_id
                        WHERE u.card_id = card AND gtm_holds_nul_escape(v.raw_body)) THEN
        RETURN gtm_card_violation_as(card, at_publication, NULL);
    END IF;
    problem := gtm_card_violation_as(card, at_publication, chr(65533));
    IF problem IS NULL THEN
        problem := gtm_card_violation_as(card, at_publication, chr(9216));
    END IF;
    RETURN problem;
END
$$;
