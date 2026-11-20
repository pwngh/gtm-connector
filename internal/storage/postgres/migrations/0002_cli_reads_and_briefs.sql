-- Cards made by the CLI and by prepare_renewal_brief, under one set of rules.
--
-- A source read is either a child of a pending top-level
-- prepare_renewal_brief receipt, numbered from 1, or a top-level call of its
-- own (the CLI's reads), and is fenced by its connection's generation either
-- way. A card rests on the children of its brief receipt, or, when the seal
-- is its brief receipt, on top-level reads of its brief by the seal's
-- principal and token. A seal names its brief in its row; its brief_id
-- column may be empty, as the CLI's save_brief leaves it. A receipt keeps its
-- arguments exactly as the call gave them, key order included.

ALTER TABLE receipts ALTER COLUMN arguments TYPE json USING arguments::json;

CREATE OR REPLACE FUNCTION gtm_receipts_guard_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    current_generation bigint;
    connection_source text;
BEGIN
    IF NEW.status <> 'pending' OR NEW.finishing_lease IS NOT NULL THEN
        RAISE EXCEPTION 'a receipt starts pending' USING ERRCODE = 'GT011';
    END IF;
    IF NEW.parent_id IS NOT NULL THEN
        PERFORM 1 FROM receipts WHERE id = NEW.parent_id AND status = 'pending' FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'parent receipt % is not pending', NEW.parent_id USING ERRCODE = 'GT006';
        END IF;
        PERFORM 1 FROM receipts WHERE id = NEW.parent_id AND parent_id IS NULL AND tool = 'prepare_renewal_brief';
        IF NOT FOUND THEN
            RAISE EXCEPTION 'receipt % is not a top-level prepare_renewal_brief call', NEW.parent_id
                USING ERRCODE = 'GT017';
        END IF;
        IF NEW.ordinal < 1 OR NEW.connection_id IS NULL THEN
            RAISE EXCEPTION 'a child read is numbered from 1 and names its connection' USING ERRCODE = 'GT017';
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

-- As in 0001, with the arguments compared as the text they were given.
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
    END IF;
    RETURN NEW;
END
$$;

-- Whether a read may be cited by a card: a child of the card's brief
-- receipt, or, when the seal is the brief receipt, a top-level read of the
-- card's brief by the seal's principal and token.
CREATE FUNCTION gtm_read_of_card(rd receipts, c cards) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT (rd).parent_id = (c).parent_receipt_id
        OR ((rd).parent_id IS NULL AND (c).parent_receipt_id = (c).seal_receipt_id
            AND (rd).brief_id = (c).brief_id
            AND EXISTS (SELECT 1 FROM receipts s WHERE s.id = (c).seal_receipt_id
                           AND s.principal_id IS NOT DISTINCT FROM (rd).principal_id
                           AND s.token_id = (rd).token_id))
$$;

-- As in 0001, with the seal naming its brief by its row and the reads
-- checked by gtm_read_of_card.
CREATE OR REPLACE FUNCTION gtm_card_violation(card uuid, at_publication boolean) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    c cards%ROWTYPE;
    doc jsonb;
    bad text;
BEGIN
    SELECT * INTO c FROM cards WHERE id = card;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    PERFORM 1 FROM receipts r
     WHERE r.id = c.seal_receipt_id AND r.parent_id IS NULL AND r.status = 'ok'
       AND r.account_ref = c.account_ref AND (r.brief_id IS NULL OR r.brief_id = c.brief_id)
       AND r.rows = jsonb_build_array('briefs/' || c.brief_id || '/' || encode(c.card_sha256, 'hex'));
    IF NOT FOUND THEN
        RETURN 'card ' || c.id || ' has no finished seal naming its bytes';
    END IF;
    PERFORM 1 FROM receipts r WHERE r.id = c.parent_receipt_id AND r.account_ref = c.account_ref
       AND r.parent_id IS NULL;
    IF NOT FOUND THEN
        RETURN 'card ' || c.id || ' names a brief receipt of another account';
    END IF;
    BEGIN
        doc := convert_from(c.card_bytes, 'UTF8')::jsonb;
    EXCEPTION WHEN others THEN
        RETURN 'card ' || c.id || ' is not a JSON document';
    END;
    IF jsonb_typeof(doc -> 'units') IS DISTINCT FROM 'array' OR jsonb_typeof(doc -> 'gaps') IS DISTINCT FROM 'array'
       OR jsonb_array_length(doc -> 'units') <> c.unit_count OR jsonb_array_length(doc -> 'gaps') <> c.gap_count
       OR (SELECT count(*) FROM evidence_units WHERE card_id = c.id) <> c.unit_count
       OR (SELECT count(*) FROM gaps WHERE card_id = c.id) <> c.gap_count THEN
        RETURN 'card ' || c.id || ' does not hold exactly its units and gaps';
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
    SELECT 'unit ' || u.n INTO bad FROM evidence_units u
      LEFT JOIN receipts r ON r.id = u.receipt_id
      LEFT JOIN connections cn ON cn.id = r.connection_id
      LEFT JOIN source_records v ON v.id = u.source_record_id
     WHERE u.card_id = c.id
       AND ((c.kind = 'go' AND u.source_record_id IS NULL)
            OR (u.receipt_id IS NOT NULL
                AND (r.status <> 'ok' OR r.account_ref IS DISTINCT FROM c.account_ref
                     OR NOT coalesce(gtm_read_of_card(r, c), false) OR r.connection_id IS NULL
                     OR (at_publication AND r.connection_generation <> cn.generation)))
            OR (u.source_record_id IS NOT NULL
                AND v.source || '/' || v.record_table || '/' || v.record_id <> u.record_ref))
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' cites a read the card may not rest on';
    END IF;
    RETURN NULL;
END
$$;

-- As in 0001, with top-level source reads allowed and each child checked
-- against its parent.
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
            OR c.connection_id IS NULL;
    RETURN QUERY
        SELECT 'receipt ' || r.id || ' does not match its connection or its bound records'
          FROM receipts r JOIN connections c ON c.id = r.connection_id
         WHERE r.scope IS DISTINCT FROM c.source || ':read'
            OR (r.status = 'ok' AND gtm_bound_rows(r.id) <> r.rows)
            OR (r.status <> 'ok' AND EXISTS (SELECT 1 FROM receipt_records WHERE receipt_id = r.id));
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
