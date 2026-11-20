-- Identity and evidence for G1.
--
-- Every table that belongs to a workspace carries workspace_id, and every
-- foreign key between two such tables includes it, so a row can only point at
-- rows of its own workspace. Receipts, record versions, cards, units and gaps
-- are durable: triggers refuse to change or delete them once written.

CREATE FUNCTION gtm_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% rows are never changed or deleted', TG_TABLE_NAME
        USING ERRCODE = 'GT010';
END
$$;

-- Identity ------------------------------------------------------------------

CREATE TABLE workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    -- Demo data and a real source never share a workspace.
    kind text NOT NULL CHECK (kind IN ('demo', 'live')),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, kind)
);

CREATE TABLE principals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    workspace_kind text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('person', 'machine')),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    -- Password sign-in exists only for demo people in a demo workspace. The
    -- credential is 32 random bytes, so its SHA-256 is stored, not a slow hash.
    login text UNIQUE CHECK (login ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    credential_sha256 bytea CHECK (octet_length(credential_sha256) = 32),
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, id, kind),
    FOREIGN KEY (workspace_id, workspace_kind) REFERENCES workspaces (id, kind),
    CHECK ((login IS NULL) = (credential_sha256 IS NULL)),
    CHECK (login IS NULL OR (workspace_kind = 'demo' AND kind = 'person'))
);

