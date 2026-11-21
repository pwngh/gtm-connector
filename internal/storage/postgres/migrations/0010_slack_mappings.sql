-- The Slack thread selected for each account of a workspace (debug slack map): one thread per account and
-- one account per thread.
--
-- mapping is the selection as the Slack reader takes it, as canonical JSON: the account, the CRM and Slack
-- bindings it was made under, the channel and the parent's timestamp, when it was selected, the receipt of
-- the CRM read that found the account, and the digest of all that as its ID. It holds no credential.
-- An account whose thread moved to another account keeps its row with no thread, so no row is deleted.
CREATE TABLE slack_mappings (
    workspace_id uuid NOT NULL,
    account_ref text NOT NULL,
    channel_id text COLLATE "C" CHECK (channel_id ~ '^[CG][A-Z0-9]{1,31}$'),
    thread_ts text COLLATE "C" CHECK (thread_ts ~ '^(0|[1-9][0-9]{0,11})\.[0-9]{6}$'),
    mapping text CHECK (octet_length(mapping) <= 8192 AND jsonb_typeof(mapping::jsonb) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, account_ref),
    FOREIGN KEY (workspace_id, account_ref) REFERENCES accounts (workspace_id, ref),
    UNIQUE (workspace_id, channel_id, thread_ts),
    CHECK ((channel_id IS NULL) = (thread_ts IS NULL) AND (channel_id IS NULL) = (mapping IS NULL)),
    -- A mapping holds every member the reader takes, each of its type, and agrees with its row. A member
    -- that is missing makes a comparison NULL, which coalesce turns into a refusal.
    CHECK (mapping IS NULL OR coalesce(
        mapping::jsonb ?& ARRAY['schema_version', 'mapping_id', 'account_id', 'crm_binding', 'slack_binding',
                                'channel_id', 'thread_ts', 'selected_at', 'resolution_receipt_id']
        AND jsonb_typeof(mapping::jsonb -> 'crm_binding') = 'object'
        AND jsonb_typeof(mapping::jsonb -> 'slack_binding') = 'object'
        AND mapping::jsonb -> 'slack_binding' ->> 'provider' = 'slack'
        AND mapping::jsonb ->> 'account_id' = account_ref AND mapping::jsonb ->> 'channel_id' = channel_id
        AND mapping::jsonb ->> 'thread_ts' = thread_ts, false))
);
CREATE TRIGGER slack_mappings_no_truncate BEFORE TRUNCATE ON slack_mappings
    FOR EACH STATEMENT EXECUTE FUNCTION gtm_refuse_change();
