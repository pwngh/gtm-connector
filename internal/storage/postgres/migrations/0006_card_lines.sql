-- The lines a card prints under ATTENTION and RECORD VALUES, as data: a
-- projection of the sealed text, written in the transaction that publishes
-- the card and checked against the text's own lines at commit. The sealed
-- bytes and their digest are untouched.
--
-- line_count is how many lines a card holds. Only a card published before
-- this migration ran may have a null count, and it has no lines; every card
-- published since counts its lines, and holds at least one.
ALTER TABLE cards ADD COLUMN line_count integer CHECK (line_count >= 0);

-- The cards published before this migration ran, the only cards that may
-- have no line count. The migration fills it once, and no row is added,
-- changed or removed afterwards, so a card published since cannot be made to
-- look as if it came before. A restore loads its rows as it loads the cards'.
CREATE TABLE cards_before_lines (
    workspace_id uuid NOT NULL,
    card_id uuid PRIMARY KEY,
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id)
);
INSERT INTO cards_before_lines (workspace_id, card_id) SELECT workspace_id, id FROM cards;
CREATE FUNCTION gtm_cards_before_lines_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the cards published before lines were kept were fixed when lines began'
        USING ERRCODE = 'GT009';
END
$$;
CREATE TRIGGER cards_before_lines_fixed BEFORE INSERT OR UPDATE OR DELETE ON cards_before_lines
    FOR EACH ROW EXECUTE FUNCTION gtm_cards_before_lines_fixed();
