-- A deal closes with a reason, a receipt and a final card.
--
-- Closing a deal changed its state and nothing else. Now a close says who closed the deal and why, under a
-- close_deal receipt that finishes ok in the transaction that closes it, and names the deal's final card:
-- its newest card as it closes, or none when it has no card. A card is bound only while its deal is open,
-- so the final card stays the newest for good.
--
-- A deal closed before this migration has no such record. It is marked unrecorded here, and no deal is
-- closed that way afterwards.

ALTER TABLE deal_cards ADD CONSTRAINT deal_cards_card_of_deal UNIQUE (workspace_id, card_id, deal_ref);

-- The columns are added with the type change that fills close_basis, which rewrites the rows without firing
-- the guard that lets a deal change only as it closes.
ALTER TABLE deals
    ADD COLUMN close_basis text,
    ADD COLUMN close_reason text,
    ADD COLUMN closed_by uuid,
    ADD COLUMN close_receipt_id uuid,
    ADD COLUMN final_card_id uuid;
ALTER TABLE deals
    ALTER COLUMN close_basis TYPE text USING CASE WHEN state = 'closed' THEN 'unrecorded' END;
ALTER TABLE deals
    ADD CONSTRAINT deals_close_basis_check CHECK (close_basis IN ('person', 'unrecorded')),
    ADD CONSTRAINT deals_close_recorded CHECK ((state = 'closed') = (close_basis IS NOT NULL)),
    -- A reason is one line of 1 to 500 characters, with no control character or line separator, that neither
    -- starts nor ends with white space: what postgres.CloseReason gives.
    ADD CONSTRAINT deals_close_reason_check
        CHECK (char_length(close_reason) BETWEEN 1 AND 500
               AND close_reason !~ '[\u0001-\u001f\u007f-\u009f\u2028\u2029]'
               AND close_reason !~ '^[ \u00a0\u1680\u2000-\u200a\u202f\u205f\u3000]'
               AND close_reason !~ '[ \u00a0\u1680\u2000-\u200a\u202f\u205f\u3000]$'),
    ADD CONSTRAINT deals_close_person
        CHECK (close_basis IS DISTINCT FROM 'person' OR (close_reason IS NOT NULL AND close_receipt_id IS NOT NULL)),
    ADD CONSTRAINT deals_close_only_when_recorded
        CHECK (close_basis IS NOT DISTINCT FROM 'person'
               OR (close_reason IS NULL AND closed_by IS NULL AND close_receipt_id IS NULL
                   AND final_card_id IS NULL)),
    ADD CONSTRAINT deals_closed_by_fkey
        FOREIGN KEY (workspace_id, closed_by) REFERENCES principals (workspace_id, id),
    ADD CONSTRAINT deals_close_receipt_fkey
        FOREIGN KEY (workspace_id, close_receipt_id) REFERENCES receipts (workspace_id, id),
    ADD CONSTRAINT deals_final_card_fkey
        FOREIGN KEY (workspace_id, final_card_id, ref) REFERENCES deal_cards (workspace_id, card_id, deal_ref);
-- One receipt closes one deal.
CREATE UNIQUE INDEX deals_close_receipt ON deals (workspace_id, close_receipt_id);

-- What is wrong with a deal's close, or NULL: its receipt is a close_deal that finished ok for the deal's
-- account and names who closed it; the receipt's arguments are the deal, the basis, the reason and the final
-- card, those four members and no other, each of its own type, with a null final card for none; and the
-- final card is a card of the deal and its newest, or the deal has no card. These hold for good, so a
-- restore checks them too. A deal closed before closes were recorded has nothing to check.
CREATE FUNCTION gtm_deal_close_violation(d deals) RETURNS text LANGUAGE plpgsql STABLE AS $$
BEGIN
    IF d.state <> 'closed' OR d.close_basis = 'unrecorded' THEN
        RETURN NULL;
    END IF;
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = d.workspace_id AND r.id = d.close_receipt_id AND r.status = 'ok'
       AND r.tool = 'close_deal' AND r.connection_id IS NULL AND r.account_ref = d.account_ref
       AND r.principal_id IS NOT DISTINCT FROM d.closed_by
       AND r.arguments::jsonb = jsonb_build_object('deal', d.ref, 'basis', d.close_basis,
                                                   'reason', d.close_reason, 'final_card', d.final_card_id);
    IF NOT FOUND THEN
        RETURN 'deal ' || d.ref || ' is not closed as its receipt names';
    END IF;
    IF d.final_card_id IS NOT NULL THEN
        PERFORM 1 FROM deal_cards l
         WHERE l.workspace_id = d.workspace_id AND l.card_id = d.final_card_id AND l.deal_ref = d.ref;
        IF NOT FOUND THEN
            RETURN 'deal ' || d.ref || ' closed on a card that is not its own';
        END IF;
    END IF;
    PERFORM 1 FROM deal_cards l JOIN cards c ON c.workspace_id = l.workspace_id AND c.id = l.card_id
     WHERE l.workspace_id = d.workspace_id AND l.deal_ref = d.ref
       AND (d.final_card_id IS NULL
            OR (c.published_at, c.id) > (SELECT f.published_at, f.id FROM cards f
                                          WHERE f.workspace_id = d.workspace_id AND f.id = d.final_card_id));
    IF FOUND THEN
        RETURN 'deal ' || d.ref || ' closed on a card that is not its newest';
    END IF;
    RETURN NULL;
