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

-- A signed-in browser session. Only the digest of its ID is stored, so a
-- copy of this table opens no session. Pre-sessions are signed tokens and
-- have no row until sign-in consumes one.
CREATE TABLE sessions (
    id_sha256 bytea PRIMARY KEY CHECK (octet_length(id_sha256) = 32),
    workspace_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    principal_kind text NOT NULL CHECK (principal_kind = 'person'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    FOREIGN KEY (workspace_id, principal_id, principal_kind)
        REFERENCES principals (workspace_id, id, kind)
);
-- Each pre-session token signs in once.
CREATE TABLE consumed_presessions (
    id_sha256 bytea PRIMARY KEY CHECK (octet_length(id_sha256) = 32),
    expires_at timestamptz NOT NULL
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
-- started under and finishes ok only if it is unchanged.
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
    source text NOT NULL CHECK (source IN ('crm', 'calls', 'slack')),
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
    -- A top-level operation ID never contains '#', which joins a parent's
    -- operation ID to a child's ordinal.
    operation_id text NOT NULL UNIQUE CHECK (length(operation_id) BETWEEN 1 AND 128),
    parent_id uuid,
    ordinal integer CHECK (ordinal >= 0),
    principal_id uuid,
    token_id text NOT NULL CHECK (token_id = 'anonymous' OR token_id ~ '^[0-9a-f]{64}$'),
    tool text NOT NULL CHECK (length(tool) BETWEEN 1 AND 100),
    scope text CHECK (scope IN ('crm:read', 'calls:read', 'slack:read', 'crm:write')),
    -- The account the request named, as given (or '<invalid>'), and the
    -- account it resolved to in this workspace, if any. A request naming an
    -- unknown account still gets its receipt.
    requested_account text CHECK (requested_account ~ '^([A-Za-z0-9._:-]{1,64}|<invalid>)$'),
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
    -- The lease token a writer presents with an update; checked by the
    -- trigger and never stored.
    finishing_lease uuid CHECK (finishing_lease IS NULL),
    UNIQUE (workspace_id, id),
    UNIQUE (parent_id, ordinal),
    CHECK (parent_id IS NOT NULL OR strpos(operation_id, '#') = 0),
    CHECK ((parent_id IS NULL) = (ordinal IS NULL)),
    CHECK ((connection_id IS NULL) = (connection_generation IS NULL)),
    CHECK (account_ref IS NULL OR requested_account = account_ref),
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

-- The record versions a source read returned, in order.
CREATE TABLE receipt_records (
    workspace_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    source_record_id uuid NOT NULL,
    PRIMARY KEY (receipt_id, ordinal),
    UNIQUE (receipt_id, source_record_id),
    UNIQUE (workspace_id, receipt_id, source_record_id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, source_record_id) REFERENCES source_records (workspace_id, id)
);
CREATE INDEX receipt_records_record ON receipt_records (source_record_id);

-- The bound records of a source read, as row references, in order.
CREATE FUNCTION gtm_bound_rows(receipt uuid) RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT coalesce(jsonb_agg(v.source || '/' || v.record_table || '/' || v.record_id ORDER BY b.ordinal),
                    '[]'::jsonb)
      FROM receipt_records b JOIN source_records v ON v.id = b.source_record_id
     WHERE b.receipt_id = receipt
$$;

-- A receipt is born pending. A child starts only under a pending parent (the
-- share lock keeps the parent from finishing first). A source read names its
-- connection, the scope of that connection's source, and its current
-- generation.
CREATE FUNCTION gtm_receipts_guard_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    current_generation bigint;
    connection_source text;
BEGIN
    IF NEW.status <> 'pending' OR NEW.finishing_lease IS NOT NULL THEN
        RAISE EXCEPTION 'a receipt starts pending' USING ERRCODE = 'GT011';
    END IF;
    IF NEW.parent_id IS NOT NULL THEN
        PERFORM 1 FROM receipts WHERE id = NEW.parent_id AND status = 'pending' FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'parent receipt % is not pending', NEW.parent_id USING ERRCODE = 'GT006';
        END IF;
    END IF;
    IF NEW.connection_id IS NOT NULL THEN
        SELECT generation, source INTO current_generation, connection_source
          FROM connections WHERE id = NEW.connection_id;
        IF NEW.parent_id IS NULL OR NEW.scope IS DISTINCT FROM connection_source || ':read'
           OR NEW.connection_generation IS DISTINCT FROM current_generation THEN
            RAISE EXCEPTION 'a source read names its connection, that source''s scope and its generation'
                USING ERRCODE = 'GT012';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipts_start_pending BEFORE INSERT ON receipts
    FOR EACH ROW EXECUTE FUNCTION gtm_receipts_guard_insert();

-- A receipt finishes once, and every update presents the lease token in
-- finishing_lease. While pending, only its lease may change: renewed by the
-- holder before it expires, or taken over (with a higher fence) after.
-- Finishing needs the live lease, and a parent finishes only after its
-- children. A source read finishes ok only at its connection's current
-- generation, claiming exactly the records bound to it, in order; otherwise
-- it binds nothing.
CREATE FUNCTION gtm_receipts_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    presented uuid := NEW.finishing_lease;
    current_generation bigint;
BEGIN
    NEW.finishing_lease := NULL;
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'receipt % is already finished', OLD.id USING ERRCODE = 'GT001';
    END IF;
    IF (NEW.id, NEW.workspace_id, NEW.operation_id, NEW.parent_id, NEW.ordinal, NEW.principal_id,
        NEW.token_id, NEW.tool, NEW.scope, NEW.requested_account, NEW.account_ref, NEW.connection_id,
        NEW.connection_generation, NEW.arguments, NEW.request_id, NEW.brief_id, NEW.started_at)
       IS DISTINCT FROM
       (OLD.id, OLD.workspace_id, OLD.operation_id, OLD.parent_id, OLD.ordinal, OLD.principal_id,
        OLD.token_id, OLD.tool, OLD.scope, OLD.requested_account, OLD.account_ref, OLD.connection_id,
        OLD.connection_generation, OLD.arguments, OLD.request_id, OLD.brief_id, OLD.started_at) THEN
        RAISE EXCEPTION 'a receipt''s request columns never change' USING ERRCODE = 'GT002';
    END IF;
    IF NEW.status = 'pending' THEN
        IF NEW.lease_token = OLD.lease_token THEN
            IF presented IS DISTINCT FROM OLD.lease_token OR NEW.lease_fence <> OLD.lease_fence
               OR OLD.lease_expires_at <= clock_timestamp() THEN
                RAISE EXCEPTION 'the lease on receipt % is not held', OLD.id USING ERRCODE = 'GT003';
            END IF;
        ELSIF OLD.lease_expires_at > clock_timestamp() OR NEW.lease_fence <= OLD.lease_fence THEN
            RAISE EXCEPTION 'the lease on receipt % is held', OLD.id USING ERRCODE = 'GT004';
        END IF;
        RETURN NEW;
    END IF;
    IF presented IS DISTINCT FROM OLD.lease_token
       OR (NEW.lease_token, NEW.lease_fence, NEW.lease_expires_at)
          IS DISTINCT FROM (OLD.lease_token, OLD.lease_fence, OLD.lease_expires_at)
       OR OLD.lease_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'the lease on receipt % is not held', OLD.id USING ERRCODE = 'GT003';
    END IF;
    IF EXISTS (SELECT 1 FROM receipts WHERE parent_id = OLD.id AND status = 'pending') THEN
        RAISE EXCEPTION 'receipt % has a pending child', OLD.id USING ERRCODE = 'GT005';
    END IF;
    IF OLD.connection_id IS NOT NULL THEN
        IF NEW.status = 'ok' THEN
            SELECT generation INTO current_generation FROM connections WHERE id = OLD.connection_id FOR SHARE;
            IF current_generation <> OLD.connection_generation THEN
                RAISE EXCEPTION 'the source changed after read % started', OLD.id USING ERRCODE = 'GT013';
            END IF;
            IF gtm_bound_rows(OLD.id) <> NEW.rows THEN
                RAISE EXCEPTION 'read % claims rows it did not bind', OLD.id USING ERRCODE = 'GT014';
            END IF;
        ELSIF EXISTS (SELECT 1 FROM receipt_records WHERE receipt_id = OLD.id) THEN
            RAISE EXCEPTION 'read % failed but bound records', OLD.id USING ERRCODE = 'GT014';
        END IF;
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

-- A binding is added to a pending source read, numbered in order, for a
-- record of that read's source. The row lock serializes bindings and the
-- finish; the deferred check makes the bindings commit with an ok finish.
CREATE FUNCTION gtm_receipt_records_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    read_connection uuid;
BEGIN
    SELECT connection_id INTO read_connection FROM receipts
     WHERE id = NEW.receipt_id AND workspace_id = NEW.workspace_id AND status = 'pending' FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'receipt % is not pending', NEW.receipt_id USING ERRCODE = 'GT007';
    END IF;
    IF read_connection IS NULL OR NOT EXISTS (
        SELECT 1 FROM source_records v JOIN connections c ON c.id = read_connection
         WHERE v.id = NEW.source_record_id AND v.source = c.source) THEN
        RAISE EXCEPTION 'receipt % reads no source of record %', NEW.receipt_id, NEW.source_record_id
            USING ERRCODE = 'GT015';
    END IF;
    IF NEW.ordinal <> (SELECT count(*) FROM receipt_records WHERE receipt_id = NEW.receipt_id) THEN
        RAISE EXCEPTION 'bindings of receipt % are numbered in order', NEW.receipt_id USING ERRCODE = 'GT015';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER receipt_records_while_pending BEFORE INSERT ON receipt_records
    FOR EACH ROW EXECUTE FUNCTION gtm_receipt_records_guard();

CREATE FUNCTION gtm_receipt_records_finished() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM receipts WHERE id = NEW.receipt_id AND status = 'ok';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'bindings of receipt % commit only with its ok finish', NEW.receipt_id
            USING ERRCODE = 'GT016';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER receipt_records_with_finish AFTER INSERT ON receipt_records
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION gtm_receipt_records_finished();
CREATE TRIGGER receipt_records_immutable BEFORE UPDATE OR DELETE ON receipt_records
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER receipt_records_no_truncate BEFORE TRUNCATE ON receipt_records
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

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
CREATE TRIGGER receipt_deliveries_no_truncate BEFORE TRUNCATE ON receipt_deliveries
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A card is its stored bytes plus their digest. Verification re-hashes the
-- bytes and never re-encodes them. The bytes are a JSON object whose "units"
-- and "gaps" arrays the evidence_units and gaps rows project.
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
    -- The brief's receipt; every read a unit cites is its child.
    parent_receipt_id uuid NOT NULL,
    seal_receipt_id uuid NOT NULL UNIQUE,
    unit_count integer NOT NULL CHECK (unit_count >= 0),
    gap_count integer NOT NULL CHECK (gap_count >= 0),
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, brief_id),
    CHECK (kind = 'prepared' OR rules_version IS NOT NULL),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, parent_receipt_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, seal_receipt_id) REFERENCES receipts (workspace_id, id)
);
CREATE INDEX cards_account ON cards (workspace_id, account_ref, published_at);
CREATE TRIGGER cards_immutable BEFORE UPDATE OR DELETE ON cards
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER cards_no_truncate BEFORE TRUNCATE ON cards
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- A unit is identified by its card and number. A Go card binds each unit to
-- its read receipt and to a record version that receipt bound. Admitted
-- Python cards bind by tool name, so both may be absent for them.
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
    CHECK (source_record_id IS NULL OR receipt_id IS NOT NULL),
    FOREIGN KEY (workspace_id, card_id) REFERENCES cards (workspace_id, id),
    FOREIGN KEY (workspace_id, receipt_id) REFERENCES receipts (workspace_id, id),
    FOREIGN KEY (workspace_id, source_record_id) REFERENCES source_records (workspace_id, id),
    FOREIGN KEY (workspace_id, receipt_id, source_record_id)
        REFERENCES receipt_records (workspace_id, receipt_id, source_record_id)
);
CREATE INDEX evidence_units_receipt ON evidence_units (receipt_id);
CREATE TRIGGER evidence_units_immutable BEFORE UPDATE OR DELETE ON evidence_units
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER evidence_units_no_truncate BEFORE TRUNCATE ON evidence_units
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

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
CREATE TRIGGER gaps_no_truncate BEFORE TRUNCATE ON gaps
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- What is wrong with a card, or NULL. The seal finished ok naming the bytes'
-- digest; the units and gaps are exactly the bytes' "units" and "gaps"; each
-- cited read finished ok, on the card's account, under the card's brief,
-- binding the cited record version. At publication the reads' generations
-- must also be current; a restore checks everything else.
CREATE FUNCTION gtm_card_violation(card uuid, at_publication boolean) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    c cards%ROWTYPE;
    doc jsonb;
    bad text;
