-- A workspace's speaker settings, beside its seller list: the company's own email domains and the
-- aliases that map a speaker label to a person. connect saves them, and every card seals them in its
-- evidence. The connector checks each entry before it saves the object; the database keeps it a JSON
-- object of bounded size.
ALTER TABLE workspaces ADD COLUMN speakers text NOT NULL DEFAULT '{}'
    CHECK (octet_length(speakers) <= 65536 AND jsonb_typeof(speakers::jsonb) = 'object');
