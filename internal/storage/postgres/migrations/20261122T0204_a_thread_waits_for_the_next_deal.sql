-- A thread, or a call with no time, waits for a person when its deal is closed and the account's next deal is
-- open.
--
-- A thread's time is its parent message's, and a thread goes on: one that began under an earlier deal may hold
-- the words of the next. A call with no time fits every deal of its account. So when such an item is on a deal
-- that is closed, by its time or by a decision made before the account's open deal was registered, it fits
-- both the closed deal and the open one, and the connector decides it as waiting (basis deal_closed), with
-- those two deals as its candidates, under a decide_match receipt of its own. Where the item already had a
-- decision, the new one supersedes it and its receipt names it. A person then puts it on one deal or on none,
-- as with any waiting item. A call with a time stays on the deal that holds it.

ALTER TABLE deal_matches
    DROP CONSTRAINT deal_matches_basis_check,
    DROP CONSTRAINT deal_matches_check2,
    DROP CONSTRAINT deal_matches_check3,
    ADD CONSTRAINT deal_matches_basis_check
        CHECK (basis IN ('only_deal', 'within_deal', 'time_unknown', 'deal_closed', 'person')),
    -- A person's decision supersedes one, and only a person or a closed deal makes a later decision.
    ADD CONSTRAINT deal_matches_person_supersedes CHECK (basis <> 'person' OR supersedes IS NOT NULL),
    ADD CONSTRAINT deal_matches_superseding_basis CHECK (supersedes IS NULL OR basis IN ('person', 'deal_closed')),
    ADD CONSTRAINT deal_matches_waiting_basis
        CHECK ((state = 'waiting') = (basis IN ('time_unknown', 'deal_closed')));

-- What is wrong with a decision, or NULL, as before, and for a decision of basis deal_closed: its item is a
-- thread or a call with no time; its first candidate is a deal of the account closed by then, the one that
-- held its time for a first decision, or the one the decision it supersedes matched it to; its second is
-- the account's deal that was open then; and a person's decision it supersedes was made before that deal
-- was registered, so a person who kept the item on the closed deal since is not overruled. These hold for
-- good, so a restore checks them too.
CREATE OR REPLACE FUNCTION gtm_deal_match_violation(m deal_matches) RETURNS text LANGUAGE plpgsql STABLE AS $$
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
    IF m.kind = 'call' THEN
        PERFORM 1 FROM call_import_revisions v
         WHERE v.workspace_id = m.workspace_id AND v.import_id = m.item AND v.account_ref = m.account_ref
           AND gtm_call_time(v.manifest) IS NOT DISTINCT FROM m.at;
        IF NOT FOUND THEN
            RETURN 'match ' || m.id || ' is not of a call of its account at the call''s time';
        END IF;
    ELSE
        PERFORM 1 FROM slack_mappings s
         WHERE s.workspace_id = m.workspace_id AND s.mapping::jsonb ->> 'mapping_id' = m.item
           AND (s.account_ref <> m.account_ref OR gtm_slack_time(s.thread_ts) IS DISTINCT FROM m.at);
        IF FOUND THEN
            RETURN 'match ' || m.id || ' is not of a thread of its account at the thread''s time';
        END IF;
    END IF;
    IF m.basis IN ('only_deal', 'within_deal', 'time_unknown')
       AND m.candidates <> gtm_match_candidates(m.workspace_id, m.account_ref, m.at, m.decided_at) THEN
        RETURN 'match ' || m.id || ' names other deals than held its time when it was decided';
    END IF;
    IF m.basis = 'deal_closed' THEN
        PERFORM 1 FROM deals c, deals o
         WHERE (m.kind = 'thread' OR m.at IS NULL) AND cardinality(m.candidates) = 2
           AND c.workspace_id = m.workspace_id AND c.account_ref = m.account_ref AND c.ref = m.candidates[1]
           AND c.closed_at <= m.decided_at
           AND o.workspace_id = m.workspace_id AND o.account_ref = m.account_ref AND o.ref = m.candidates[2]
           AND o.registered_at <= m.decided_at AND (o.closed_at IS NULL OR o.closed_at > m.decided_at)
           AND CASE WHEN m.supersedes IS NULL
                    THEN gtm_match_candidates(m.workspace_id, m.account_ref, m.at, m.decided_at) = ARRAY[c.ref]
                    ELSE EXISTS (SELECT 1 FROM deal_matches p
                                  WHERE p.workspace_id = m.workspace_id AND p.id = m.supersedes
                                    AND p.state = 'matched' AND p.deal_ref = c.ref
                                    AND (p.basis <> 'person' OR p.decided_at < o.registered_at)) END;
        IF NOT FOUND THEN
            RETURN 'match ' || m.id || ' does not wait between a closed deal and the deal open then';
        END IF;
    END IF;
    IF m.basis <> 'person' AND (m.state = 'matched') <> (cardinality(m.candidates) = 1)
       OR m.basis <> 'person' AND m.state = 'matched' AND m.deal_ref <> m.candidates[1]
       OR m.state = 'rejected' AND m.basis <> 'person' THEN
        RETURN 'match ' || m.id || ' does not follow from its candidates';
    END IF;
    IF m.supersedes IS NOT NULL THEN
        PERFORM 1 FROM deal_matches p
         WHERE p.workspace_id = m.workspace_id AND p.id = m.supersedes AND p.kind = m.kind AND p.item = m.item
           AND p.account_ref = m.account_ref AND p.at IS NOT DISTINCT FROM m.at
           AND (m.basis = 'deal_closed' OR p.candidates = m.candidates)
           AND p.decided_at <= m.decided_at;
        IF NOT FOUND THEN
            RETURN 'match ' || m.id || ' supersedes a decision of another item';
        END IF;
    END IF;
    RETURN NULL;
END
$$;

-- At its insert, each decision of the connector's own, a first one or one that supersedes another, is also made
-- while the account has an open deal, of a thread the account has mapped now. A wait between two deals names
-- that open deal and is dated by this database's clock, whatever its writer gave, so it stays true when the
-- deal closes. A thread is never put on another deal than the open one by its time: it waits instead.
CREATE OR REPLACE FUNCTION gtm_deal_matches_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
    open_ref text;
BEGIN
    IF NEW.basis <> 'person' THEN
        SELECT ref INTO open_ref FROM deals
         WHERE workspace_id = NEW.workspace_id AND account_ref = NEW.account_ref AND state = 'open' FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'the connector decides an item only while its account has an open deal'
                USING ERRCODE = 'GT023';
        END IF;
    END IF;
    IF NEW.basis = 'deal_closed' THEN
        NEW.decided_at := clock_timestamp();
        IF NEW.candidates[2] IS DISTINCT FROM open_ref THEN
            RAISE EXCEPTION 'match % does not wait for the account''s open deal', NEW.id USING ERRCODE = 'GT023';
        END IF;
    END IF;
    problem := gtm_deal_match_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT023';
    END IF;
    IF NEW.basis = 'person' THEN
        RETURN NEW;
    END IF;
    IF NEW.kind = 'thread' THEN
        PERFORM 1 FROM slack_mappings s
         WHERE s.workspace_id = NEW.workspace_id AND s.account_ref = NEW.account_ref
           AND s.mapping::jsonb ->> 'mapping_id' = NEW.item;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'match % is not of a thread its account has mapped', NEW.id USING ERRCODE = 'GT023';
        END IF;
        IF NEW.state = 'matched' AND NEW.deal_ref <> open_ref THEN
            RAISE EXCEPTION 'match %: a thread on a closed deal waits for a person while another deal is open',
                NEW.id USING ERRCODE = 'GT023';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
