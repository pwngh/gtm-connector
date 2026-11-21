-- What connect saves for a workspace: each source's binding, and the sellers the operator lists.

-- A connection's binding is what the operator chose for the source, such as a sign-in's alias, as the
-- text of one JSON object. connect status shows it, so it holds no credential, organization ID or
-- address: what pins a source to one identity is identity_sha256, a digest.
ALTER TABLE connections ADD COLUMN binding text NOT NULL DEFAULT '{}'
    CHECK (octet_length(binding) <= 4096 AND jsonb_typeof(binding::jsonb) = 'object');

-- Whether a list is one a workspace may hold as its sellers: at most 200 names or Slack IDs, each of 1
-- to 200 characters.
CREATE FUNCTION gtm_seller_list(sellers text[]) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT cardinality(sellers) <= 200
       AND NOT EXISTS (SELECT 1 FROM unnest(sellers) AS seller
                        WHERE seller IS NULL OR length(seller) NOT BETWEEN 1 AND 200)
$$;

-- The identities a workspace's cards read as sellers, in the order the operator gave them.
ALTER TABLE workspaces ADD COLUMN sellers text[] NOT NULL DEFAULT '{}' CHECK (gtm_seller_list(sellers));