CREATE TABLE accounts (
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    ref text NOT NULL CHECK (ref ~ '^acc-([0-9]{3}|sf-[A-Za-z0-9]{18})$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, ref)
);

CREATE TABLE account_grants (
    workspace_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    account_ref text NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    PRIMARY KEY (workspace_id, principal_id, account_ref),
    FOREIGN KEY (workspace_id, principal_id) REFERENCES principals (workspace_id, id),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

-- A browser session. A pre-session (no principal) exists only so sign-in
-- has a CSRF token, which is derived from the session ID. Only the ID's
-- digest is stored, so a copy of this table opens no session.
CREATE TABLE sessions (
    id_sha256 bytea PRIMARY KEY CHECK (octet_length(id_sha256) = 32),
    workspace_id uuid,
    principal_id uuid,
    principal_kind text CHECK (principal_kind = 'person'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK ((workspace_id IS NULL) = (principal_id IS NULL)
       AND (principal_id IS NULL) = (principal_kind IS NULL)),
    FOREIGN KEY (workspace_id, principal_id, principal_kind)
        REFERENCES principals (workspace_id, id, kind)
);
CREATE INDEX sessions_expires_at ON sessions (expires_at);
CREATE INDEX sessions_principal ON sessions (workspace_id, principal_id);

CREATE TABLE machine_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    principal_kind text NOT NULL DEFAULT 'machine' CHECK (principal_kind = 'machine'),
    token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
    scopes text[] NOT NULL CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['crm:read', 'calls:read', 'slack:read', 'crm:write']::text[]),
    -- The accounts this token may read; NULL means every account it is granted.
    selection text[] CHECK (selection IS NULL OR cardinality(selection) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    FOREIGN KEY (workspace_id, principal_id, principal_kind)
        REFERENCES principals (workspace_id, id, kind)
);

-- Evidence ------------------------------------------------------------------

-- What a workspace's sources are connected to. No secret is stored here.
-- Changing a source bumps generation; a read records the generation it
-- started under and finishes only if it is unchanged.
CREATE TABLE connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    workspace_kind text NOT NULL,
    source text NOT NULL CHECK (source IN ('crm', 'calls', 'slack')),
    mode text NOT NULL CHECK (mode IN ('demo', 'fixture', 'imported', 'sf', 'env_token', 'slack', 'none')),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    identity_sha256 text CHECK (identity_sha256 ~ '^[0-9a-f]{64}$'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, source),
    FOREIGN KEY (workspace_id, workspace_kind) REFERENCES workspaces (id, kind),
    CHECK (mode = 'none' OR (workspace_kind = 'demo') = (mode IN ('demo', 'fixture')))
);

-- One immutable version of one source record: source, table, record ID and
-- content version identify it (the unit identity model).
CREATE TABLE source_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    source text NOT NULL CHECK (source ~ '^[a-z][a-z0-9_]{0,31}$'),
    record_table text NOT NULL CHECK (record_table ~ '^[a-z][a-z0-9_]{0,31}$'),
    record_id text NOT NULL CHECK (length(record_id) BETWEEN 1 AND 200),
    content_version text NOT NULL CHECK (length(content_version) BETWEEN 1 AND 200),
    raw_body bytea NOT NULL,
    body_sha256 bytea NOT NULL CHECK (body_sha256 = sha256(raw_body)),
    first_read_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, source, record_table, record_id, content_version)
);
CREATE TRIGGER source_records_immutable BEFORE UPDATE OR DELETE ON source_records
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER source_records_no_truncate BEFORE TRUNCATE ON source_records
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A receipt: today's audit_log columns plus the workspace, the principal, a
-- parent link, an operation ID, the connection generation and a lease.
CREATE TABLE receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    operation_id text NOT NULL UNIQUE CHECK (length(operation_id) BETWEEN 1 AND 128),
    parent_id uuid,
    ordinal integer CHECK (ordinal >= 0),
    principal_id uuid,
    token_id text NOT NULL CHECK (token_id = 'anonymous' OR token_id ~ '^[0-9a-f]{64}$'),
    tool text NOT NULL CHECK (length(tool) BETWEEN 1 AND 100),
    scope text CHECK (scope IN ('crm:read', 'calls:read', 'slack:read', 'crm:write')),
    account_ref text,
    connection_id uuid,
    connection_generation bigint,
    arguments jsonb NOT NULL,
    request_id jsonb NOT NULL,
    brief_id text,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error')),
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    row_count integer NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    rows jsonb NOT NULL DEFAULT '[]',
    elapsed_ms double precision CHECK (elapsed_ms >= 0),
    error text,
    finished_at timestamptz,
    lease_token uuid NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    lease_fence bigint NOT NULL DEFAULT 1 CHECK (lease_fence >= 1),
    UNIQUE (workspace_id, id),
    UNIQUE (parent_id, ordinal),
    CHECK ((parent_id IS NULL) = (ordinal IS NULL)),
    CHECK ((connection_id IS NULL) = (connection_generation IS NULL)),
    CHECK ((status = 'pending') = (finished_at IS NULL)),
    CHECK ((status = 'pending') = (http_status IS NULL)),
    CHECK (status <> 'pending' OR (row_count = 0 AND elapsed_ms IS NULL AND error IS NULL)),
    -- A denial or failure finishes with no rows.
    CHECK (status = 'ok' OR row_count = 0),
    CHECK (jsonb_typeof(rows) = 'array' AND jsonb_array_length(rows) = row_count),
    FOREIGN KEY (workspace_id, parent_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, principal_id) REFERENCES principals (workspace_id, id),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, connection_id) REFERENCES connections (workspace_id, id)
);
CREATE INDEX receipts_account ON receipts (workspace_id, account_ref);
CREATE INDEX receipts_parent ON receipts (parent_id);
CREATE INDEX receipts_pending_lease ON receipts (lease_expires_at) WHERE status = 'pending';

