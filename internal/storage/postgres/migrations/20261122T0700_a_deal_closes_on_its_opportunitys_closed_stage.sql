-- A deal closes when a card reads its opportunity as closed in the CRM.
--
-- A person closes a deal with a reason. The connector closes one too: when a card it publishes for the deal
-- read the opportunity the deal binds as closed, won or lost, that card is the deal's final card and the deal
-- closes on it, whichever of the two comes first. Such a close has no reason and names no one: its basis is
-- crm_stage, its outcome is won or lost, and its close_deal receipt names the deal, the opportunity, the
-- outcome and the final card.

ALTER TABLE deals
    ADD COLUMN close_outcome text CHECK (close_outcome IN ('won', 'lost')),
    DROP CONSTRAINT deals_close_basis_check,
    DROP CONSTRAINT deals_close_only_when_recorded,
    ADD CONSTRAINT deals_close_basis_check CHECK (close_basis IN ('person', 'crm_stage', 'unrecorded')),
    -- Only a person's close and the CRM's hold a record; an open deal and an unrecorded close hold none.
    ADD CONSTRAINT deals_close_only_when_recorded
        CHECK (coalesce(close_basis IN ('person', 'crm_stage'), false)
               OR (close_reason IS NULL AND closed_by IS NULL AND close_receipt_id IS NULL
                   AND final_card_id IS NULL)),
    -- The CRM's close has an outcome, a receipt and a final card, binds an opportunity, and has no reason
    -- and no person. No other close has an outcome.
    ADD CONSTRAINT deals_close_outcome CHECK ((close_outcome IS NOT NULL) = (close_basis IS NOT DISTINCT FROM 'crm_stage')),
    ADD CONSTRAINT deals_close_crm_stage
        CHECK (close_basis IS DISTINCT FROM 'crm_stage'
               OR (close_reason IS NULL AND closed_by IS NULL AND close_receipt_id IS NOT NULL
                   AND final_card_id IS NOT NULL AND opportunity_ref IS NOT NULL));

-- What is wrong with a deal's close, or NULL, as before, and for a close of basis crm_stage: the receipt's
-- arguments are the deal, the basis, a null reason, the final card, the opportunity the deal binds and the
-- outcome, those six members and no other; and the final card rests on a list_opportunities read that
-- returned that opportunity. The database does not read the stage out of the card: that the opportunity was
-- closed is the connector's reading of the card's evidence.
CREATE OR REPLACE FUNCTION gtm_deal_close_violation(d deals) RETURNS text LANGUAGE plpgsql STABLE AS $$
DECLARE
    named jsonb;
BEGIN
    IF d.state <> 'closed' OR d.close_basis = 'unrecorded' THEN
        RETURN NULL;
    END IF;
    named := jsonb_build_object('deal', d.ref, 'basis', d.close_basis, 'reason', d.close_reason,
                                'final_card', d.final_card_id);
    IF d.close_basis = 'crm_stage' THEN
        named := named || jsonb_build_object('opportunity', d.opportunity_ref, 'outcome', d.close_outcome);
    END IF;
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = d.workspace_id AND r.id = d.close_receipt_id AND r.status = 'ok'
       AND r.tool = 'close_deal' AND r.connection_id IS NULL AND r.account_ref = d.account_ref
       AND r.principal_id IS NOT DISTINCT FROM d.closed_by
       AND r.arguments::jsonb = named;
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
    IF d.close_basis = 'crm_stage' THEN
        PERFORM 1 FROM card_reads cr JOIN receipts r ON r.workspace_id = cr.workspace_id AND r.id = cr.receipt_id
         WHERE cr.workspace_id = d.workspace_id AND cr.card_id = d.final_card_id
           AND r.tool = 'list_opportunities' AND r.status = 'ok' AND r.account_ref = d.account_ref
           AND r.rows ? ('crm/opportunities/' || d.opportunity_ref);
        IF NOT FOUND THEN
            RETURN 'deal ' || d.ref || ' closed on a card that did not read its opportunity';
        END IF;
    END IF;
    RETURN NULL;
END
$$;
