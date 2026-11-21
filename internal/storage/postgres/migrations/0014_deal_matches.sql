-- Matches: which deal each imported call and each mapped Slack thread belongs to.
--
-- An item is one call import revision (note-...) or one Slack mapping (map-...), stated for one account when
-- it was imported or mapped. Its time is the call's time, or unknown, and for a thread the time of its parent
-- message. The deals of an account follow each other, so each deal holds the times from its own registration
-- to the next deal's, the first deal every earlier time and the last every later one: evidence joins the next
-- deal from the moment it is registered. An item fits the deal that holds its time, and an item with no time
-- fits every deal of its account (gtm_match_candidates).
--
-- While the account has an open deal, the connector decides each item once: one deal fits, and the item is
-- matched to it, or more fit, and the item waits for a person. A person may later decide an item again:
-- matched to one deal of its account, or rejected, which keeps it off every card. Each decision is a row that
-- names its receipt (tool decide_match, finished ok, naming the item, the decision and the deal), and a later
-- decision of the same item supersedes the one before it. Rows are never changed or deleted.
--
-- A card bound to a deal names the matches of the calls and threads it read (deal_card_matches). Each was
-- matched to that deal when the card was published, and a call's was read by one of the card's reads.

-- The time a Slack timestamp names, to the microsecond.
CREATE FUNCTION gtm_slack_time(ts text) RETURNS timestamptz LANGUAGE sql IMMUTABLE AS $$
    SELECT to_timestamp(split_part(ts, '.', 1)::bigint) + split_part(ts, '.', 2)::bigint * interval '1 microsecond'
$$;

-- The time a call import revision states, or NULL.
CREATE FUNCTION gtm_call_time(manifest text) RETURNS timestamptz LANGUAGE sql STABLE AS $$
    SELECT (manifest::jsonb ->> 'called_at')::timestamptz
$$;

CREATE FUNCTION gtm_match_candidates(ws uuid, account text, at timestamptz) RETURNS text[] LANGUAGE sql STABLE AS $$
    SELECT coalesce(array_agg(ref ORDER BY registered_at, ref), '{}') FROM (
        SELECT ref, registered_at,
               lag(registered_at) OVER (ORDER BY registered_at, ref) IS NULL AS first,
               lead(registered_at) OVER (ORDER BY registered_at, ref) AS next
          FROM deals WHERE workspace_id = ws AND account_ref = account) d
     WHERE at IS NULL OR ((first OR at >= registered_at) AND (next IS NULL OR at < next))
$$;

CREATE TABLE deal_matches (
    workspace_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('call', 'thread')),
    item text COLLATE "C" NOT NULL,
    account_ref text NOT NULL,
    at timestamptz,
    candidates text[] COLLATE "C" NOT NULL CHECK (cardinality(candidates) >= 1),
    state text NOT NULL CHECK (state IN ('matched', 'waiting', 'rejected')),
    deal_ref text COLLATE "C",
    basis text NOT NULL CHECK (basis IN ('only_deal', 'within_deal', 'time_unknown', 'person')),
    decided_by uuid,
    supersedes uuid,
    receipt_id uuid NOT NULL UNIQUE,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (id),
    UNIQUE (workspace_id, supersedes),
    CHECK (kind = 'call' AND item ~ '^note-[0-9a-f]{64}$' OR kind = 'thread' AND item ~ '^map-[0-9a-f]{64}$'),
    CHECK ((state = 'matched') = (deal_ref IS NOT NULL)),
    CHECK ((basis = 'person') = (supersedes IS NOT NULL)),
    CHECK ((state = 'waiting') = (basis = 'time_unknown')),
    CHECK (basis = 'person' OR decided_by IS NULL),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, deal_ref, account_ref) REFERENCES deals (workspace_id, ref, account_ref),
    FOREIGN KEY (workspace_id, decided_by) REFERENCES principals (workspace_id, id),
    FOREIGN KEY (workspace_id, supersedes) REFERENCES deal_matches (workspace_id, id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id)
);
-- The connector decides an item once; every later decision supersedes one.
CREATE UNIQUE INDEX deal_matches_first ON deal_matches (workspace_id, kind, item) WHERE supersedes IS NULL;
CREATE INDEX deal_matches_account ON deal_matches (workspace_id, account_ref);