BEGIN
    SELECT * INTO c FROM cards WHERE id = card;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    PERFORM 1 FROM receipts r
     WHERE r.id = c.seal_receipt_id AND r.status = 'ok' AND r.account_ref = c.account_ref
       AND r.brief_id = c.brief_id
       AND r.rows = jsonb_build_array('briefs/' || c.brief_id || '/' || encode(c.card_sha256, 'hex'));
    IF NOT FOUND THEN
        RETURN 'card ' || c.id || ' has no finished seal naming its bytes';
    END IF;
    PERFORM 1 FROM receipts r WHERE r.id = c.parent_receipt_id AND r.account_ref = c.account_ref
       AND r.parent_id IS NULL;
    IF NOT FOUND THEN
        RETURN 'card ' || c.id || ' names a brief receipt of another account';
    END IF;
    BEGIN
        doc := convert_from(c.card_bytes, 'UTF8')::jsonb;
    EXCEPTION WHEN others THEN
        RETURN 'card ' || c.id || ' is not a JSON document';
    END;
    IF jsonb_typeof(doc -> 'units') IS DISTINCT FROM 'array' OR jsonb_typeof(doc -> 'gaps') IS DISTINCT FROM 'array'
       OR jsonb_array_length(doc -> 'units') <> c.unit_count OR jsonb_array_length(doc -> 'gaps') <> c.gap_count
       OR (SELECT count(*) FROM evidence_units WHERE card_id = c.id) <> c.unit_count
       OR (SELECT count(*) FROM gaps WHERE card_id = c.id) <> c.gap_count THEN
        RETURN 'card ' || c.id || ' does not hold exactly its units and gaps';
    END IF;
    SELECT 'unit ' || u.n INTO bad FROM evidence_units u
     WHERE u.card_id = c.id
       AND (u.n > c.unit_count OR u.body IS DISTINCT FROM doc -> 'units' -> (u.n - 1)
            OR u.body ->> 'kind' IS DISTINCT FROM u.kind OR u.body ->> 'record_ref' IS DISTINCT FROM u.record_ref
            OR u.body ->> 'tool' IS DISTINCT FROM u.tool
            OR (u.kind = 'sentence' AND u.body ->> 'shown' IS DISTINCT FROM u.shown))
     LIMIT 1;
    IF bad IS NULL THEN
        SELECT 'gap ' || g.code INTO bad FROM gaps g
         WHERE g.card_id = c.id
           AND (g.ordinal >= c.gap_count OR doc -> 'gaps' -> g.ordinal IS DISTINCT FROM jsonb_build_object(
                'code', g.code, 'disposition', g.disposition, 'count', g.count, 'by_role', g.by_role,
                'text', g.text))
         LIMIT 1;
    END IF;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' differs from the sealed bytes';
    END IF;
    SELECT 'unit ' || u.n INTO bad FROM evidence_units u
      LEFT JOIN receipts r ON r.id = u.receipt_id
      LEFT JOIN connections cn ON cn.id = r.connection_id
      LEFT JOIN source_records v ON v.id = u.source_record_id
     WHERE u.card_id = c.id
       AND ((c.kind = 'go' AND u.source_record_id IS NULL)
            OR (u.receipt_id IS NOT NULL
                AND (r.status <> 'ok' OR r.account_ref IS DISTINCT FROM c.account_ref
                     OR r.parent_id IS DISTINCT FROM c.parent_receipt_id OR r.connection_id IS NULL
                     OR (at_publication AND r.connection_generation <> cn.generation)))
            OR (u.source_record_id IS NOT NULL
                AND v.source || '/' || v.record_table || '/' || v.record_id <> u.record_ref))
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RETURN 'card ' || c.id || ': ' || bad || ' cites a read the card may not rest on';
    END IF;
    RETURN NULL;