CREATE TRIGGER cards_before_lines_no_truncate BEFORE TRUNCATE ON cards_before_lines
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A line's body is the line as the API serves it: section, code, label,
-- text, cites, more, and the countdown, amount or contact it carries.
CREATE TABLE card_lines (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    section text NOT NULL CHECK (section IN ('signal', 'value')),
    body jsonb NOT NULL CHECK (jsonb_typeof(body) = 'object' AND body ->> 'section' = section),
    PRIMARY KEY (card_id, ordinal),
    CHECK (body ->> 'code' IN ('renewal_date', 'contract_end', 'arr', 'crm_risk', 'account', 'opportunity',
        'contract', 'contact', 'crm_at_risk', 'contract_ended', 'renewal_passed', 'still_open',
        'close_after_renewal', 'contact_no_role', 'call_stale')),
    CHECK (CASE WHEN jsonb_typeof(body -> 'cites') = 'array' THEN jsonb_array_length(body -> 'cites') > 0
                ELSE false END),
    CHECK (jsonb_typeof(body -> 'more') = 'number' AND (section = 'signal' OR body -> 'more' = '0'::jsonb)),
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id)
);
CREATE TRIGGER card_lines_immutable BEFORE UPDATE OR DELETE ON card_lines
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER card_lines_no_truncate BEFORE TRUNCATE ON card_lines
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A line as the card's text prints it: a value as its label, its text and
-- the numbers it cites; a signal as its text, the numbers it cites and, when
-- it counts more than it cites, "and N more".
CREATE FUNCTION gtm_line_text(line jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT '  '
        || CASE WHEN line ->> 'section' = 'value' THEN (line ->> 'label') || ': ' ELSE '' END
        || (line ->> 'text') || ' '
        || (SELECT coalesce(string_agg('[' || n || ']', '' ORDER BY i), '')
              FROM jsonb_array_elements_text(line -> 'cites') WITH ORDINALITY AS c(n, i))
        || CASE WHEN line ->> 'section' = 'signal' AND (line ->> 'more')::numeric > 0
                THEN ' and ' || (line ->> 'more') || ' more' ELSE '' END
$$;

-- A card's bytes as a JSON document. PostgreSQL reads no JSON string that
-- holds U+0000, which a record in the card's evidence may, so each such
-- escape, one that no backslash of its own escapes, is read as U+FFFD; a
-- text that held one would then differ from its lines.
CREATE FUNCTION gtm_card_document(card_bytes bytea) RETURNS json LANGUAGE sql IMMUTABLE AS $$
    SELECT regexp_replace(convert_from(card_bytes, 'UTF8'), '(?<![\\])((?:[\\][\\])*)[\\]u0000', '\1\\ufffd',
        'g')::json
$$;

-- What the text of a card prints under a heading: the lines after its first
-- occurrence, up to the first empty line.
CREATE FUNCTION gtm_printed_lines(printed text[], heading text) RETURNS text[] LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    at integer := array_position(printed, heading);
    out text[] := '{}';
BEGIN
    IF at IS NULL THEN
        RETURN out;
    END IF;
    at := at + 1;
    WHILE at <= coalesce(array_length(printed, 1), 0) AND printed[at] <> '' LOOP
        out := out || printed[at];
        at := at + 1;
    END LOOP;
    RETURN out;
END
$$;

-- "1 day" or "N days", as a card prints a count of days.
CREATE FUNCTION gtm_days_text(n integer) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN abs(n) = 1 THEN '1 day' ELSE abs(n) || ' days' END
$$;

-- A field of a contact line as the card prints it: quoted as JSON, or
-- Missing when it is null.
CREATE FUNCTION gtm_printed_field(value jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN value IS NULL OR jsonb_typeof(value) = 'null' THEN 'Missing' ELSE to_json(value #>> '{}')::text END
$$;

-- Whether a line carries exactly the facts its code implies, and they are
-- what its own text prints: a renewal date line, and a contract end line
-- that names a contract, carry a countdown whose date and days, counted from
-- the card's as_of day, open their text; the annual recurring revenue line
-- carries an amount and currency that are its text; a contact line carries
-- a name and title that open its text. No other line carries a fact. A value
-- has its code's label and a signal has none. A line it cannot read is one
-- whose facts do not hold; its caller reads NULL as false.
CREATE FUNCTION gtm_line_facts_hold(line jsonb, as_of text) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    code text := line ->> 'code';
    printed text := line ->> 'text';
    countdown jsonb := line -> 'countdown';
    amount jsonb := line -> 'amount';
    contact jsonb := line -> 'contact';
    dated boolean := coalesce(code = 'renewal_date' OR code = 'contract_end' AND printed NOT LIKE 'Missing: %', false);
    label jsonb;
    counts_to text;
    counted text;
    figure text;
BEGIN
    label := (CASE code
        WHEN 'renewal_date' THEN '"Renewal date"' WHEN 'contract_end' THEN '"Contract end"'
        WHEN 'arr' THEN '"Annual recurring revenue"' WHEN 'crm_risk' THEN '"CRM risk"'
        WHEN 'account' THEN '"Account"' WHEN 'opportunity' THEN '"Opportunity"'
        WHEN 'contract' THEN '"Contract"' WHEN 'contact' THEN '"Contact"' ELSE 'null' END)::jsonb;
    counts_to := (CASE code WHEN 'renewal_date' THEN 'renewal' ELSE 'contract_end' END);
    IF (line ->> 'section' = 'signal') <> (code IN ('crm_at_risk', 'contract_ended', 'renewal_passed', 'still_open',
            'close_after_renewal', 'contact_no_role', 'call_stale'))
       OR (line -> 'label') IS DISTINCT FROM label
       OR coalesce(jsonb_typeof(countdown) = 'object', false) <> dated
       OR coalesce(jsonb_typeof(amount) = 'object', false) <> (code = 'arr')
       OR coalesce(jsonb_typeof(contact) = 'object', false) <> (code = 'contact')
       OR (jsonb_typeof(countdown) IS DISTINCT FROM 'object' AND countdown IS DISTINCT FROM 'null'::jsonb)
       OR (jsonb_typeof(amount) IS DISTINCT FROM 'object' AND amount IS DISTINCT FROM 'null'::jsonb)
       OR (jsonb_typeof(contact) IS DISTINCT FROM 'object' AND contact IS DISTINCT FROM 'null'::jsonb) THEN
        RETURN false;
    END IF;
    IF code = 'contract_end' AND NOT dated THEN
        RETURN printed = 'Missing: no contract ends on or after ' || as_of;
    END IF;
    IF dated THEN
        IF countdown ->> 'date' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR jsonb_typeof(countdown -> 'days') <> 'number'
           OR countdown ->> 'counts_to' IS DISTINCT FROM counts_to
           OR (countdown ->> 'date')::date - as_of::date IS DISTINCT FROM (countdown ->> 'days')::integer THEN
            RETURN false;
        END IF;
        counted := (countdown ->> 'date') || ', ' || CASE
            WHEN (countdown ->> 'days')::integer > 0 THEN 'in ' || gtm_days_text((countdown ->> 'days')::integer)
            WHEN (countdown ->> 'days')::integer < 0 THEN gtm_days_text((countdown ->> 'days')::integer) || ' ago'
            ELSE 'today' END;
        RETURN CASE code WHEN 'renewal_date' THEN printed = counted
                         ELSE starts_with(printed, counted || '; status ') END;
    END IF;
    IF code = 'arr' THEN
        figure := amount ->> 'amount';
        IF figure !~ '^(0|[1-9][0-9]*)\.[0-9]{2}$' OR jsonb_typeof(amount -> 'amount') <> 'string'
           OR jsonb_typeof(amount -> 'currency') NOT IN ('null', 'string')
           OR amount ->> 'currency' !~ '^[A-Z]{3}$' THEN
            RETURN false;
        END IF;
        figure := regexp_replace(split_part(figure, '.', 1), '([0-9])(?=([0-9]{3})+$)', '\1,', 'g') || '.'
            || split_part(figure, '.', 2);
        RETURN printed = CASE WHEN amount ->> 'currency' IS NULL THEN figure || ' (currency Missing)'
                              ELSE (amount ->> 'currency') || ' ' || figure END;
    END IF;
    IF code = 'contact' THEN
        IF jsonb_typeof(contact -> 'name') NOT IN ('null', 'string')
           OR jsonb_typeof(contact -> 'title') NOT IN ('null', 'string') THEN
            RETURN false;
        END IF;
        RETURN starts_with(printed, gtm_printed_field(contact -> 'name') || '; role '
            || gtm_printed_field(contact -> 'title') || '; email ');
    END IF;
    RETURN true;
EXCEPTION WHEN others THEN
    RETURN false;
END
$$;

-- What is wrong with a card's lines, or NULL. A card with a null count is one
-- published before this migration ran, published before every card that
-- counts its lines, and holds no lines. Any other card holds exactly
-- line_count lines, numbered from 0, the signals before the values, with at
-- least one value; rendered, the signals are exactly what its text prints
-- under ATTENTION and the values exactly what it prints under RECORD VALUES;
-- each cites a unit of the card; and each carries exactly the facts its code
-- implies, which are what its text prints (gtm_line_facts_hold).
CREATE FUNCTION gtm_card_lines_violation(card uuid) RETURNS text LANGUAGE plpgsql STABLE AS $$
DECLARE
    c cards%ROWTYPE;
    document json;
    printed text[];
    before boolean;
BEGIN
    SELECT * INTO c FROM cards WHERE id = card;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    before := EXISTS (SELECT 1 FROM cards_before_lines WHERE card_id = c.id AND workspace_id = c.workspace_id);
    IF c.line_count IS NULL THEN
        IF NOT before THEN
            RETURN 'card ' || c.id || ' counts no lines and was published after lines were kept';
        END IF;
        IF EXISTS (SELECT 1 FROM cards x WHERE x.line_count IS NOT NULL AND x.published_at <= c.published_at) THEN
            RETURN 'card ' || c.id || ' counts no lines and was published after a card that counts them';
        END IF;
        IF EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id) THEN
            RETURN 'card ' || c.id || ' holds lines it does not count';
        END IF;
        RETURN NULL;
    END IF;
    IF before THEN
        RETURN 'card ' || c.id || ' counts lines and is listed as published before lines were kept';
    END IF;
    IF (SELECT count(*) FROM card_lines WHERE card_id = c.id) <> c.line_count
       OR EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id AND ordinal >= c.line_count) THEN
        RETURN 'card ' || c.id || ' does not hold exactly its lines';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id AND section = 'value') THEN
        RETURN 'card ' || c.id || ' prints no record values';
    END IF;
    IF EXISTS (SELECT 1 FROM card_lines s JOIN card_lines v ON v.card_id = s.card_id
                WHERE s.card_id = c.id AND s.section = 'signal' AND v.section = 'value' AND v.ordinal < s.ordinal) THEN
        RETURN 'card ' || c.id || ' holds a signal line after a value line';
    END IF;
    BEGIN
        document := gtm_card_document(c.card_bytes);
        printed := string_to_array(coalesce(document ->> 'text', ''), E'\n');
    EXCEPTION WHEN others THEN
        RETURN 'card ' || c.id || ' is not a JSON document';
    END;
    IF (SELECT coalesce(array_agg(gtm_line_text(body) ORDER BY ordinal), '{}') FROM card_lines
         WHERE card_id = c.id AND section = 'signal')
           IS DISTINCT FROM gtm_printed_lines(printed, 'ATTENTION, from record fields and dates')
       OR (SELECT coalesce(array_agg(gtm_line_text(body) ORDER BY ordinal), '{}') FROM card_lines
            WHERE card_id = c.id AND section = 'value')
           IS DISTINCT FROM gtm_printed_lines(printed, 'RECORD VALUES, from CRM fields and connector arithmetic') THEN
        RETURN 'card ' || c.id || ': its lines differ from the sealed text';
    END IF;
    IF EXISTS (SELECT 1 FROM card_lines l, jsonb_array_elements(l.body -> 'cites') AS e(n)
                WHERE l.card_id = c.id
                  AND (jsonb_typeof(e.n) <> 'number' OR e.n::text !~ '^[1-9][0-9]{0,8}$'
                       OR e.n::text::integer > c.unit_count)) THEN
        RETURN 'card ' || c.id || ': a line cites a unit the card does not hold';
    END IF;
    IF EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id
                AND NOT coalesce(gtm_line_facts_hold(body, document ->> 'as_of'), false)) THEN
        RETURN 'card ' || c.id || ': a line carries facts its text does not print';
    END IF;
    RETURN NULL;
