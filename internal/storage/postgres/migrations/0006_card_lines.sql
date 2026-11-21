-- The lines a card prints under ATTENTION and RECORD VALUES, as data: a
-- projection of the sealed text, written in the transaction that publishes
-- the card and checked against the text's own lines at commit. The sealed
-- bytes and their digest are untouched.
--
-- line_count is how many lines a card holds. A card published before lines
-- were projected has none and a null count; every card made here from now on
-- counts its lines, so a reader can tell "no lines recorded" from a card.
ALTER TABLE cards ADD COLUMN line_count integer CHECK (line_count >= 0);

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

-- The "text" of a card's bytes, or NULL when they hold none. PostgreSQL reads
-- no JSON string that holds U+0000, which a record in the card's evidence
-- may, so each such escape, one that no backslash of its own escapes, is read
-- as U+FFFD; a text that held one would then differ from its lines.
CREATE FUNCTION gtm_card_text(card_bytes bytea) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT regexp_replace(convert_from(card_bytes, 'UTF8'), '(?<![\\])((?:[\\][\\])*)[\\]u0000', '\1\\ufffd',
        'g')::json ->> 'text'
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

-- What is wrong with a card's lines, or NULL. A card with a null count holds
-- no lines. Otherwise it holds exactly line_count lines, numbered from 0,
-- the signals before the values; rendered, the signals are exactly what its
-- text prints under ATTENTION and the values exactly what it prints under
-- RECORD VALUES; and each cites a unit of the card.
CREATE FUNCTION gtm_card_lines_violation(card uuid) RETURNS text LANGUAGE plpgsql STABLE AS $$
DECLARE
    c cards%ROWTYPE;
    printed text[];
BEGIN
    SELECT * INTO c FROM cards WHERE id = card;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF c.line_count IS NULL THEN
        IF EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id) THEN
            RETURN 'card ' || c.id || ' holds lines it does not count';
        END IF;
        RETURN NULL;
    END IF;
    IF (SELECT count(*) FROM card_lines WHERE card_id = c.id) <> c.line_count
       OR EXISTS (SELECT 1 FROM card_lines WHERE card_id = c.id AND ordinal >= c.line_count) THEN
        RETURN 'card ' || c.id || ' does not hold exactly its lines';
    END IF;
    IF EXISTS (SELECT 1 FROM card_lines s JOIN card_lines v ON v.card_id = s.card_id
                WHERE s.card_id = c.id AND s.section = 'signal' AND v.section = 'value' AND v.ordinal < s.ordinal) THEN
        RETURN 'card ' || c.id || ' holds a signal line after a value line';
    END IF;
    BEGIN
        printed := string_to_array(coalesce(gtm_card_text(c.card_bytes), ''), E'\n');
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

-- A card made here counts its lines. A restore loads the cards published
-- before lines were projected with this trigger off.
CREATE FUNCTION gtm_cards_count_lines() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.kind = 'go' AND NEW.line_count IS NULL THEN
        RAISE EXCEPTION 'a card made here counts its lines' USING ERRCODE = 'GT009';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER cards_count_lines BEFORE INSERT ON cards
    FOR EACH ROW EXECUTE FUNCTION gtm_cards_count_lines();

-- gtm_integrity_violations checks the rows a restore loads. It keeps every
-- check it held, under another name, and adds each card's lines.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_receipt_and_card_violations;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_receipt_and_card_violations() v
    UNION ALL
    SELECT v FROM (SELECT gtm_card_lines_violation(id) AS v FROM cards) x WHERE v IS NOT NULL
$$;
