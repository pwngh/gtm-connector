-- Deals: one renewal of one account, under a readable ID such as thornfield-2027-2e3hke.
--
-- A deal is registered by hand after two CRM reads of its account, each under its own receipt: get_account
-- and list_opportunities, made for the account as the workspace lists it, by the principal that registers
-- the deal (none for the owner). The deal names both receipts, and binds only records they returned: the
-- account, and the opportunity and the contract when the CRM has them. Its ID is unique in its workspace
-- and is never given again. An account has at most one open deal. A deal closes once, and is never changed
-- otherwise or deleted.
--
-- A card belongs to at most one deal, of the card's own account, published while that deal was open, and
-- is bound to it in the card's own publication. A card's bytes do not name its deal.

ALTER TABLE cards ADD CONSTRAINT cards_account_unique UNIQUE (workspace_id, id, account_ref);

CREATE TABLE deals (
    workspace_id uuid NOT NULL,
    ref text COLLATE "C" NOT NULL CHECK (ref ~ '^[a-z0-9]{1,40}-[0-9]{4}-([a-z2-7]{6}|[a-z2-7]{12})(-[1-9][0-9]{0,5})?$'),
    account_ref text NOT NULL,
    opportunity_ref text COLLATE "C" CHECK (opportunity_ref ~ '^[A-Za-z0-9._-]{1,64}$'),
    contract_ref text COLLATE "C" CHECK (contract_ref ~ '^[A-Za-z0-9._-]{1,64}$'),
    renews_on date,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    registered_by uuid,
    account_read_id uuid NOT NULL,
    opportunities_read_id uuid NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    PRIMARY KEY (workspace_id, ref),
    UNIQUE (workspace_id, ref, account_ref),
    CHECK ((state = 'closed') = (closed_at IS NOT NULL)),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, registered_by) REFERENCES principals (workspace_id, id),
    FOREIGN KEY (workspace_id, account_read_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, opportunities_read_id) REFERENCES receipts (workspace_id, id)
);
CREATE UNIQUE INDEX deals_one_open ON deals (workspace_id, account_ref) WHERE state = 'open';

-- What is wrong with a deal's bindings, or NULL: each read finished ok, is the read it is named as, was made
-- for the deal's account by the principal that registered it, and returned what the deal binds.
CREATE FUNCTION gtm_deal_violation(d deals) RETURNS text LANGUAGE plpgsql STABLE AS $$
BEGIN
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = d.workspace_id AND r.id = d.account_read_id AND r.status = 'ok'
       AND r.tool = 'get_account' AND r.account_ref = d.account_ref
       AND r.principal_id IS NOT DISTINCT FROM d.registered_by
       AND r.rows ? ('crm/accounts/' || d.account_ref)
       AND (d.contract_ref IS NULL OR r.rows ? ('crm/contracts/' || d.contract_ref));
    IF NOT FOUND THEN
        RETURN 'deal ' || d.ref || ' binds what its account read did not return';
    END IF;
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = d.workspace_id AND r.id = d.opportunities_read_id AND r.status = 'ok'
       AND r.tool = 'list_opportunities' AND r.account_ref = d.account_ref
       AND r.principal_id IS NOT DISTINCT FROM d.registered_by
       AND (d.opportunity_ref IS NULL OR r.rows ? ('crm/opportunities/' || d.opportunity_ref));
    IF NOT FOUND THEN
        RETURN 'deal ' || d.ref || ' binds what its opportunities read did not return';
    END IF;
    RETURN NULL;
END
$$;

-- A deal is born open with bindings its reads returned. Afterwards only its state changes, from open to
-- closed, with the time it closed.
CREATE FUNCTION gtm_deals_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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
    RETURN NEW;
END
$$;
CREATE TRIGGER deals_guard BEFORE INSERT OR UPDATE ON deals
    FOR EACH ROW EXECUTE FUNCTION gtm_deals_guard();
CREATE TRIGGER deals_no_delete BEFORE DELETE ON deals
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER deals_no_truncate BEFORE TRUNCATE ON deals
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- The cards of each deal. The keys hold a card to its own account's deal; the trigger holds the deal open
-- (a share lock, so a close waits) while the card is bound, and refuses a card published before the deal
-- was registered.
CREATE TABLE deal_cards (
    workspace_id uuid NOT NULL,
    card_id uuid PRIMARY KEY,
    deal_ref text COLLATE "C" NOT NULL,
    account_ref text NOT NULL,
    FOREIGN KEY (workspace_id, deal_ref, account_ref) REFERENCES deals (workspace_id, ref, account_ref),
    FOREIGN KEY (workspace_id, card_id, account_ref) REFERENCES cards (workspace_id, id, account_ref)
);
CREATE INDEX deal_cards_deal ON deal_cards (workspace_id, deal_ref);

-- What is wrong with a card's place in its deal, or NULL: the card was published while the deal was open.
CREATE FUNCTION gtm_deal_card_violation(l deal_cards) RETURNS text LANGUAGE sql STABLE AS $$
    SELECT 'card ' || l.card_id || ' was published outside the time deal ' || l.deal_ref || ' was open'
      FROM deals d JOIN cards c ON c.workspace_id = d.workspace_id AND c.id = l.card_id
     WHERE d.workspace_id = l.workspace_id AND d.ref = l.deal_ref
       AND (c.published_at < d.registered_at OR c.published_at > d.closed_at)
$$;

CREATE FUNCTION gtm_deal_cards_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    PERFORM 1 FROM deals WHERE workspace_id = NEW.workspace_id AND ref = NEW.deal_ref AND state = 'open'
        FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'deal % is not open', NEW.deal_ref USING ERRCODE = 'GT022';
    END IF;
    problem := gtm_deal_card_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT022';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER deal_cards_while_open BEFORE INSERT ON deal_cards
    FOR EACH ROW EXECUTE FUNCTION gtm_deal_cards_guard();
CREATE TRIGGER deal_cards_immutable BEFORE UPDATE OR DELETE ON deal_cards
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER deal_cards_no_truncate BEFORE TRUNCATE ON deal_cards
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A restore loads deals and their cards with their triggers off, so gtm_integrity_violations checks the
-- deals' bindings and their cards' times too. It keeps every check it held, under another name. A later
-- migration that replaces it must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_violations_before_deals;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_violations_before_deals() v
    UNION ALL
    SELECT v FROM (SELECT gtm_deal_violation(d) AS v FROM deals d) x WHERE v IS NOT NULL
    UNION ALL
    SELECT v FROM (SELECT gtm_deal_card_violation(l) AS v FROM deal_cards l) x WHERE v IS NOT NULL
$$;