END
$$;

-- At commit, a new card's lines pass gtm_card_lines_violation, and no line
-- is added to a card afterwards (its count would no longer match).
CREATE FUNCTION gtm_card_lines_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    IF TG_TABLE_NAME = 'cards' THEN
        problem := gtm_card_lines_violation(NEW.id);
    ELSE
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.line_count = (SELECT count(*) FROM card_lines WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a line was added to published card ' || NEW.card_id;
        END IF;
    END IF;
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT009';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER cards_lines_checked AFTER INSERT ON cards
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_card_lines_check();
CREATE CONSTRAINT TRIGGER card_lines_counted AFTER INSERT ON card_lines
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_card_lines_check();

-- A card published from now on counts its lines, whatever its kind. A
-- restore loads the cards published before with this trigger off, and checks
-- them against cards_before_lines instead.
CREATE FUNCTION gtm_cards_count_lines() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.line_count IS NULL THEN
        RAISE EXCEPTION 'a card published since lines were kept counts its lines' USING ERRCODE = 'GT009';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER cards_count_lines BEFORE INSERT ON cards
    FOR EACH ROW EXECUTE FUNCTION gtm_cards_count_lines();

-- gtm_integrity_violations checks the rows a restore loads. It keeps every
-- check it held, under another name, and adds each card's lines. A later
-- migration that replaces gtm_integrity_violations() must keep the line
-- check, gtm_card_lines_violation over every card, or a restore stops
-- checking lines.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_receipt_and_card_violations;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_receipt_and_card_violations() v
    UNION ALL
    SELECT v FROM (SELECT gtm_card_lines_violation(id) AS v FROM cards) x WHERE v IS NOT NULL
$$;