-- A receipt finishes once. While pending, only its lease may change: renewed
-- by the holder before it expires, or taken over (with a higher fence) after.
-- Finishing needs a live lease, and a parent finishes only after its children.
-- The application's UPDATE ... WHERE status = 'pending' is the first guard;
-- this trigger is the second, for any other writer.
CREATE FUNCTION gtm_receipts_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'receipt % is already finished', OLD.id USING ERRCODE = 'GT001';
    END IF;
    IF (NEW.id, NEW.workspace_id, NEW.operation_id, NEW.parent_id, NEW.ordinal, NEW.principal_id,
        NEW.token_id, NEW.tool, NEW.scope, NEW.account_ref, NEW.connection_id,
        NEW.connection_generation, NEW.arguments, NEW.request_id, NEW.brief_id, NEW.started_at)
       IS DISTINCT FROM
       (OLD.id, OLD.workspace_id, OLD.operation_id, OLD.parent_id, OLD.ordinal, OLD.principal_id,
        OLD.token_id, OLD.tool, OLD.scope, OLD.account_ref, OLD.connection_id,
        OLD.connection_generation, OLD.arguments, OLD.request_id, OLD.brief_id, OLD.started_at) THEN
        RAISE EXCEPTION 'a receipt''s request columns never change' USING ERRCODE = 'GT002';
    END IF;
    IF NEW.status = 'pending' THEN
        IF NEW.lease_token = OLD.lease_token THEN
            IF NEW.lease_fence <> OLD.lease_fence OR OLD.lease_expires_at <= clock_timestamp() THEN
                RAISE EXCEPTION 'the lease on receipt % has expired', OLD.id USING ERRCODE = 'GT003';
            END IF;
        ELSIF OLD.lease_expires_at > clock_timestamp() OR NEW.lease_fence <= OLD.lease_fence THEN
            RAISE EXCEPTION 'the lease on receipt % is held', OLD.id USING ERRCODE = 'GT004';
        END IF;
        RETURN NEW;
    END IF;
    IF (NEW.lease_token, NEW.lease_fence, NEW.lease_expires_at)
       IS DISTINCT FROM (OLD.lease_token, OLD.lease_fence, OLD.lease_expires_at)
       OR OLD.lease_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'the lease on receipt % has expired', OLD.id USING ERRCODE = 'GT003';
    END IF;
    IF EXISTS (SELECT 1 FROM receipts WHERE parent_id = OLD.id AND status = 'pending') THEN
        RAISE EXCEPTION 'receipt % has a pending child', OLD.id USING ERRCODE = 'GT005';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipts_finish_once BEFORE UPDATE ON receipts
    FOR EACH ROW EXECUTE FUNCTION gtm_receipts_guard_update();
CREATE TRIGGER receipts_no_delete BEFORE DELETE ON receipts
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER receipts_no_truncate BEFORE TRUNCATE ON receipts
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A child receipt starts only under a pending parent. The share lock keeps
-- the parent from finishing until the child is committed.
CREATE FUNCTION gtm_receipts_guard_child() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM receipts WHERE id = NEW.parent_id AND status = 'pending' FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'parent receipt % is not pending', NEW.parent_id USING ERRCODE = 'GT006';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipts_child_under_pending_parent BEFORE INSERT ON receipts
    FOR EACH ROW WHEN (NEW.parent_id IS NOT NULL) EXECUTE FUNCTION gtm_receipts_guard_child();

-- The record versions a read receipt returned, in order. Bound only while the
-- receipt is pending, in the transaction that finishes it.
CREATE TABLE receipt_records (
    workspace_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    source_record_id uuid NOT NULL,
    PRIMARY KEY (receipt_id, ordinal),
    UNIQUE (receipt_id, source_record_id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, source_record_id) REFERENCES source_records (workspace_id, id)
);
CREATE INDEX receipt_records_record ON receipt_records (source_record_id);

CREATE FUNCTION gtm_receipt_records_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM receipts WHERE id = NEW.receipt_id AND status = 'pending';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'receipt % is not pending', NEW.receipt_id USING ERRCODE = 'GT007';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipt_records_while_pending BEFORE INSERT ON receipt_records
    FOR EACH ROW EXECUTE FUNCTION gtm_receipt_records_guard();
CREATE TRIGGER receipt_records_immutable BEFORE UPDATE OR DELETE ON receipt_records
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();

-- "Committed" is the receipt's own state; "delivered to the caller" is
-- recorded here, separately, once the reply has been handed over.
CREATE TABLE receipt_deliveries (
    receipt_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    delivered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id)
);

CREATE FUNCTION gtm_receipt_deliveries_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM receipts WHERE id = NEW.receipt_id AND status <> 'pending';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'receipt % is not finished', NEW.receipt_id USING ERRCODE = 'GT008';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipt_deliveries_after_finish BEFORE INSERT ON receipt_deliveries
    FOR EACH ROW EXECUTE FUNCTION gtm_receipt_deliveries_guard();
CREATE TRIGGER receipt_deliveries_immutable BEFORE UPDATE OR DELETE ON receipt_deliveries
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();

