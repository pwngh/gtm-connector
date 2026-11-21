-- Reporting views: deals, cards, card lines, units and receipts, read with SQL through the role
-- gtm_reporting, which may select from these views and from nothing else.
--
-- A view runs with its owner's rights, so the role reads the tables only through the views. No view has a
-- column for a credential, a token or its fingerprint, a session, a lease, a call's arguments, a card's bytes
-- or a unit's body. The only text that came from a source is what a sealed card already prints: a line's
-- text, label and contact. Every time is a timestamptz the database's own clock wrote.
--
-- Roles belong to the whole PostgreSQL cluster, so gtm_reporting is made once, by the first database that
-- migrates, and each database grants it its own views. A later migration that changes a column a view
-- reads drops the view and makes it again, with its comments and its grant.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gtm_reporting') THEN
        CREATE ROLE gtm_reporting NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
    END IF;
EXCEPTION WHEN duplicate_object OR unique_violation THEN
    -- Another database's migration made it first.
    NULL;
END
$$;

CREATE SCHEMA reporting;
COMMENT ON SCHEMA reporting IS
    'Read-only views for reporting. Grant gtm_reporting to a login role to read them; it reads nothing else.';
REVOKE ALL ON SCHEMA reporting FROM PUBLIC;

CREATE VIEW reporting.deals AS
SELECT d.workspace_id, d.ref AS deal_id, d.account_ref, d.state, d.opportunity_ref, d.contract_ref, d.renews_on,
       d.registered_at, d.closed_at, d.account_read_id, d.opportunities_read_id,
       (SELECT count(*) FROM deal_cards dc WHERE dc.workspace_id = d.workspace_id AND dc.deal_ref = d.ref)
           AS card_count,
       l.id AS latest_card_id, l.published_at AS latest_card_published_at
  FROM deals d
  LEFT JOIN LATERAL (
      SELECT c.id, c.published_at FROM deal_cards dc
        JOIN cards c ON c.workspace_id = dc.workspace_id AND c.id = dc.card_id
       WHERE dc.workspace_id = d.workspace_id AND dc.deal_ref = d.ref
       ORDER BY c.published_at DESC, c.id DESC LIMIT 1) l ON true;
COMMENT ON VIEW reporting.deals IS
    'One row per deal: one renewal of one account. deal_id is the readable ID; account_read_id and '
    'opportunities_read_id are the receipts (reporting.receipts) of the two CRM reads its bindings came from.';
COMMENT ON COLUMN reporting.deals.renews_on IS 'The day the deal renews, as the CRM gave it; null when it gave none.';
COMMENT ON COLUMN reporting.deals.registered_at IS 'When the deal was registered, by the database clock.';
COMMENT ON COLUMN reporting.deals.closed_at IS 'When the deal closed, by the database clock; null while it is open.';
COMMENT ON COLUMN reporting.deals.card_count IS 'How many cards were published for the deal (reporting.cards.deal_id).';
COMMENT ON COLUMN reporting.deals.latest_card_id IS 'The newest card of the deal (reporting.cards.card_id), or null.';
COMMENT ON COLUMN reporting.deals.latest_card_published_at IS
    'When the newest card of the deal was published, by the database clock, or null.';

CREATE VIEW reporting.cards AS
SELECT c.workspace_id, c.id AS card_id, c.brief_id, c.account_ref, dc.deal_ref AS deal_id, c.kind,
       c.schema_version, c.rules_version,
       CASE WHEN pg_input_is_valid(s.as_of, 'date') THEN s.as_of::date END AS as_of,
       c.published_at, c.unit_count, c.gap_count, c.line_count, encode(c.card_sha256, 'hex') AS sha256,
       octet_length(c.card_bytes) AS size, c.seal_receipt_id
  FROM cards c
  LEFT JOIN deal_cards dc ON dc.card_id = c.id
  CROSS JOIN LATERAL (SELECT convert_from(c.card_bytes, 'UTF8')::json ->> 'as_of' AS as_of) s;
