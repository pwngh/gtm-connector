-- gtm.yaml, the file a workspace is set by: every revision that was previewed, and which one is active.
--
-- A revision is the file's exact bytes under the SHA-256 that names them, with the settings the connector read
-- from it beside them as JSON, so that a rule in the database can read a setting. Both tables are written
-- once. The active file of a workspace is its latest activation; a workspace with none runs on the defaults.

CREATE TABLE config_revisions (
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    revision text COLLATE "C" NOT NULL CHECK (revision ~ '^[0-9a-f]{64}$'),
    body bytea NOT NULL CHECK (octet_length(body) <= 65536 AND encode(sha256(body), 'hex') = revision),
    settings jsonb NOT NULL CHECK (jsonb_typeof(settings) = 'object'),
    stored_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (workspace_id, revision)
);
CREATE TRIGGER config_revisions_immutable BEFORE UPDATE OR DELETE ON config_revisions
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER config_revisions_no_truncate BEFORE TRUNCATE ON config_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

CREATE TABLE config_activations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    revision text COLLATE "C" NOT NULL,
    receipt_id uuid NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (workspace_id, revision) REFERENCES config_revisions (workspace_id, revision),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id),
    -- One receipt activates one revision, and no two activations of a workspace share a moment, so "the
    -- latest" is always one.
    UNIQUE (workspace_id, receipt_id),
    UNIQUE (workspace_id, activated_at)
);

-- What is wrong with an activation, or NULL: its receipt is an activate_config that finished ok for no
-- account and no person, the owner's, whose arguments are the revision and nothing else. This holds for good,
-- so a restore checks it too. The function and the guard below read the tables of this schema by their own
-- path, with temporary tables last, so that a writer's temporary table named receipts is not read in its
-- place.
CREATE FUNCTION gtm_config_activation_violation(a config_activations) RETURNS text LANGUAGE plpgsql STABLE
    SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    PERFORM 1 FROM receipts r
     WHERE r.workspace_id = a.workspace_id AND r.id = a.receipt_id AND r.status = 'ok'
       AND r.tool = 'activate_config' AND r.connection_id IS NULL AND r.account_ref IS NULL
       AND r.principal_id IS NULL
       AND r.arguments::jsonb = jsonb_build_object('revision', a.revision);
    IF NOT FOUND THEN
        RETURN 'activation ' || a.id || ' is not the one its receipt names';
    END IF;
    RETURN NULL;
END
$$;

-- An activation is written once, at the database's time, later than every activation of its workspace before
-- it, and it changes the active file: the file that is active is not activated again. Like a deal's close it
-- is made in a read committed transaction, which sees the activation before it.
CREATE FUNCTION gtm_config_activations_guard() RETURNS trigger LANGUAGE plpgsql
    SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
    problem text;
    latest text;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'a file is activated in a read committed transaction' USING ERRCODE = 'GT025';
    END IF;
    -- One activation of a workspace at a time.
    PERFORM pg_advisory_xact_lock(hashtextextended('config:' || NEW.workspace_id::text, 0));
    NEW.activated_at := clock_timestamp();
    PERFORM 1 FROM config_activations WHERE workspace_id = NEW.workspace_id AND activated_at >= NEW.activated_at;
    IF FOUND THEN
        RAISE EXCEPTION 'an activation is later than every one before it' USING ERRCODE = 'GT025';
    END IF;
    SELECT revision INTO latest FROM config_activations WHERE workspace_id = NEW.workspace_id
     ORDER BY activated_at DESC LIMIT 1;
    IF latest = NEW.revision THEN
        RAISE EXCEPTION 'this file is active already' USING ERRCODE = 'GT026';
    END IF;
    problem := gtm_config_activation_violation(NEW);
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT025';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER config_activations_guard BEFORE INSERT ON config_activations
    FOR EACH ROW EXECUTE FUNCTION gtm_config_activations_guard();
CREATE TRIGGER config_activations_immutable BEFORE UPDATE OR DELETE ON config_activations
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER config_activations_no_truncate BEFORE TRUNCATE ON config_activations
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A restore loads these rows with their triggers off, so gtm_integrity_violations checks the activations too.
-- It keeps every check it held, under another name. A later migration that replaces it must keep both.
ALTER FUNCTION gtm_integrity_violations() RENAME TO gtm_violations_before_config;
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT v FROM gtm_violations_before_config() v
    UNION ALL
    SELECT v FROM (SELECT gtm_config_activation_violation(a) AS v FROM config_activations a) x WHERE v IS NOT NULL
$$;

-- reporting.receipts names activate_config: the same view, column for column, with that name.
CREATE OR REPLACE VIEW reporting.receipts AS
SELECT r.workspace_id, r.id AS receipt_id, r.parent_id, r.ordinal, r.principal_id,
       CASE WHEN r.tool IN ('search_accounts', 'get_account', 'list_opportunities', 'read_call_notes',
                            'read_slack_messages', 'prepare_renewal_brief', 'read_renewal_brief', 'list_deals',
                            'read_deal_card', 'save_brief', 'log_activity',
                            'list_users', 'find_renewal_candidate', 'decide_match', 'close_deal', 'verify_card',
                            'activate_config',
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
