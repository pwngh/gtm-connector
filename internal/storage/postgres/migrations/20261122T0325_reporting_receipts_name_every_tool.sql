-- reporting.receipts names every tool the connector defines.
--
-- The view shows a receipt's tool only when it is a name the connector defines, since a caller of the MCP gate
-- may send any name. Its first list held the tools of the gate and the seal alone, so the receipts of the
-- connector's own steps read as <invalid>: a read of the org's users or of the contract a renewal plans
-- from, a match decision, a grant or its end, a Slack sign-in step, and each step of a Salesforce check or
-- connect. This is the same view, column for column, with those names.

CREATE OR REPLACE VIEW reporting.receipts AS
SELECT r.workspace_id, r.id AS receipt_id, r.parent_id, r.ordinal, r.principal_id,
       CASE WHEN r.tool IN ('search_accounts', 'get_account', 'list_opportunities', 'read_call_notes',
                            'read_slack_messages', 'prepare_renewal_brief', 'read_renewal_brief', 'list_deals',
                            'read_deal_card', 'save_brief', 'log_activity',
                            'list_users', 'find_renewal_candidate', 'decide_match',
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