END
$$;

-- At commit, a new card passes gtm_card_violation, and no unit or gap is
-- added to a card afterwards (its counts would no longer match).
CREATE FUNCTION gtm_cards_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    problem text;
BEGIN
    IF TG_TABLE_NAME = 'cards' THEN
        problem := gtm_card_violation(NEW.id, true);
    ELSIF TG_TABLE_NAME = 'evidence_units' THEN
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.unit_count = (SELECT count(*) FROM evidence_units WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a unit was added to published card ' || NEW.card_id;
        END IF;
    ELSE
        PERFORM 1 FROM cards c WHERE c.id = NEW.card_id
           AND c.gap_count = (SELECT count(*) FROM gaps WHERE card_id = c.id);
        IF NOT FOUND THEN
            problem := 'a gap was added to published card ' || NEW.card_id;
        END IF;
    END IF;
    IF problem IS NOT NULL THEN
        RAISE EXCEPTION '%', problem USING ERRCODE = 'GT009';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER cards_checked AFTER INSERT ON cards
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_cards_check();
CREATE CONSTRAINT TRIGGER evidence_units_counted AFTER INSERT ON evidence_units
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_cards_check();
CREATE CONSTRAINT TRIGGER gaps_counted AFTER INSERT ON gaps
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gtm_cards_check();

-- Every rule the triggers enforce on writing, checked over rows that were
-- loaded with the triggers off (a restore). Empty means none is broken.
CREATE FUNCTION gtm_integrity_violations() RETURNS SETOF text LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
        SELECT 'receipt ' || c.id || ' is pending under a finished parent'
          FROM receipts c JOIN receipts p ON p.id = c.parent_id
         WHERE c.status = 'pending' AND p.status <> 'pending';
    RETURN QUERY
        SELECT 'receipt ' || r.id || ' does not match its connection or its bound records'
          FROM receipts r JOIN connections c ON c.id = r.connection_id
         WHERE r.parent_id IS NULL OR r.scope IS DISTINCT FROM c.source || ':read'
            OR (r.status = 'ok' AND gtm_bound_rows(r.id) <> r.rows)
            OR (r.status <> 'ok' AND EXISTS (SELECT 1 FROM receipt_records WHERE receipt_id = r.id));
    RETURN QUERY
        SELECT 'binding ' || b.receipt_id || '/' || b.ordinal || ' is out of order or of another source'
          FROM receipt_records b
          JOIN receipts r ON r.id = b.receipt_id
          JOIN source_records v ON v.id = b.source_record_id
          LEFT JOIN connections c ON c.id = r.connection_id
         WHERE c.id IS NULL OR v.source <> c.source
            OR b.ordinal >= (SELECT count(*) FROM receipt_records x WHERE x.receipt_id = b.receipt_id);
    RETURN QUERY
        SELECT 'delivery of pending receipt ' || d.receipt_id
          FROM receipt_deliveries d JOIN receipts r ON r.id = d.receipt_id WHERE r.status = 'pending';
    RETURN QUERY
        SELECT v FROM (SELECT gtm_card_violation(id, false) AS v FROM cards) x WHERE v IS NOT NULL;
END
$$;

-- Restores of this database, so an interrupted one can be finished.
CREATE TABLE restores (
    archive_sha256 text PRIMARY KEY CHECK (archive_sha256 ~ '^[0-9a-f]{64}$'),
    restored_at timestamptz NOT NULL DEFAULT now(),
    credentials_installed_at timestamptz
);