-- What is wrong with a decision, or NULL: its receipt, its candidates, and the decision it supersedes. These
-- hold for good, so a restore checks them too.
CREATE FUNCTION gtm_deal_match_violation(m deal_matches) RETURNS text LANGUAGE plpgsql STABLE AS $$
BEGIN
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = m.workspace_id AND r.id = m.receipt_id AND r.status = 'ok'
       AND r.tool = 'decide_match' AND r.connection_id IS NULL AND r.account_ref = m.account_ref
       AND r.principal_id IS NOT DISTINCT FROM m.decided_by
       AND r.arguments ->> 'kind' = m.kind AND r.arguments ->> 'item' = m.item
       AND r.arguments ->> 'decision' = m.state
       AND (r.arguments ->> 'deal') IS NOT DISTINCT FROM m.deal_ref
       AND (r.arguments ->> 'match') IS NOT DISTINCT FROM m.supersedes::text;
    IF NOT FOUND THEN
        RETURN 'match ' || m.id || ' is not the decision its receipt names';
    END IF;
    PERFORM 1 FROM unnest(m.candidates) c
     WHERE NOT EXISTS (SELECT 1 FROM deals d
                        WHERE d.workspace_id = m.workspace_id AND d.ref = c AND d.account_ref = m.account_ref);
    IF FOUND THEN
        RETURN 'match ' || m.id || ' names a candidate that is no deal of its account';
    END IF;
    IF m.basis <> 'person' AND (m.state = 'matched') <> (cardinality(m.candidates) = 1)
       OR m.basis <> 'person' AND m.state = 'matched' AND m.deal_ref <> m.candidates[1]
       OR m.state = 'rejected' AND m.basis <> 'person' THEN
        RETURN 'match ' || m.id || ' does not follow from its candidates';
    END IF;
    IF m.supersedes IS NOT NULL THEN
        PERFORM 1 FROM deal_matches p
         WHERE p.workspace_id = m.workspace_id AND p.id = m.supersedes AND p.kind = m.kind AND p.item = m.item
           AND p.account_ref = m.account_ref AND p.at IS NOT DISTINCT FROM m.at AND p.candidates = m.candidates
           AND p.decided_at <= m.decided_at;
        IF NOT FOUND THEN
            RETURN 'match ' || m.id || ' supersedes a decision of another item';
        END IF;
    END IF;
    RETURN NULL;
END
$$;

-- At its insert, the connector's own decision also follows the rule: the account has an open deal, the item
-- is the account's, its time is the item's own, and its candidates are the deals that hold that time.
CREATE FUNCTION gtm_deal_matches_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    problem := gtm_deal_match_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT023';
    END IF;
    IF NEW.supersedes IS NOT NULL THEN
        RETURN NEW;
    END IF;
    PERFORM 1 FROM deals WHERE workspace_id = NEW.workspace_id AND account_ref = NEW.account_ref AND state = 'open'
        FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the connector decides an item only while its account has an open deal' USING ERRCODE = 'GT023';
    END IF;
    IF NEW.kind = 'call' THEN
        PERFORM 1 FROM call_import_revisions v
         WHERE v.workspace_id = NEW.workspace_id AND v.import_id = NEW.item AND v.account_ref = NEW.account_ref
           AND gtm_call_time(v.manifest) IS NOT DISTINCT FROM NEW.at;
    ELSE
        PERFORM 1 FROM slack_mappings s
         WHERE s.workspace_id = NEW.workspace_id AND s.account_ref = NEW.account_ref
           AND s.mapping::jsonb ->> 'mapping_id' = NEW.item AND gtm_slack_time(s.thread_ts) = NEW.at;
    END IF;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'match % is not of an item of its account at its time', NEW.id USING ERRCODE = 'GT023';
    END IF;
    IF NEW.candidates <> gtm_match_candidates(NEW.workspace_id, NEW.account_ref, NEW.at) THEN
        RAISE EXCEPTION 'match % names other deals than hold its time', NEW.id USING ERRCODE = 'GT023';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER deal_matches_guard BEFORE INSERT ON deal_matches
    FOR EACH ROW EXECUTE FUNCTION gtm_deal_matches_guard();
