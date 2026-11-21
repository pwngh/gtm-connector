-- Imported call transcripts (internal/sources/calls).
--
-- An import is one file's bytes, kept once per workspace under their SHA-256, and one or more revisions:
-- each the manifest of one excerpt of the file for one account, named by the SHA-256 of the manifest's
-- text. Bytes and revisions never change and are never deleted, and the checks below hold a row to its
-- own digest for any writer. What changes is which account the bytes are associated with, counted by
-- generation, and which revision each account's cards read.

-- The CRM a workspace's imports are bound to, as the canonical JSON of its binding. The first import
-- writes it, and every import locks it, so imports of one workspace run one at a time.
CREATE TABLE call_import_stores (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id),
    crm_binding text NOT NULL CHECK (length(crm_binding) BETWEEN 1 AND 4096)
);
CREATE TRIGGER call_import_stores_immutable BEFORE UPDATE OR DELETE ON call_import_stores
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER call_import_stores_no_truncate BEFORE TRUNCATE ON call_import_stores
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- The bytes of one imported file, and the account they are associated with now. Moving them to another
-- account is one generation more.
CREATE TABLE call_import_contents (
    workspace_id uuid NOT NULL REFERENCES call_import_stores (workspace_id),
    content_sha256 text NOT NULL,
    raw bytea NOT NULL CHECK (octet_length(raw) BETWEEN 1 AND 2097152),
    account_ref text NOT NULL,
    generation bigint NOT NULL CHECK (generation >= 1),
    PRIMARY KEY (workspace_id, content_sha256),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    CHECK (content_sha256 = encode(sha256(raw), 'hex'))
);

CREATE FUNCTION gtm_call_import_contents_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.workspace_id = OLD.workspace_id AND NEW.content_sha256 = OLD.content_sha256
       AND NEW.raw = OLD.raw
       AND NEW.generation = OLD.generation + (NEW.account_ref <> OLD.account_ref)::int THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'imported bytes are never changed or deleted, and move to another account one generation at a time'
        USING ERRCODE = 'GT010';
END
$$;
CREATE TRIGGER call_import_contents_guard BEFORE UPDATE OR DELETE ON call_import_contents
    FOR EACH ROW EXECUTE FUNCTION gtm_call_import_contents_guard();
CREATE TRIGGER call_import_contents_no_truncate BEFORE TRUNCATE ON call_import_contents
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- One saved import: the manifest of one excerpt, as its canonical JSON text. request_key is the digest of
-- what makes two requests the same import, so a repeated request finds its revision.
CREATE TABLE call_import_revisions (
    workspace_id uuid NOT NULL,
    import_id text COLLATE "C" NOT NULL,
    request_key text NOT NULL CHECK (request_key ~ '^[0-9a-f]{64}$'),
    content_sha256 text NOT NULL,
    account_ref text NOT NULL,
    manifest text NOT NULL CHECK (octet_length(manifest) <= 131072),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, import_id),
    UNIQUE (workspace_id, request_key),
    UNIQUE (workspace_id, import_id, account_ref),
    FOREIGN KEY (workspace_id, content_sha256) REFERENCES call_import_contents (workspace_id, content_sha256),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    CHECK (import_id = 'note-' || encode(sha256(convert_to(manifest, 'UTF8')), 'hex'))
);
CREATE INDEX call_import_revisions_content ON call_import_revisions (workspace_id, content_sha256);
CREATE TRIGGER call_import_revisions_immutable BEFORE UPDATE OR DELETE ON call_import_revisions
    FOR EACH ROW EXECUTE FUNCTION gtm_refuse_change();
CREATE TRIGGER call_import_revisions_no_truncate BEFORE TRUNCATE ON call_import_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();

-- The account each AccountNumber last resolved to. The number is the operator's private selector and is
-- in no manifest.
CREATE TABLE call_import_numbers (
    workspace_id uuid NOT NULL REFERENCES call_import_stores (workspace_id),
    account_number text NOT NULL CHECK (length(account_number) BETWEEN 1 AND 40),
    account_ref text NOT NULL,
    PRIMARY KEY (workspace_id, account_number),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref)
);

-- The revision each account's cards read, or none once its content moved to another account. A revision
-- is the active one of its own account only.
CREATE TABLE call_import_active (
    workspace_id uuid NOT NULL,
    account_ref text NOT NULL,
    import_id text COLLATE "C",
    PRIMARY KEY (workspace_id, account_ref),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    FOREIGN KEY (workspace_id, import_id, account_ref)
        REFERENCES call_import_revisions (workspace_id, import_id, account_ref)
);