END
$$;

-- A deal is born open with bindings its reads returned. Afterwards it only closes, once, at the database's
-- time, as its close_deal receipt names. A card is bound under a share lock on its deal, so a close waits for
-- it; the close must then see it, which only a read committed transaction does: one with a pinned snapshot
-- would check the final card against the cards as they were when it began, and is refused.
CREATE OR REPLACE FUNCTION gtm_deals_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'open' THEN
            RAISE EXCEPTION 'a deal is registered open' USING ERRCODE = 'GT021';
        END IF;
        problem := gtm_deal_violation(NEW);
        IF problem IS NOT NULL THEN
            RAISE EXCEPTION '%', problem USING ERRCODE = 'GT021';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state <> 'open' OR NEW.state <> 'closed'
       OR (NEW.workspace_id, NEW.ref, NEW.account_ref, NEW.opportunity_ref, NEW.contract_ref, NEW.renews_on,
           NEW.registered_by, NEW.account_read_id, NEW.opportunities_read_id, NEW.registered_at)
          IS DISTINCT FROM
          (OLD.workspace_id, OLD.ref, OLD.account_ref, OLD.opportunity_ref, OLD.contract_ref, OLD.renews_on,
           OLD.registered_by, OLD.account_read_id, OLD.opportunities_read_id, OLD.registered_at) THEN
        RAISE EXCEPTION 'a deal only closes, once' USING ERRCODE = 'GT021';
    END IF;
    IF NEW.close_basis IS NOT DISTINCT FROM 'unrecorded' THEN
        RAISE EXCEPTION 'a deal closes under a receipt' USING ERRCODE = 'GT021';
    END IF;
    IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
        RAISE EXCEPTION 'a deal closes in a read committed transaction' USING ERRCODE = 'GT021';
    END IF;
    NEW.closed_at := clock_timestamp();
    problem := gtm_deal_close_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT021';
    END IF;
    RETURN NEW;
END
$$;

-- A restore loads deals with their triggers off, so gtm_integrity_violations checks their closes too. It
-- keeps every check it held, under another name. A later migration that replaces it must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_violations_before_deal_closes;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_violations_before_deal_closes() v
    UNION ALL
    SELECT v FROM (SELECT gtm_deal_close_violation(d) AS v FROM deals d) x WHERE v IS NOT NULL
$$;

-- reporting.receipts names close_deal: the same view, column for column, with that name.
CREATE OR REPLACE VIEW reporting.receipts AS
SELECT r.workspace_id, r.id AS receipt_id, r.parent_id, r.ordinal, r.principal_id,
       CASE WHEN r.tool IN ('search_accounts', 'get_account', 'list_opportunities', 'read_call_notes',
                            'read_slack_messages', 'prepare_renewal_brief', 'read_renewal_brief', 'list_deals',
                            'read_deal_card', 'save_brief', 'log_activity',
                            'list_users', 'find_renewal_candidate', 'decide_match', 'close_deal',
                            'grant_account', 'revoke_grant', 'disable_person', 'enable_person',
                            'slack_sign_in_exchange', 'slack_sign_in_identity', 'slack_sign_in_refresh',
                            'slack_sign_in_revoke',
                            'check_salesforce_login', 'check_salesforce_org', 'check_salesforce_fields',
                            'connect_salesforce_login_display', 'connect_salesforce_login_token',
                            'connect_salesforce_org', 'connect_salesforce_userinfo', 'connect_salesforce_query',
                            'connect_salesforce_describe_account', 'connect_salesforce_describe_contact',
                            'connect_salesforce_describe_opportunity', 'connect_salesforce_describe_contract')
            THEN r.tool ELSE '<invalid>' END AS tool,
       r.scope, r.account_ref, r.connection_id, r.connection_generation,
       CASE WHEN r.brief_id ~ '^[A-Za-z0-9_-]{1,128}$' THEN r.brief_id END AS brief_id, r.status, r.http_status,
       CASE WHEN r.error ~ '^[a-z0-9_]{1,64}$' THEN r.error END AS error_code, r.row_count, r.rows,
       r.started_at, r.finished_at, r.elapsed_ms
  FROM receipts r;