COMMENT ON VIEW reporting.cards IS
    'One row per sealed card. sha256 is the digest of its stored bytes, and seal_receipt_id the receipt that '
    'sealed it (reporting.receipts). deal_id is the deal it was published for, or null. Its lines are in '
    'reporting.card_lines and its units in reporting.units, by card_id.';
COMMENT ON COLUMN reporting.cards.as_of IS 'The day the card is as of, as its sealed bytes state it.';
COMMENT ON COLUMN reporting.cards.published_at IS 'When the card was published, by the database clock.';
COMMENT ON COLUMN reporting.cards.line_count IS
    'How many lines the card holds; null for a card published before lines were kept.';

CREATE VIEW reporting.card_lines AS
SELECT l.workspace_id, l.card_id, l.ordinal, l.section, l.body ->> 'code' AS code, l.body ->> 'label' AS label,
       l.body ->> 'text' AS text,
       ARRAY(SELECT n::integer FROM jsonb_array_elements_text(l.body -> 'cites') n) AS cites,
       (l.body ->> 'more')::integer AS more,
       (l.body -> 'countdown' ->> 'date')::date AS countdown_date,
       (l.body -> 'countdown' ->> 'days')::integer AS countdown_days,
       l.body -> 'countdown' ->> 'counts_to' AS counts_to,
       (l.body -> 'amount' ->> 'amount')::numeric AS amount, l.body -> 'amount' ->> 'currency' AS currency,
       l.body -> 'contact' ->> 'name' AS contact_name, l.body -> 'contact' ->> 'title' AS contact_title
  FROM card_lines l;
COMMENT ON VIEW reporting.card_lines IS
    'One row per line a card prints under ATTENTION (section signal) or RECORD VALUES (section value), in '
    'the card''s order. cites are the numbers of the units it cites: (card_id, n) in reporting.units.';
COMMENT ON COLUMN reporting.card_lines.countdown_date IS
    'The renewal or contract end day the line counts to, as the CRM gave it; null on other lines.';
COMMENT ON COLUMN reporting.card_lines.countdown_days IS
    'Days from the card''s as_of day to countdown_date; negative once it has passed.';

CREATE VIEW reporting.units AS
SELECT u.workspace_id, u.card_id, u.n, u.kind, u.body ->> 'source' AS source, u.body ->> 'role' AS role,
       u.record_ref, u.tool, u.shown, u.receipt_id, u.source_record_id AS record_version_id
  FROM evidence_units u;
COMMENT ON VIEW reporting.units IS
    'One row per numbered unit of a card, without its text. record_ref names the record it rests on, and '
    'receipt_id the read that returned it (reporting.receipts); both are null on an admitted card''s units.';

CREATE VIEW reporting.receipts AS
SELECT r.workspace_id, r.id AS receipt_id, r.parent_id, r.ordinal, r.principal_id, r.tool, r.scope,
       r.account_ref, r.connection_id, r.connection_generation, r.brief_id, r.status, r.http_status,
       CASE WHEN r.error ~ '^[a-z0-9_]{1,64}$' THEN r.error END AS error_code, r.row_count, r.rows,
       r.started_at, r.finished_at, r.elapsed_ms
  FROM receipts r;
COMMENT ON VIEW reporting.receipts IS
    'One row per receipt: a tool call, or a read inside one (parent_id). rows are the record references it '
    'returned. A call''s arguments, its request ID and its token''s fingerprint are left out.';
COMMENT ON COLUMN reporting.receipts.error_code IS
    'Why the call failed, when that is a code such as request_failed; null otherwise.';
COMMENT ON COLUMN reporting.receipts.started_at IS 'When the receipt was committed, before the call ran, by the database clock.';
COMMENT ON COLUMN reporting.receipts.finished_at IS 'When the receipt was finished, by the database clock; null while pending.';

GRANT USAGE ON SCHEMA reporting TO gtm_reporting;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO gtm_reporting;