-- A card is its stored bytes plus their digest. Verification re-hashes the
-- bytes and never re-encodes them.
CREATE TABLE cards (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    account_ref text NOT NULL,
    brief_id text NOT NULL CHECK (brief_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    -- cli: saved by brief or demo; prepared: an MCP prepared brief; go: made here.
    kind text NOT NULL CHECK (kind IN ('cli', 'prepared', 'go')),
    schema_version integer NOT NULL CHECK (schema_version >= 1),
    rules_version text,
    card_bytes bytea NOT NULL,
    card_sha256 bytea NOT NULL CHECK (card_sha256 = sha256(card_bytes)),
    seal_receipt_id uuid NOT NULL UNIQUE,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, brief_id),
    CHECK (kind = 'prepared' OR rules_version IS NOT NULL),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, seal_receipt_id) REFERENCES receipts (workspace_id, id)
);
CREATE INDEX cards_account ON cards (workspace_id, account_ref, published_at);
CREATE TRIGGER cards_immutable BEFORE UPDATE OR DELETE ON cards
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER cards_no_truncate BEFORE TRUNCATE ON cards
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- At commit, a card's seal receipt must have finished ok, for the card's
-- account and brief, naming the digest of exactly these bytes.
CREATE FUNCTION gtm_cards_check_seal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM receipts
     WHERE id = NEW.seal_receipt_id AND status = 'ok' AND account_ref = NEW.account_ref
       AND brief_id = NEW.brief_id
       AND rows = jsonb_build_array('briefs/' || NEW.brief_id || '/' || encode(NEW.card_sha256, 'hex'));
    IF NOT FOUND THEN
        RAISE EXCEPTION 'card % has no finished seal for its account', NEW.id USING ERRCODE = 'GT009';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER cards_sealed AFTER INSERT ON cards
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION gtm_cards_check_seal();

-- A unit is identified by its card and number, and is bound to its record
-- version and to its read receipt by ID. Admitted Python cards bind by tool
-- name, so both bindings may be absent for them.
CREATE TABLE evidence_units (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL,
    n integer NOT NULL CHECK (n >= 1),
    kind text NOT NULL CHECK (kind IN ('record', 'sentence')),
    record_ref text NOT NULL CHECK (length(record_ref) BETWEEN 1 AND 300),
    tool text NOT NULL CHECK (length(tool) BETWEEN 1 AND 100),
    shown text CHECK (shown IN ('yes', 'cut', 'later', 'long', 'more')),
    receipt_id uuid,
    source_record_id uuid,
    body jsonb NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    PRIMARY KEY (card_id, n),
    CHECK ((kind = 'sentence') = (shown IS NOT NULL)),
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, source_record_id) REFERENCES source_records (workspace_id, id)
);
CREATE INDEX evidence_units_receipt ON evidence_units (receipt_id);
CREATE TRIGGER evidence_units_immutable BEFORE UPDATE OR DELETE ON evidence_units
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();

-- Each gap code appears at most once per card.
CREATE TABLE gaps (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    code text NOT NULL CHECK (code IN (
        'crm_unmapped_fields', 'calls_denied', 'calls_excerpt', 'calls_outside_records',
        'slack_not_requested', 'slack_denied', 'slack_thread', 'slack_channel',
        'calls_not_connected', 'slack_not_connected', 'slack_not_selected',
        'crm_more_opportunities', 'crm_more_contracts', 'crm_more_contacts', 'sentences_cut',
        'sentences_more', 'sentences_long', 'sentences_later', 'crm_fields_empty',
        'crm_no_opportunity', 'calls_no_record', 'slack_no_message', 'calls_no_transcript',
        'speakers_unknown', 'neutralized', 'calls_no_contact')),
    disposition text NOT NULL CHECK (disposition IN ('not_read', 'read_not_shown', 'note')),
    count integer CHECK (count >= 0),
    by_role jsonb CHECK (by_role IS NULL OR jsonb_typeof(by_role) = 'object'),
    text text NOT NULL,
    PRIMARY KEY (card_id, code),
    UNIQUE (card_id, ordinal),
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id)
);
CREATE TRIGGER gaps_immutable BEFORE UPDATE OR DELETE ON gaps
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