CREATE TRIGGER deal_matches_immutable BEFORE UPDATE OR DELETE ON deal_matches
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER deal_matches_no_truncate BEFORE TRUNCATE ON deal_matches
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- The matches each deal card read.
CREATE TABLE deal_card_matches (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL REFERENCES deal_cards (card_id),
    match_id uuid NOT NULL,
    PRIMARY KEY (card_id, match_id),
    FOREIGN KEY (workspace_id, match_id) REFERENCES deal_matches (workspace_id, id)
);

-- What is wrong with a card's match, or NULL: it is matched to the card's deal, and a call's was read by one
-- of the card's reads, a thread's by a Slack read of the card.
CREATE FUNCTION gtm_deal_card_match_violation(l deal_card_matches) RETURNS text LANGUAGE sql STABLE AS $$
    SELECT 'card ' || l.card_id || ' names match ' || l.match_id || ', which it did not read for its deal'
     WHERE NOT EXISTS (
        SELECT 1 FROM deal_cards dc
          JOIN deal_matches m ON m.workspace_id = dc.workspace_id AND m.id = l.match_id
          JOIN card_reads cr ON cr.card_id = dc.card_id
          JOIN receipts r ON r.workspace_id = cr.workspace_id AND r.id = cr.receipt_id
         WHERE dc.card_id = l.card_id AND dc.workspace_id = l.workspace_id AND m.state = 'matched'
           AND m.deal_ref = dc.deal_ref AND r.status = 'ok'
           AND (m.kind = 'call' AND r.tool = 'read_call_notes' AND r.rows ? ('calls/call_notes/' || m.item)
             OR m.kind = 'thread' AND r.tool = 'read_slack_messages'))
$$;

-- A card names only its account's current decision of an item.
CREATE FUNCTION gtm_deal_card_matches_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    problem := gtm_deal_card_match_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT024';
    END IF;
    PERFORM 1 FROM deal_matches WHERE workspace_id = NEW.workspace_id AND supersedes = NEW.match_id;
    IF FOUND THEN
        RAISE EXCEPTION 'match % was decided again before the card was published', NEW.match_id
            USING ERRCODE = 'GT024';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER deal_card_matches_guard BEFORE INSERT ON deal_card_matches
    FOR EACH ROW EXECUTE FUNCTION gtm_deal_card_matches_guard();
CREATE TRIGGER deal_card_matches_immutable BEFORE UPDATE OR DELETE ON deal_card_matches
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER deal_card_matches_no_truncate BEFORE TRUNCATE ON deal_card_matches
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A deal card says when it did not read the account's call or thread because it is not matched to the deal.
ALTER TABLE gaps DROP CONSTRAINT gaps_code_check;
ALTER TABLE gaps ADD CONSTRAINT gaps_code_check CHECK (code IN (
    'crm_unmapped_fields', 'calls_denied', 'calls_excerpt', 'calls_outside_records',
    'slack_not_requested', 'slack_denied', 'slack_thread', 'slack_channel',
    'calls_not_connected', 'slack_not_connected', 'slack_not_selected',
    'calls_not_matched', 'slack_not_matched',
    'crm_more_opportunities', 'crm_more_contracts', 'crm_more_contacts', 'sentences_cut',
    'sentences_more', 'sentences_long', 'sentences_later', 'crm_fields_empty',
    'crm_no_opportunity', 'calls_no_record', 'slack_no_message', 'calls_no_transcript',
    'speakers_unknown', 'neutralized', 'calls_no_contact'));

-- A restore loads matches with their triggers off, so gtm_integrity_violations checks them too. It keeps
-- every check it held, under another name. A later migration that replaces it must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_violations_before_deal_matches;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_violations_before_deal_matches() v
    UNION ALL
    SELECT v FROM (SELECT gtm_deal_match_violation(m) AS v FROM deal_matches m) x WHERE v IS NOT NULL
    UNION ALL
    SELECT v FROM (SELECT gtm_deal_card_match_violation(l) AS v FROM deal_card_matches l) x WHERE v IS NOT NULL
$$;
